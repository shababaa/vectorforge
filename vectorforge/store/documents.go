package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"byodb"
	"byodb/vectorforge/vector"
)

// Document is one ingested file or uploaded text. Its chunks and vectors are
// stored in separate tables keyed by DocID.
type Document struct {
	ID             string            `json:"doc_id"`
	Source         string            `json:"source"`
	Path           string            `json:"path"`
	Kind           string            `json:"kind"`
	ContentHash    string            `json:"content_hash"`
	ChunkerVersion string            `json:"chunker_version"`
	ModelID        string            `json:"model_id"`
	SizeBytes      int64             `json:"size_bytes"`
	ChunkCount     int               `json:"chunk_count"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	CreatedAt      int64             `json:"created_at"`
	UpdatedAt      int64             `json:"updated_at"`
}

// Chunk is a contiguous span of a document and the unit that is embedded.
type Chunk struct {
	DocID     string `json:"doc_id"`
	Ordinal   int    `json:"ordinal"`
	VectorID  int64  `json:"vector_id"`
	Section   string `json:"section"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Tokens    int    `json:"tokens"`
	Text      string `json:"text"`
}

func checkText(field, value string, maxBytes int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidRecord, field)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%w: %s is %d bytes; maximum is %d", ErrInvalidRecord, field, len(value), maxBytes)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalidRecord, field)
	}
	// 0x00 and 0x01 would double in size under byodb's escaping and are not
	// meaningful in indexed text; ingestion strips them.
	if strings.ContainsAny(value, "\x00\x01") {
		return fmt.Errorf("%w: %s contains NUL or 0x01 bytes", ErrInvalidRecord, field)
	}
	return nil
}

func (d *Document) validate() error {
	checks := []error{
		checkText("doc_id", d.ID, MaxIDBytes, true),
		checkText("source", d.Source, maxSourceBytes, true),
		checkText("path", d.Path, MaxPathBytes, true),
		checkText("kind", d.Kind, maxKindBytes, true),
		checkText("content_hash", d.ContentHash, 128, true),
		checkText("chunker_version", d.ChunkerVersion, 64, true),
		checkText("model_id", d.ModelID, 200, true),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	if d.SizeBytes < 0 || d.ChunkCount < 0 {
		return fmt.Errorf("%w: negative size or chunk count", ErrInvalidRecord)
	}
	return nil
}

func (c *Chunk) validate() error {
	if err := checkText("section", c.Section, MaxSectionBytes, false); err != nil {
		return err
	}
	if err := checkText("text", c.Text, MaxChunkBytes, true); err != nil {
		return err
	}
	if c.Ordinal < 0 || c.VectorID < 1 || c.StartLine < 0 || c.EndLine < c.StartLine {
		return fmt.Errorf("%w: chunk %s/%d has invalid ordinal, vector id or line range", ErrInvalidRecord, c.DocID, c.Ordinal)
	}
	return nil
}

func documentRecord(d Document) (*byodb.Record, error) {
	meta := []byte("{}")
	if len(d.Metadata) > 0 {
		encoded, err := json.Marshal(d.Metadata)
		if err != nil {
			return nil, err
		}
		meta = encoded
	}
	if len(meta) > MaxMetadataBytes || strings.ContainsAny(string(meta), "\x00\x01") {
		return nil, fmt.Errorf("%w: metadata exceeds %d bytes", ErrInvalidRecord, MaxMetadataBytes)
	}
	return (&byodb.Record{}).
		AddString("doc_id", d.ID).AddString("source", d.Source).AddString("path", d.Path).
		AddString("kind", d.Kind).AddString("content_hash", d.ContentHash).
		AddString("chunker_version", d.ChunkerVersion).AddString("model_id", d.ModelID).
		AddInt64("size_bytes", d.SizeBytes).AddInt64("chunk_count", int64(d.ChunkCount)).
		AddStr("metadata", meta).AddInt64("created_at", d.CreatedAt).AddInt64("updated_at", d.UpdatedAt), nil
}

func documentFromRecord(rec byodb.Record) (Document, error) {
	d := Document{
		ID: str(rec, "doc_id"), Source: str(rec, "source"), Path: str(rec, "path"), Kind: str(rec, "kind"),
		ContentHash: str(rec, "content_hash"), ChunkerVersion: str(rec, "chunker_version"), ModelID: str(rec, "model_id"),
		SizeBytes: i64(rec, "size_bytes"), ChunkCount: int(i64(rec, "chunk_count")),
		CreatedAt: i64(rec, "created_at"), UpdatedAt: i64(rec, "updated_at"),
	}
	if raw := rec.Get("metadata").Str; len(raw) > 2 {
		if err := json.Unmarshal(raw, &d.Metadata); err != nil {
			return Document{}, fmt.Errorf("decode metadata of %s: %w", d.ID, err)
		}
	}
	return d, nil
}

func chunkFromRecord(rec byodb.Record) Chunk {
	return Chunk{
		DocID: str(rec, "doc_id"), Ordinal: int(i64(rec, "ordinal")), VectorID: i64(rec, "vector_id"),
		Section: str(rec, "section"), StartLine: int(i64(rec, "start_line")), EndLine: int(i64(rec, "end_line")),
		Tokens: int(i64(rec, "tokens")), Text: str(rec, "text"),
	}
}

// Document returns the document with id.
func (r *Reader) Document(id string) (Document, error) {
	rec := (&byodb.Record{}).AddString("doc_id", id)
	ok, err := r.tx.Get(tableDocuments, rec)
	if err != nil {
		return Document{}, err
	}
	if !ok {
		return Document{}, fmt.Errorf("document %s: %w", id, ErrNotFound)
	}
	return documentFromRecord(*rec)
}

// Documents calls fn for every document in doc_id order.
func (r *Reader) Documents(fn func(Document) error) error {
	return r.scanAll(tableDocuments, func(rec byodb.Record) error {
		d, err := documentFromRecord(rec)
		if err != nil {
			return err
		}
		return fn(d)
	})
}

// Chunks returns a document's chunks in ordinal order.
func (r *Reader) Chunks(docID string) ([]Chunk, error) {
	var out []Chunk
	key := (&byodb.Record{}).AddString("doc_id", docID)
	err := r.scanPrefix(tableChunks, []string{"doc_id", "ordinal"}, key, func(rec byodb.Record) error {
		out = append(out, chunkFromRecord(rec))
		return nil
	})
	return out, err
}

// AllChunks calls fn for every chunk in (doc_id, ordinal) order.
func (r *Reader) AllChunks(fn func(Chunk) error) error {
	return r.scanAll(tableChunks, func(rec byodb.Record) error { return fn(chunkFromRecord(rec)) })
}

// ChunkByVectorID resolves a search hit through the vector_id secondary index.
func (r *Reader) ChunkByVectorID(id int64) (Chunk, error) {
	var out *Chunk
	key := (&byodb.Record{}).AddInt64("vector_id", id)
	err := r.scanPrefix(tableChunks, []string{"vector_id"}, key, func(rec byodb.Record) error {
		c := chunkFromRecord(rec)
		out = &c
		return nil
	})
	if err != nil {
		return Chunk{}, err
	}
	if out == nil {
		return Chunk{}, fmt.Errorf("chunk for vector %d: %w", id, ErrNotFound)
	}
	return *out, nil
}

// NewVector is a chunk plus its embedding, as written by PutDocument.
type NewVector struct {
	Chunk  Chunk
	Vector []float32
	List   int // IVF list for the posting; ignored when the index is untrained
}

// PutDocument replaces a document atomically within the transaction: any
// previous version's chunks, vectors and postings are deleted and the new
// rows written. withPostings is false while the IVF index is untrained.
// It returns the vector IDs of the replaced version.
func (w *Writer) PutDocument(doc Document, vectors []NewVector, withPostings bool) ([]int64, error) {
	cfg, ok := w.store.Config()
	if !ok {
		return nil, ErrNotConfigured
	}
	if err := doc.validate(); err != nil {
		return nil, err
	}
	if doc.ModelID != cfg.ModelID {
		return nil, fmt.Errorf("%w: document embedded by %s, collection uses %s", ErrConfigMismatch, doc.ModelID, cfg.ModelID)
	}
	doc.ChunkCount = len(vectors)
	removed, err := w.DeleteDocument(doc.ID)
	if err != nil && !isNotFound(err) {
		return nil, err
	}
	rec, err := documentRecord(doc)
	if err != nil {
		return nil, err
	}
	if _, err := w.tx.Set(tableDocuments, *rec, byodb.MODE_INSERT_ONLY); err != nil {
		return nil, err
	}
	for i := range vectors {
		nv := &vectors[i]
		nv.Chunk.DocID, nv.Chunk.Ordinal = doc.ID, i
		if err := nv.Chunk.validate(); err != nil {
			return nil, err
		}
		chunk := (&byodb.Record{}).AddString("doc_id", doc.ID).AddInt64("ordinal", int64(i)).
			AddInt64("vector_id", nv.Chunk.VectorID).AddString("section", nv.Chunk.Section).
			AddInt64("start_line", int64(nv.Chunk.StartLine)).AddInt64("end_line", int64(nv.Chunk.EndLine)).
			AddInt64("tokens", int64(nv.Chunk.Tokens)).AddString("text", nv.Chunk.Text)
		if changed, err := w.tx.Set(tableChunks, *chunk, byodb.MODE_INSERT_ONLY); err != nil {
			return nil, err
		} else if !changed {
			return nil, fmt.Errorf("%w: duplicate chunk %s/%d", ErrInvalidRecord, doc.ID, i)
		}
		if err := w.putVector(nv.Chunk.VectorID, doc.ID, nv.Vector, cfg.Dimension); err != nil {
			return nil, err
		}
		if withPostings {
			if err := w.PutPosting(nv.List, nv.Chunk.VectorID); err != nil {
				return nil, err
			}
		}
	}
	return removed, nil
}

// DeleteDocument removes a document, its chunks, vector segments and IVF
// postings, returning the removed vector IDs.
func (w *Writer) DeleteDocument(id string) ([]int64, error) {
	if _, err := w.Document(id); err != nil {
		return nil, err
	}
	chunks, err := w.Chunks(id)
	if err != nil {
		return nil, err
	}
	removed := make([]int64, 0, len(chunks))
	cfg, _ := w.store.Config()
	for _, c := range chunks {
		key := (&byodb.Record{}).AddString("doc_id", id).AddInt64("ordinal", int64(c.Ordinal))
		if _, err := w.tx.Delete(tableChunks, *key); err != nil {
			return nil, err
		}
		if err := w.deleteVector(c.VectorID, cfg.Dimension); err != nil {
			return nil, err
		}
		if err := w.DeletePostingsFor(c.VectorID); err != nil {
			return nil, err
		}
		removed = append(removed, c.VectorID)
	}
	key := (&byodb.Record{}).AddString("doc_id", id)
	if _, err := w.tx.Delete(tableDocuments, *key); err != nil {
		return nil, err
	}
	return removed, nil
}

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

func (w *Writer) putVector(id int64, docID string, v []float32, dim int) error {
	if err := vector.Validate(v, dim); err != nil {
		return fmt.Errorf("vector %d: %w", id, err)
	}
	if !vector.IsUnit(v) {
		return fmt.Errorf("%w: vector %d is not unit length (norm %.6f)", ErrInvalidRecord, id, vector.Norm(v))
	}
	for seg := 0; seg < vector.SegmentCount(dim); seg++ {
		rec := (&byodb.Record{}).AddInt64("vector_id", id).AddInt64("segment", int64(seg)).
			AddString("doc_id", docID).AddStr("data", vector.Encode(nil, vector.Segment(v, seg)))
		changed, err := w.tx.Set(tableVectors, *rec, byodb.MODE_INSERT_ONLY)
		if err != nil {
			return err
		}
		if !changed {
			return fmt.Errorf("%w: duplicate vector id %d", ErrInvalidRecord, id)
		}
	}
	return nil
}

func (w *Writer) deleteVector(id int64, dim int) error {
	for seg := 0; seg < vector.SegmentCount(dim); seg++ {
		key := (&byodb.Record{}).AddInt64("vector_id", id).AddInt64("segment", int64(seg))
		if _, err := w.tx.Delete(tableVectors, *key); err != nil {
			return err
		}
	}
	return nil
}
