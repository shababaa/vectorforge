// Package store persists VectorForge collections in a byodb database file.
//
// Documents, chunks, vector segments, IVF centroids and IVF postings are
// ordinary byodb tables. Every mutation runs inside one byodb transaction,
// so a document's rows and its index postings become durable together at the
// engine's meta-page fsync, or not at all.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"byodb"
	"byodb/vectorforge/vector"
)

// SchemaVersion identifies the vf_* table layout.
const SchemaVersion = 1

// MetricCosine is the only supported metric: vectors are unit-normalized and
// scored by dot product.
const MetricCosine = "cosine"

// Size limits keep every row inside byodb's 3,000-byte value limit. Text and
// sections are sanitized so they contain no 0x00/0x01 bytes and therefore do
// not expand under the engine's byte-string escaping.
const (
	MaxPathBytes     = 512
	MaxSectionBytes  = 256
	MaxChunkBytes    = 2000
	MaxMetadataBytes = 1024
	MaxIDBytes       = 64
	maxSourceBytes   = 64
	maxKindBytes     = 32
)

const (
	tableMeta      = "vf_meta"
	tableDocuments = "vf_documents"
	tableChunks    = "vf_chunks"
	tableVectors   = "vf_vectors"
	tableCentroids = "vf_ivf_centroids"
	tablePostings  = "vf_ivf_postings"
	tableRuns      = "vf_ingest_runs"
)

const (
	metaSchemaVersion = "schema_version"
	metaConfig        = "collection_config"
	metaNextVectorID  = "next_vector_id"
	metaIVF           = "ivf"
)

var (
	ErrForeignDatabase = errors.New("database contains non-VectorForge application tables")
	ErrNotConfigured   = errors.New("collection is not configured")
	ErrConfigMismatch  = errors.New("collection configuration mismatch")
	ErrNotFound        = errors.New("not found")
	ErrInvalidRecord   = errors.New("invalid record")
	ErrReadOnly        = byodb.ErrReadOnly
)

func tableDefinitions() []*byodb.TableDef {
	B, I := uint32(byodb.TYPE_BYTES), uint32(byodb.TYPE_INT64)
	return []*byodb.TableDef{
		{Name: tableMeta, Cols: []string{"key", "value"}, Types: []uint32{B, B}, PKeys: 1, Indexes: [][]string{{"key"}}},
		{Name: tableDocuments,
			Cols:    []string{"doc_id", "source", "path", "kind", "content_hash", "chunker_version", "model_id", "size_bytes", "chunk_count", "metadata", "created_at", "updated_at"},
			Types:   []uint32{B, B, B, B, B, B, B, I, I, B, I, I},
			PKeys:   1,
			Indexes: [][]string{{"doc_id"}, {"source", "path"}}},
		{Name: tableChunks,
			Cols:    []string{"doc_id", "ordinal", "vector_id", "section", "start_line", "end_line", "tokens", "text"},
			Types:   []uint32{B, I, I, B, I, I, I, B},
			PKeys:   2,
			Indexes: [][]string{{"doc_id", "ordinal"}, {"vector_id"}}},
		{Name: tableVectors, Cols: []string{"vector_id", "segment", "doc_id", "data"}, Types: []uint32{I, I, B, B}, PKeys: 2,
			Indexes: [][]string{{"vector_id", "segment"}}},
		{Name: tableCentroids, Cols: []string{"list_id", "segment", "data"}, Types: []uint32{I, I, B}, PKeys: 2,
			Indexes: [][]string{{"list_id", "segment"}}},
		// Postings are key-only rows: the inverted list is the primary-key
		// range (list_id, *); the secondary index finds a vector's list on delete.
		{Name: tablePostings, Cols: []string{"list_id", "vector_id"}, Types: []uint32{I, I}, PKeys: 2,
			Indexes: [][]string{{"list_id", "vector_id"}, {"vector_id"}}},
		{Name: tableRuns, Cols: []string{"run_id", "status", "started_at", "finished_at", "summary"}, Types: []uint32{B, B, I, I, B}, PKeys: 1,
			Indexes: [][]string{{"run_id"}, {"started_at", "run_id"}}},
	}
}

// Config fixes the embedding space of a collection. One database file holds
// exactly one collection, so vectors from different models never mix.
type Config struct {
	Dimension int    `json:"dimension"`
	Metric    string `json:"metric"`
	ModelID   string `json:"model_id"`
	CreatedAt int64  `json:"created_at"`
}

func (c Config) validate() error {
	if c.Dimension < 1 || c.Dimension > vector.MaxDimension {
		return fmt.Errorf("%w: dimension %d outside [1,%d]", ErrInvalidRecord, c.Dimension, vector.MaxDimension)
	}
	if c.Metric != MetricCosine {
		return fmt.Errorf("%w: unsupported metric %q", ErrInvalidRecord, c.Metric)
	}
	if c.ModelID == "" || len(c.ModelID) > 200 {
		return fmt.Errorf("%w: model id must be 1-200 bytes", ErrInvalidRecord)
	}
	return nil
}

// Options controls how a store is opened.
type Options struct {
	ReadOnly bool
	Now      func() time.Time
}

// Store is a VectorForge collection backed by one byodb file.
type Store struct {
	db       *byodb.DB
	readOnly bool
	now      func() time.Time
	cfg      Config
	hasCfg   bool
}

// Open opens (creating if needed) a VectorForge database. It refuses files
// that already hold other applications' tables, such as the market service's,
// so VectorForge can never mutate them.
func Open(path string, opts Options) (*Store, error) {
	db, err := byodb.OpenDBWithOptions(path, byodb.DBOptions{ReadOnly: opts.ReadOnly})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, readOnly: opts.ReadOnly, now: opts.Now}
	if s.now == nil {
		s.now = time.Now
	}
	if err := s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init() error {
	for _, def := range s.db.Tables() {
		if !strings.HasPrefix(def.Name, "vf_") && !strings.HasPrefix(def.Name, "@") {
			return fmt.Errorf("%w: found table %q; use a dedicated VectorForge database file", ErrForeignDatabase, def.Name)
		}
	}
	if s.readOnly {
		if err := s.checkSchema(); err != nil {
			return err
		}
	} else if err := s.ensureSchema(); err != nil {
		return err
	}
	return s.View(func(r *Reader) error {
		cfg, ok, err := r.config()
		s.cfg, s.hasCfg = cfg, ok
		return err
	})
}

func sameSchema(actual, expected *byodb.TableDef) bool {
	return actual.Name == expected.Name && actual.PKeys == expected.PKeys &&
		reflect.DeepEqual(actual.Cols, expected.Cols) && reflect.DeepEqual(actual.Types, expected.Types) &&
		reflect.DeepEqual(actual.Indexes, expected.Indexes)
}

func (s *Store) checkSchema() error {
	for _, expected := range tableDefinitions() {
		actual, ok := s.db.Table(expected.Name)
		if !ok {
			return fmt.Errorf("read-only database is missing table %q", expected.Name)
		}
		if !sameSchema(actual, expected) {
			return fmt.Errorf("schema mismatch for table %q", expected.Name)
		}
	}
	return nil
}

func (s *Store) ensureSchema() error {
	return s.Update(func(w *Writer) error {
		for _, expected := range tableDefinitions() {
			if actual, ok := w.tx.Table(expected.Name); ok {
				if !sameSchema(actual, expected) {
					return fmt.Errorf("schema mismatch for table %q", expected.Name)
				}
				continue
			}
			if err := w.tx.TableNew(expected); err != nil {
				return err
			}
		}
		version, ok, err := w.getMeta(metaSchemaVersion)
		if err != nil {
			return err
		}
		if ok && version != strconv.Itoa(SchemaVersion) {
			return fmt.Errorf("unsupported VectorForge schema version %s", version)
		}
		if !ok {
			return w.setMeta(metaSchemaVersion, strconv.Itoa(SchemaVersion))
		}
		return nil
	})
}

// Close releases the database file lock.
func (s *Store) Close() error { return s.db.Close() }

// ReadOnly reports whether the store rejects writes.
func (s *Store) ReadOnly() bool { return s.readOnly }

// Config returns the collection configuration, if one has been set.
func (s *Store) Config() (Config, bool) { return s.cfg, s.hasCfg }

// Configure sets the collection configuration on first use. Later calls must
// match the stored dimension, metric and model exactly.
func (s *Store) Configure(cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if s.hasCfg {
		if s.cfg.Dimension != cfg.Dimension || s.cfg.Metric != cfg.Metric || s.cfg.ModelID != cfg.ModelID {
			return fmt.Errorf("%w: database holds %s (%d-d %s); embedder reports %s (%d-d %s)",
				ErrConfigMismatch, s.cfg.ModelID, s.cfg.Dimension, s.cfg.Metric, cfg.ModelID, cfg.Dimension, cfg.Metric)
		}
		return nil
	}
	if cfg.CreatedAt == 0 {
		cfg.CreatedAt = s.now().UTC().Unix()
	}
	err := s.Update(func(w *Writer) error {
		encoded, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		return w.setMeta(metaConfig, string(encoded))
	})
	if err == nil {
		s.cfg, s.hasCfg = cfg, true
	}
	return err
}

// Stats returns engine-level storage statistics for the database file.
func (s *Store) Stats() (byodb.DBStats, error) { return s.db.Stats() }

// View runs fn in a read-only snapshot transaction.
func (s *Store) View(fn func(*Reader) error) error {
	r := &Reader{store: s}
	if err := s.db.Begin(&r.tx); err != nil {
		return err
	}
	defer s.db.Abort(&r.tx)
	return fn(r)
}

// Update runs fn in a write transaction and commits it if fn succeeds. The
// commit is the durability point for every row fn wrote.
func (s *Store) Update(fn func(*Writer) error) error {
	if s.readOnly {
		return ErrReadOnly
	}
	w := &Writer{Reader: Reader{store: s}}
	if err := s.db.Begin(&w.tx); err != nil {
		return err
	}
	if err := fn(w); err != nil {
		s.db.Abort(&w.tx)
		return err
	}
	return s.db.Commit(&w.tx)
}

// Reader exposes snapshot reads.
type Reader struct {
	store *Store
	tx    byodb.DBTX
}

// Writer extends Reader with mutations inside one transaction.
type Writer struct {
	Reader
}

func (r *Reader) getMeta(key string) (string, bool, error) {
	rec := (&byodb.Record{}).AddString("key", key)
	ok, err := r.tx.Get(tableMeta, rec)
	if err != nil || !ok {
		return "", ok, err
	}
	return string(rec.Get("value").Str), true, nil
}

func (w *Writer) setMeta(key, value string) error {
	rec := (&byodb.Record{}).AddString("key", key).AddString("value", value)
	_, err := w.tx.Set(tableMeta, *rec, byodb.MODE_UPSERT)
	return err
}

func (r *Reader) config() (Config, bool, error) {
	raw, ok, err := r.getMeta(metaConfig)
	if err != nil || !ok {
		return Config{}, ok, err
	}
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return Config{}, false, fmt.Errorf("decode collection config: %w", err)
	}
	return cfg, true, cfg.validate()
}

// AllocateVectorIDs reserves n consecutive, never-reused vector IDs.
func (w *Writer) AllocateVectorIDs(n int) (int64, error) {
	if n < 0 {
		return 0, fmt.Errorf("%w: negative id count", ErrInvalidRecord)
	}
	raw, ok, err := w.getMeta(metaNextVectorID)
	if err != nil {
		return 0, err
	}
	next := int64(1)
	if ok {
		if next, err = strconv.ParseInt(raw, 10, 64); err != nil || next < 1 {
			return 0, fmt.Errorf("corrupt %s metadata %q", metaNextVectorID, raw)
		}
	}
	if err := w.setMeta(metaNextVectorID, strconv.FormatInt(next+int64(n), 10)); err != nil {
		return 0, err
	}
	return next, nil
}

// TableRows counts rows in each vf_* table by scanning primary keys. It is
// intended for statistics and tests, not hot paths.
func (r *Reader) TableRows() (map[string]int, error) {
	out := map[string]int{}
	for _, def := range tableDefinitions() {
		sc := byodb.Scanner{}
		if err := r.tx.Scan(def.Name, &sc); err != nil {
			return nil, err
		}
		n := 0
		for ; sc.Valid(); sc.Next() {
			n++
		}
		out[def.Name] = n
	}
	return out, nil
}

// scanPrefix iterates rows of table whose leading index columns equal key.
func (r *Reader) scanPrefix(table string, index []string, key *byodb.Record, fn func(byodb.Record) error) error {
	sc := byodb.Scanner{Cmp1: byodb.CMP_GE, Cmp2: byodb.CMP_LE, Key1: *key, Key2: *key, Index: index}
	if err := r.tx.Scan(table, &sc); err != nil {
		return err
	}
	for ; sc.Valid(); sc.Next() {
		var rec byodb.Record
		if err := sc.Deref(&rec); err != nil {
			return err
		}
		if err := fn(rec); err != nil {
			return err
		}
	}
	return nil
}

// scanAll iterates every row of table in primary-key order.
func (r *Reader) scanAll(table string, fn func(byodb.Record) error) error {
	sc := byodb.Scanner{}
	if err := r.tx.Scan(table, &sc); err != nil {
		return err
	}
	for ; sc.Valid(); sc.Next() {
		var rec byodb.Record
		if err := sc.Deref(&rec); err != nil {
			return err
		}
		if err := fn(rec); err != nil {
			return err
		}
	}
	return nil
}

// deleteAll removes every row of table (and its secondary-index keys) in
// bounded batches within the current transaction.
func (w *Writer) deleteAll(table string) (int, error) {
	total := 0
	for {
		result, err := w.tx.DeleteRange(table, byodb.Scanner{}, byodb.MaxDeleteRangeRows)
		if err != nil {
			return total, err
		}
		total += result.Deleted
		if !result.More {
			return total, nil
		}
	}
}

func str(rec byodb.Record, col string) string { return string(rec.Get(col).Str) }
func i64(rec byodb.Record, col string) int64  { return rec.Get(col).I64 }
