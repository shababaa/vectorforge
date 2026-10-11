package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"byodb/vectorforge/embed"
	"byodb/vectorforge/ingest"
	"byodb/vectorforge/store"
)

// MaxUploadBytes bounds a single uploaded document.
const MaxUploadBytes = 256 << 10

// IngestSummary describes one ingestion run.
type IngestSummary struct {
	RunID           string         `json:"run_id"`
	Source          string         `json:"source"`
	FilesSeen       int            `json:"files_seen"`
	Unchanged       int            `json:"unchanged"`
	Added           int            `json:"added"`
	Updated         int            `json:"updated"`
	Deleted         int            `json:"deleted"`
	Skipped         int            `json:"skipped"`
	Failed          int            `json:"failed"`
	ChunksEmbedded  int            `json:"chunks_embedded"`
	TruncatedChunks int            `json:"truncated_chunks"`
	EmbedMillis     int64          `json:"embed_millis"`
	CommitMillis    int64          `json:"commit_millis"`
	TotalMillis     int64          `json:"total_millis"`
	SkipReasons     map[string]int `json:"skip_reasons,omitempty"`
	Errors          []string       `json:"errors,omitempty"`
	VectorsAfter    int            `json:"vectors_after"`
	AutoTrained     bool           `json:"auto_trained"`
}

const maxSummaryErrors = 5

func (s *IngestSummary) addError(format string, args ...any) {
	s.Failed++
	if len(s.Errors) < maxSummaryErrors {
		msg := fmt.Sprintf(format, args...)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		s.Errors = append(s.Errors, msg)
	}
}

// pendingDoc is a changed document waiting to be embedded and committed.
type pendingDoc struct {
	doc    store.Document
	pieces []ingest.Piece
	isNew  bool
}

// StartIngest ingests a configured source in the background.
func (e *Engine) StartIngest(source string, force bool) (Job, error) {
	src, ok := e.sources[source]
	if !ok {
		return Job{}, fmt.Errorf("%w: unknown source %q", ErrNotFound, source)
	}
	if err := e.acquire(); err != nil {
		return Job{}, err
	}
	return e.startJob("ingest", source, func(jc *jobContext) (any, error) {
		return e.ingestLocked(e.ctx, src, force, jc)
	}), nil
}

// Ingest ingests a configured source synchronously.
func (e *Engine) Ingest(ctx context.Context, source string, force bool) (IngestSummary, error) {
	src, ok := e.sources[source]
	if !ok {
		return IngestSummary{}, fmt.Errorf("%w: unknown source %q", ErrNotFound, source)
	}
	if err := e.acquire(); err != nil {
		return IngestSummary{}, err
	}
	defer e.writeMu.Unlock()
	return e.ingestLocked(ctx, src, force, nil)
}

func (e *Engine) ingestLocked(ctx context.Context, src ingest.Source, force bool, jc *jobContext) (summary IngestSummary, err error) {
	started := e.now()
	summary = IngestSummary{RunID: newJobID(started), Source: src.Name}
	if jc != nil {
		summary.RunID = jc.id
	}
	e.recordRun(summary.RunID, statusRunning, started, nil, summary)
	defer func() {
		summary.TotalMillis = e.now().Sub(started).Milliseconds()
		summary.VectorsAfter = e.vectorCount()
		status := statusSucceeded
		if err != nil {
			status = statusFailed
			if ctx.Err() != nil {
				status = statusCancelled
			}
			if len(summary.Errors) < maxSummaryErrors {
				summary.Errors = append(summary.Errors, truncate(err.Error(), 200))
			}
		}
		finished := e.now()
		e.recordRun(summary.RunID, status, started, &finished, summary)
		e.log.Info("ingest finished", "source", src.Name, "status", status, "added", summary.Added,
			"updated", summary.Updated, "deleted", summary.Deleted, "chunks", summary.ChunksEmbedded, "ms", summary.TotalMillis)
	}()

	files, skips, err := ingest.Discover(src)
	if err != nil {
		return summary, err
	}
	summary.FilesSeen, summary.Skipped = len(files), len(skips)
	for _, s := range skips {
		addReason(&summary, s.Reason)
	}
	jc.progress(func(p *Progress) { p.FilesTotal = len(files) })

	existing := map[string]store.Document{}
	if err := e.st.View(func(r *store.Reader) error {
		return r.Documents(func(d store.Document) error {
			if d.Source == src.Name {
				existing[d.Path] = d
			}
			return nil
		})
	}); err != nil {
		return summary, err
	}

	present := map[string]bool{}
	var batch []pendingDoc
	batchChunks := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := e.commitDocs(ctx, batch, &summary, jc); err != nil {
			return err
		}
		batch, batchChunks = batch[:0], 0
		return nil
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		jc.progress(func(p *Progress) { p.FilesDone++ })
		raw, err := os.ReadFile(f.AbsPath)
		if err != nil {
			present[f.RelPath] = true // keep the indexed version of an unreadable file
			summary.addError("%s: %v", f.RelPath, err)
			continue
		}
		text, err := ingest.Sanitize(raw)
		if err != nil {
			summary.Skipped++
			addReason(&summary, err.Error())
			continue
		}
		present[f.RelPath] = true
		hash := ingest.ContentHash(raw)
		old, had := existing[f.RelPath]
		if had && !force && old.ContentHash == hash && old.ChunkerVersion == ingest.ChunkerVersion && old.ModelID == e.info.ModelID {
			summary.Unchanged++
			continue
		}
		doc := store.Document{
			ID: ingest.DocumentID(src.Name, f.RelPath), Source: src.Name, Path: f.RelPath, Kind: string(f.Kind),
			ContentHash: hash, ChunkerVersion: ingest.ChunkerVersion, ModelID: e.info.ModelID,
			SizeBytes: int64(len(raw)), CreatedAt: started.Unix(), UpdatedAt: started.Unix(),
		}
		if had {
			doc.CreatedAt = old.CreatedAt
		}
		pieces := ingest.Chunk(f.Kind, f.RelPath, text, e.cfg.Chunking)
		batch = append(batch, pendingDoc{doc: doc, pieces: pieces, isNew: !had})
		if batchChunks += len(pieces); batchChunks >= e.cfg.BatchChunks {
			if err := flush(); err != nil {
				return summary, err
			}
		}
	}
	if err := flush(); err != nil {
		return summary, err
	}

	var gone []string
	for path, d := range existing {
		if !present[path] {
			gone = append(gone, d.ID)
		}
	}
	sort.Strings(gone)
	if len(gone) > 0 {
		if err := e.deleteDocs(gone); err != nil {
			return summary, err
		}
		summary.Deleted = len(gone)
	}
	if e.shouldAutoTrain() {
		if _, err := e.rebuildLocked(ctx, RebuildOptions{}); err != nil {
			return summary, fmt.Errorf("auto-train IVF: %w", err)
		}
		summary.AutoTrained = true
	}
	return summary, nil
}

func addReason(s *IngestSummary, reason string) {
	if s.SkipReasons == nil {
		s.SkipReasons = map[string]int{}
	}
	if len(s.SkipReasons) < 8 || s.SkipReasons[reason] > 0 {
		s.SkipReasons[reason]++
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// commitDocs embeds a batch of documents and commits them in one
// transaction, then applies the change to the in-memory indexes.
func (e *Engine) commitDocs(ctx context.Context, batch []pendingDoc, summary *IngestSummary, jc *jobContext) error {
	var texts []string
	for _, p := range batch {
		for _, piece := range p.pieces {
			texts = append(texts, ingest.EmbeddingText(p.doc.Path, piece))
		}
	}
	embedStarted := time.Now()
	var res embed.Result
	if len(texts) > 0 {
		var err error
		if res, err = e.emb.Embed(ctx, texts, embed.KindDocument); err != nil {
			return fmt.Errorf("%w: embed %d chunks: %w", ErrEmbedder, len(texts), err)
		}
		if len(res.Vectors) != len(texts) {
			return fmt.Errorf("embedder returned %d vectors for %d chunks", len(res.Vectors), len(texts))
		}
	}
	summary.EmbedMillis += time.Since(embedStarted).Milliseconds()

	// e.ivf only changes under writeMu, which the caller holds.
	ivf := e.ivf
	lists := make([]int, len(texts))
	if ivf != nil {
		for i, v := range res.Vectors {
			lists[i] = ivf.Assign(v)
		}
	}
	commitStarted := time.Now()
	type written struct {
		doc     store.Document
		first   int64
		removed []int64
	}
	var out []written
	err := e.st.Update(func(w *store.Writer) error {
		out = out[:0]
		first, err := w.AllocateVectorIDs(len(texts))
		if err != nil {
			return err
		}
		next, offset := first, 0
		for _, p := range batch {
			vectors := make([]store.NewVector, len(p.pieces))
			for i, piece := range p.pieces {
				vectors[i] = store.NewVector{
					Chunk: store.Chunk{VectorID: next, Section: piece.Section, StartLine: piece.StartLine,
						EndLine: piece.EndLine, Tokens: res.Tokens[offset], Text: piece.Text},
					Vector: res.Vectors[offset], List: lists[offset],
				}
				next, offset = next+1, offset+1
			}
			removed, err := w.PutDocument(p.doc, vectors, ivf != nil)
			if err != nil {
				return fmt.Errorf("%s: %w", p.doc.Path, err)
			}
			out = append(out, written{doc: p.doc, first: next - int64(len(vectors)), removed: removed})
		}
		return nil
	})
	if err != nil {
		return err
	}
	summary.CommitMillis += time.Since(commitStarted).Milliseconds()

	e.mu.Lock()
	offset := 0
	for i, wr := range out {
		e.dropVectorsLocked(wr.removed)
		ref := &docRef{ID: wr.doc.ID, Source: wr.doc.Source, Path: wr.doc.Path, Kind: wr.doc.Kind, Chunks: len(batch[i].pieces)}
		e.docs[wr.doc.ID] = ref
		for j := range batch[i].pieces {
			id, v := wr.first+int64(j), res.Vectors[offset]
			_ = e.flat.Add(id, v) // IDs are freshly allocated, so duplicates are impossible
			if ivf != nil {
				_ = ivf.AddToList(id, v, lists[offset])
				e.sinceTrain++
			}
			e.refs[id] = ref
			offset++
		}
	}
	e.mu.Unlock()

	for _, p := range batch {
		if p.isNew {
			summary.Added++
		} else {
			summary.Updated++
		}
	}
	for _, t := range res.Truncated {
		if t {
			summary.TruncatedChunks++
		}
	}
	summary.ChunksEmbedded += len(texts)
	jc.progress(func(p *Progress) { p.ChunksEmbedded += len(texts); p.DocsCommitted += len(batch) })
	return nil
}

// dropVectorsLocked removes vectors from every in-memory index. e.mu must be
// held for writing.
func (e *Engine) dropVectorsLocked(ids []int64) {
	for _, id := range ids {
		e.flat.Remove(id)
		if e.ivf != nil {
			e.ivf.Remove(id)
		}
		delete(e.refs, id)
	}
}

// deleteDocs removes documents in one transaction.
func (e *Engine) deleteDocs(ids []string) error {
	removed := map[string][]int64{}
	if err := e.st.Update(func(w *store.Writer) error {
		for _, id := range ids {
			r, err := w.DeleteDocument(id)
			if err != nil {
				return err
			}
			removed[id] = r
		}
		return nil
	}); err != nil {
		return err
	}
	e.mu.Lock()
	for id, vectors := range removed {
		e.dropVectorsLocked(vectors)
		delete(e.docs, id)
	}
	e.mu.Unlock()
	return nil
}

// DeleteDocument removes one document and its vectors.
func (e *Engine) DeleteDocument(id string) error {
	if err := e.acquire(); err != nil {
		return err
	}
	defer e.writeMu.Unlock()
	return e.deleteDocs([]string{id})
}

// UploadRequest adds or replaces a document in the uploads source.
type UploadRequest struct {
	Path     string            `json:"path"`
	Content  string            `json:"content"`
	Kind     string            `json:"kind,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// UploadResult reports the stored document.
type UploadResult struct {
	Document store.Document `json:"document"`
	Created  bool           `json:"created"`
	Changed  bool           `json:"changed"`
}

// Upload embeds and stores text supplied by an API client.
func (e *Engine) Upload(ctx context.Context, req UploadRequest) (UploadResult, error) {
	path, err := ingest.CleanUploadPath(req.Path)
	if err != nil {
		return UploadResult{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if len(req.Content) > MaxUploadBytes {
		return UploadResult{}, fmt.Errorf("%w: content is %d bytes; maximum is %d", ErrInvalid, len(req.Content), MaxUploadBytes)
	}
	text, err := ingest.Sanitize([]byte(req.Content))
	if err != nil {
		return UploadResult{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if len(text) == 0 {
		return UploadResult{}, fmt.Errorf("%w: content is empty", ErrInvalid)
	}
	kind := ingest.Kind(req.Kind)
	if kind == "" {
		if kind, _ = ingest.KindForPath(path); kind == "" {
			kind = ingest.KindText
		}
	} else if !ingest.ValidKind(kind) {
		return UploadResult{}, fmt.Errorf("%w: unknown kind %q", ErrInvalid, req.Kind)
	}
	if len(req.Metadata) > 16 {
		return UploadResult{}, fmt.Errorf("%w: at most 16 metadata entries", ErrInvalid)
	}
	if err := e.acquire(); err != nil {
		return UploadResult{}, err
	}
	defer e.writeMu.Unlock()

	now := e.now().Unix()
	doc := store.Document{
		ID: ingest.DocumentID(UploadSource, path), Source: UploadSource, Path: path, Kind: string(kind),
		ContentHash: ingest.ContentHash([]byte(text)), ChunkerVersion: ingest.ChunkerVersion, ModelID: e.info.ModelID,
		SizeBytes: int64(len(text)), Metadata: req.Metadata, CreatedAt: now, UpdatedAt: now,
	}
	var old store.Document
	var had bool
	if err := e.st.View(func(r *store.Reader) error {
		var err error
		old, err = r.Document(doc.ID)
		had = err == nil
		if isNotFound(err) {
			return nil
		}
		return err
	}); err != nil {
		return UploadResult{}, err
	}
	if had {
		doc.CreatedAt = old.CreatedAt
		if old.ContentHash == doc.ContentHash && old.Kind == doc.Kind && old.ChunkerVersion == doc.ChunkerVersion &&
			old.ModelID == doc.ModelID && sameMetadata(old.Metadata, doc.Metadata) {
			return UploadResult{Document: old}, nil
		}
	}
	pieces := ingest.Chunk(kind, path, text, e.cfg.Chunking)
	var summary IngestSummary
	if err := e.commitDocs(ctx, []pendingDoc{{doc: doc, pieces: pieces, isNew: !had}}, &summary, nil); err != nil {
		if errors.Is(err, store.ErrInvalidRecord) {
			return UploadResult{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		return UploadResult{}, err
	}
	doc.ChunkCount = len(pieces)
	return UploadResult{Document: doc, Created: !had, Changed: true}, nil
}

func sameMetadata(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

func (e *Engine) vectorCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.flat.Len()
}

func (e *Engine) shouldAutoTrain() bool {
	at := e.cfg.IVF.AutoTrainAt
	if at <= 0 {
		return false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.ivf == nil && e.flat.Len() >= at
}

// recordRun persists a run summary; failures are logged, not returned,
// because the run's data changes have already committed or rolled back.
func (e *Engine) recordRun(id, status string, started time.Time, finished *time.Time, summary IngestSummary) {
	var end int64
	if finished != nil {
		end = finished.Unix()
	}
	if err := e.st.Update(func(w *store.Writer) error {
		return w.PutRun(id, status, started.Unix(), end, summary)
	}); err != nil {
		e.log.Warn("record ingest run", "run", id, "err", err)
	}
}
