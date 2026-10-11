// Package engine is the VectorForge search engine: it owns the byodb-backed
// store, the in-memory exact and IVF indexes, ingestion, and search.
//
// Consistency model
//
//   - The database is the source of truth. Every mutation (ingesting a batch
//     of documents, uploading, deleting, rebuilding IVF) is one byodb
//     transaction; in-memory indexes are updated only after it commits.
//   - Mutations are serialized by writeMu. Searches take mu for reading and
//     never wait on the embedding service or disk while holding it.
//   - At startup vectors are streamed from the database into the exact index,
//     and the IVF index is rebuilt from persisted centroids and postings
//     without re-running k-means.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	"byodb/vectorforge/embed"
	"byodb/vectorforge/index"
	"byodb/vectorforge/ingest"
	"byodb/vectorforge/store"
)

// UploadSource is the virtual source that holds documents uploaded through
// the API rather than discovered on disk.
const UploadSource = "uploads"

var (
	ErrBusy      = errors.New("another mutation is in progress")
	ErrNotFound  = store.ErrNotFound
	ErrReadOnly  = store.ErrReadOnly
	ErrInvalid   = errors.New("invalid request")
	ErrUntrained = errors.New("IVF index is not trained")
	ErrEmbedder  = errors.New("embedding service error")
)

// IVFConfig controls IVF training and default search parameters.
type IVFConfig struct {
	NList         int   // 0: round(sqrt(N)) at training time
	NProbe        int   // default lists probed per query; 0 means 8
	Seed          int64 // k-means seed; 0 means 42
	MaxIterations int   // 0 means the k-means default (25)
	// AutoTrainAt trains the index automatically after an ingest once the
	// collection has at least this many vectors and no index exists. 0 disables.
	AutoTrainAt int
}

// Config configures an Engine.
type Config struct {
	Path        string // explicit database file; required
	ReadOnly    bool
	Embedder    embed.Embedder
	Sources     []ingest.Source // the only directories ingestion may read
	Chunking    ingest.Options  // zero value: ingest.DefaultOptions
	BatchChunks int             // chunks per embedding call and commit; 0 means 128
	IVF         IVFConfig
	Logger      *slog.Logger
	Now         func() time.Time
}

// docRef is the in-memory catalog entry used for filters and hit metadata.
type docRef struct {
	ID, Source, Path, Kind string
	Chunks                 int
}

// StartupReport records how the in-memory state was reconstructed.
type StartupReport struct {
	LoadMillis      int64 `json:"load_millis"`
	VectorMillis    int64 `json:"vector_load_millis"`
	IVFMillis       int64 `json:"ivf_load_millis"`
	Documents       int   `json:"documents"`
	Vectors         int   `json:"vectors"`
	IVFLoaded       bool  `json:"ivf_loaded"`
	IVFRetrained    bool  `json:"ivf_retrained"` // always false: the persisted index is reused
	MissingPostings int   `json:"missing_postings_repaired"`
	StalePostings   int   `json:"stale_postings_ignored"`
	OrphanVectors   int   `json:"orphan_vectors"`
	InterruptedRuns int   `json:"interrupted_runs_marked"`
}

// Engine is safe for concurrent use.
type Engine struct {
	cfg     Config
	st      *store.Store
	emb     embed.Embedder
	info    embed.Info
	sources map[string]ingest.Source
	log     *slog.Logger
	now     func() time.Time

	writeMu sync.Mutex // serializes mutations; acquired with TryLock

	mu         sync.RWMutex // guards the fields below
	flat       *index.Flat
	ivf        *index.IVF
	ivfMeta    store.IVFMeta
	sinceTrain int
	refs       map[int64]*docRef
	docs       map[string]*docRef

	jobs    *jobRegistry
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	startup StartupReport
}

func (c *Config) defaults() error {
	if c.Path == "" {
		return fmt.Errorf("%w: a database path is required", ErrInvalid)
	}
	if c.Embedder == nil {
		return fmt.Errorf("%w: an embedder is required", ErrInvalid)
	}
	if c.Chunking == (ingest.Options{}) {
		c.Chunking = ingest.DefaultOptions
	}
	if c.BatchChunks <= 0 {
		c.BatchChunks = 128
	}
	if c.IVF.NProbe <= 0 {
		c.IVF.NProbe = 8
	}
	if c.IVF.Seed == 0 {
		c.IVF.Seed = 42
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return nil
}

// Open opens the database, checks that it matches the embedder's model and
// dimension, and loads the indexes.
func Open(ctx context.Context, cfg Config) (*Engine, error) {
	if err := cfg.defaults(); err != nil {
		return nil, err
	}
	sources := map[string]ingest.Source{}
	for _, src := range cfg.Sources {
		if err := src.Validate(); err != nil {
			return nil, err
		}
		if src.Name == UploadSource {
			return nil, fmt.Errorf("%w: source name %q is reserved", ErrInvalid, UploadSource)
		}
		if _, dup := sources[src.Name]; dup {
			return nil, fmt.Errorf("%w: duplicate source %q", ErrInvalid, src.Name)
		}
		sources[src.Name] = src
	}
	info, err := cfg.Embedder.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("embedding service: %w", err)
	}
	st, err := store.Open(cfg.Path, store.Options{ReadOnly: cfg.ReadOnly, Now: cfg.Now})
	if err != nil {
		return nil, err
	}
	want := store.Config{Dimension: info.Dimension, Metric: store.MetricCosine, ModelID: info.ModelID}
	if _, ok := st.Config(); ok || !cfg.ReadOnly {
		err = st.Configure(want)
	} else {
		err = fmt.Errorf("%w: read-only database has no collection", store.ErrNotConfigured)
	}
	if err != nil {
		st.Close()
		return nil, err
	}
	e := &Engine{
		cfg: cfg, st: st, emb: cfg.Embedder, info: info, sources: sources, log: cfg.Logger, now: cfg.Now,
		jobs: newJobRegistry(),
	}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	if err := e.load(); err != nil {
		st.Close()
		return nil, err
	}
	return e, nil
}

// load rebuilds in-memory state from the database.
func (e *Engine) load() error {
	started := time.Now()
	dim := e.info.Dimension
	flat := index.NewFlat(dim)
	refs := map[int64]*docRef{}
	docs := map[string]*docRef{}
	rep := StartupReport{}
	var state store.IVFState
	var trained bool
	err := e.st.View(func(r *store.Reader) error {
		if err := r.Documents(func(d store.Document) error {
			docs[d.ID] = &docRef{ID: d.ID, Source: d.Source, Path: d.Path, Kind: d.Kind, Chunks: d.ChunkCount}
			return nil
		}); err != nil {
			return err
		}
		if err := r.Vectors(func(v store.StoredVector) error {
			ref, ok := docs[v.DocID]
			if !ok {
				rep.OrphanVectors++
				return nil
			}
			refs[v.ID] = ref
			return flat.Add(v.ID, v.Vector) // Add copies the reused slice
		}); err != nil {
			return err
		}
		rep.VectorMillis = time.Since(started).Milliseconds()
		var err error
		state, trained, err = r.IVF()
		return err
	})
	if err != nil {
		return fmt.Errorf("load collection: %w", err)
	}
	ivfStarted := time.Now()
	var ivf *index.IVF
	var missing []int64
	if trained {
		if state.Meta.Dimension != dim || state.Meta.ModelID != e.info.ModelID {
			return fmt.Errorf("%w: persisted IVF index was trained for %s (%d-d)", store.ErrConfigMismatch, state.Meta.ModelID, state.Meta.Dimension)
		}
		if ivf, err = index.NewIVF(dim, state.Centroids); err != nil {
			return fmt.Errorf("persisted IVF centroids: %w", err)
		}
		ids := flat.IDs()
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			v, _ := flat.Vector(id)
			list, ok := state.Postings[id]
			if !ok {
				missing = append(missing, id)
				list = ivf.Assign(v)
			}
			if err := ivf.AddToList(id, v, list); err != nil {
				return err
			}
		}
		rep.StalePostings = len(state.Orphans)
		for id := range state.Postings {
			if _, ok := refs[id]; !ok {
				rep.StalePostings++
			}
		}
		rep.IVFLoaded = true
		rep.MissingPostings = len(missing)
	}
	rep.IVFMillis = time.Since(ivfStarted).Milliseconds()
	if !e.cfg.ReadOnly {
		if err := e.repairOnStartup(ivf, missing, &rep); err != nil {
			return err
		}
	}
	rep.Documents, rep.Vectors = len(docs), flat.Len()
	rep.LoadMillis = time.Since(started).Milliseconds()
	e.flat, e.ivf, e.ivfMeta, e.refs, e.docs, e.startup = flat, ivf, state.Meta, refs, docs, rep
	if trained {
		e.sinceTrain = max(0, flat.Len()-state.Meta.TrainedVectors)
	}
	e.log.Info("collection loaded", "documents", rep.Documents, "vectors", rep.Vectors, "ivf", rep.IVFLoaded,
		"load_ms", rep.LoadMillis)
	return nil
}

// repairOnStartup persists postings for vectors that lacked one and marks
// ingestion runs left "running" by a crashed process as interrupted.
func (e *Engine) repairOnStartup(ivf *index.IVF, missing []int64, rep *StartupReport) error {
	var runs []store.Run
	if err := e.st.View(func(r *store.Reader) error {
		var err error
		runs, err = r.Runs()
		return err
	}); err != nil {
		return err
	}
	if len(missing) == 0 && !hasRunning(runs) {
		return nil
	}
	return e.st.Update(func(w *store.Writer) error {
		for _, id := range missing {
			list, _ := ivf.ListOf(id)
			if err := w.PutPosting(list, id); err != nil {
				return err
			}
		}
		for _, run := range runs {
			if run.Status == statusRunning {
				if err := w.PutRun(run.ID, statusInterrupted, run.StartedAt, e.now().Unix(), run.Summary); err != nil {
					return err
				}
				rep.InterruptedRuns++
			}
		}
		return nil
	})
}

func hasRunning(runs []store.Run) bool {
	for _, r := range runs {
		if r.Status == statusRunning {
			return true
		}
	}
	return false
}

// Close cancels running jobs, waits for them, and closes the database.
func (e *Engine) Close() error {
	e.cancel()
	e.wg.Wait()
	return e.st.Close()
}

// Info returns the embedding space of the collection.
func (e *Engine) Info() embed.Info { return e.info }

// PingEmbedder checks that the embedding service is reachable now, if the
// embedder supports a liveness check.
func (e *Engine) PingEmbedder(ctx context.Context) error {
	if p, ok := e.emb.(interface{ Ping(context.Context) error }); ok {
		return p.Ping(ctx)
	}
	return nil
}

// ReadOnly reports whether mutations are disabled.
func (e *Engine) ReadOnly() bool { return e.cfg.ReadOnly }

// Sources lists the configured ingestion sources.
func (e *Engine) Sources() []ingest.Source {
	out := make([]ingest.Source, 0, len(e.sources))
	for _, s := range e.sources {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// acquire takes the mutation lock without waiting, so a long ingest makes
// other mutations fail fast with ErrBusy instead of hanging HTTP requests.
func (e *Engine) acquire() error {
	if e.cfg.ReadOnly {
		return ErrReadOnly
	}
	if !e.writeMu.TryLock() {
		return ErrBusy
	}
	return nil
}
