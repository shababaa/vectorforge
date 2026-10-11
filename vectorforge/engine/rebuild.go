package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"byodb"
	"byodb/vectorforge/index"
	"byodb/vectorforge/ingest"
	"byodb/vectorforge/store"
)

// RebuildOptions overrides the configured IVF training parameters.
type RebuildOptions struct {
	NList         int   `json:"nlist,omitempty"`
	Seed          int64 `json:"seed,omitempty"`
	MaxIterations int   `json:"max_iterations,omitempty"`
}

const maxNList = 4096

// StartRebuild trains the IVF index in the background.
func (e *Engine) StartRebuild(opts RebuildOptions) (Job, error) {
	if err := e.validateRebuild(opts); err != nil {
		return Job{}, err
	}
	if err := e.acquire(); err != nil {
		return Job{}, err
	}
	if _, err := e.trainableNList(opts); err != nil {
		e.writeMu.Unlock()
		return Job{}, err
	}
	return e.startJob("rebuild", "", func(*jobContext) (any, error) {
		return e.rebuildLocked(e.ctx, opts)
	}), nil
}

// RebuildIVF trains the IVF index synchronously.
func (e *Engine) RebuildIVF(ctx context.Context, opts RebuildOptions) (store.IVFMeta, error) {
	if err := e.validateRebuild(opts); err != nil {
		return store.IVFMeta{}, err
	}
	if err := e.acquire(); err != nil {
		return store.IVFMeta{}, err
	}
	defer e.writeMu.Unlock()
	return e.rebuildLocked(ctx, opts)
}

func (e *Engine) validateRebuild(opts RebuildOptions) error {
	if opts.NList < 0 || opts.NList > maxNList {
		return fmt.Errorf("%w: nlist must be between 1 and %d", ErrInvalid, maxNList)
	}
	if opts.MaxIterations < 0 || opts.MaxIterations > 200 {
		return fmt.Errorf("%w: max_iterations must be between 1 and 200", ErrInvalid)
	}
	return nil
}

// trainableNList resolves the list count for a rebuild and checks that the
// collection has enough vectors to train it. The caller holds writeMu.
func (e *Engine) trainableNList(opts RebuildOptions) (int, error) {
	n := e.vectorCount()
	nlist := opts.NList
	if nlist == 0 {
		nlist = e.cfg.IVF.NList
	}
	if nlist == 0 {
		nlist = index.DefaultNList(n)
	}
	if n == 0 || n < nlist {
		return 0, fmt.Errorf("%w: %d vectors cannot train %d lists", ErrInvalid, n, nlist)
	}
	return nlist, nil
}

// rebuildLocked retrains centroids from every stored vector and replaces the
// persisted index in one transaction. Searches keep using the previous index
// until the new one has committed. The caller holds writeMu, so the vector
// set cannot change between the snapshot and the commit.
func (e *Engine) rebuildLocked(ctx context.Context, opts RebuildOptions) (store.IVFMeta, error) {
	dim := e.info.Dimension
	e.mu.RLock()
	ids := e.flat.IDs()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	data := make([]float32, 0, len(ids)*dim)
	for _, id := range ids {
		v, _ := e.flat.Vector(id)
		data = append(data, v...)
	}
	prev := e.ivfMeta
	e.mu.RUnlock()

	nlist, err := e.trainableNList(opts)
	if err != nil {
		return store.IVFMeta{}, err
	}
	seed := opts.Seed
	if seed == 0 {
		seed = e.cfg.IVF.Seed
	}
	iterations := opts.MaxIterations
	if iterations == 0 {
		iterations = e.cfg.IVF.MaxIterations
	}
	if err := ctx.Err(); err != nil {
		return store.IVFMeta{}, err
	}
	started := time.Now()
	ivf, km, err := index.TrainIVF(ids, data, dim, index.KMeansConfig{K: nlist, Seed: seed, MaxIterations: iterations})
	if err != nil {
		return store.IVFMeta{}, err
	}
	meta := store.IVFMeta{
		NList: nlist, Dimension: dim, ModelID: e.info.ModelID, Seed: seed, Generation: prev.Generation + 1,
		TrainedAt: e.now().Unix(), TrainedVectors: len(ids), SampleSize: km.SampleSize, Iterations: km.Iterations,
		Converged: km.Converged, Objective: km.Objective, History: km.History, EmptyReseeds: km.EmptyReseeds,
		TrainMillis: time.Since(started).Milliseconds(),
	}
	assignments := make(map[int64]int, len(ids))
	for _, id := range ids {
		list, _ := ivf.ListOf(id)
		assignments[id] = list
	}
	if err := ctx.Err(); err != nil {
		return store.IVFMeta{}, err
	}
	if err := e.st.Update(func(w *store.Writer) error {
		return w.ReplaceIVF(meta, ivf.Centroids(), assignments)
	}); err != nil {
		return store.IVFMeta{}, err
	}
	e.mu.Lock()
	e.ivf, e.ivfMeta, e.sinceTrain = ivf, meta, 0
	e.mu.Unlock()
	e.log.Info("IVF rebuilt", "nlist", nlist, "vectors", len(ids), "iterations", km.Iterations, "ms", meta.TrainMillis)
	return meta, nil
}

// IVFStats describes the approximate index.
type IVFStats struct {
	Trained           bool           `json:"trained"`
	DefaultNProbe     int            `json:"default_nprobe"`
	Meta              *store.IVFMeta `json:"meta,omitempty"`
	VectorsSinceTrain int            `json:"vectors_since_train"`
	Balance           *index.Balance `json:"balance,omitempty"`
	ListSizes         []int          `json:"list_sizes,omitempty"`
}

// SourceStats summarizes one source. Configured is false for documents left
// behind by a source that was removed from the configuration; Ingestible is
// true only for configured directory sources.
type SourceStats struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Configured  bool   `json:"configured"`
	Ingestible  bool   `json:"ingestible"`
	Documents   int    `json:"documents"`
	Chunks      int    `json:"chunks"`
}

// Stats is the collection dashboard.
type Stats struct {
	ModelID   string         `json:"model_id"`
	Dimension int            `json:"dimension"`
	Metric    string         `json:"metric"`
	ReadOnly  bool           `json:"read_only"`
	Documents int            `json:"documents"`
	Vectors   int            `json:"vectors"`
	Kinds     map[string]int `json:"chunks_by_kind"`
	Sources   []SourceStats  `json:"sources"`
	IVF       IVFStats       `json:"ivf"`
	Memory    struct {
		FlatBytes int64 `json:"exact_index_bytes"`
		IVFBytes  int64 `json:"ivf_index_bytes"`
	} `json:"memory"`
	Storage        byodb.DBStats  `json:"storage"`
	Rows           map[string]int `json:"table_rows"`
	ChunkerVersion string         `json:"chunker_version"`
	Startup        StartupReport  `json:"startup"`
	Busy           bool           `json:"mutation_in_progress"`
}

// Stats reports collection, index and storage statistics.
func (e *Engine) Stats() (Stats, error) {
	s := Stats{ModelID: e.info.ModelID, Dimension: e.info.Dimension, Metric: store.MetricCosine,
		ReadOnly: e.cfg.ReadOnly, Kinds: map[string]int{}, ChunkerVersion: ingest.ChunkerVersion, Startup: e.startup}
	bySource := map[string]*SourceStats{}
	for name, src := range e.sources {
		bySource[name] = &SourceStats{Name: name, Description: src.Description, Configured: true, Ingestible: true}
	}
	e.mu.RLock()
	s.Documents, s.Vectors = len(e.docs), e.flat.Len()
	for _, d := range e.docs {
		src := bySource[d.Source]
		if src == nil {
			src = &SourceStats{Name: d.Source, Configured: d.Source == UploadSource}
			if d.Source == UploadSource {
				src.Description = "Documents added through the API"
			}
			bySource[d.Source] = src
		}
		src.Documents++
		src.Chunks += d.Chunks
		s.Kinds[d.Kind] += d.Chunks
	}
	s.Memory.FlatBytes = e.flat.MemoryBytes()
	s.IVF.DefaultNProbe = e.cfg.IVF.NProbe
	if e.ivf != nil {
		meta, balance := e.ivfMeta, e.ivf.Balance()
		s.IVF.Trained, s.IVF.Meta, s.IVF.Balance = true, &meta, &balance
		s.IVF.ListSizes, s.IVF.VectorsSinceTrain = e.ivf.ListSizes(), e.sinceTrain
		s.Memory.IVFBytes = e.ivf.MemoryBytes()
	}
	e.mu.RUnlock()
	for _, src := range bySource {
		s.Sources = append(s.Sources, *src)
	}
	sort.Slice(s.Sources, func(i, j int) bool { return s.Sources[i].Name < s.Sources[j].Name })
	var err error
	if s.Storage, err = e.st.Stats(); err != nil {
		return Stats{}, err
	}
	err = e.st.View(func(r *store.Reader) error {
		s.Rows, err = r.TableRows()
		return err
	})
	if e.writeMu.TryLock() {
		e.writeMu.Unlock()
	} else {
		s.Busy = true
	}
	return s, err
}

// Documents lists stored documents, optionally filtered, in doc_id order.
func (e *Engine) Documents(source, kind, pathPrefix string, offset, limit int) ([]store.Document, int, error) {
	var out []store.Document
	total := 0
	err := e.st.View(func(r *store.Reader) error {
		return r.Documents(func(d store.Document) error {
			if (source != "" && d.Source != source) || (kind != "" && d.Kind != kind) ||
				(pathPrefix != "" && !hasPrefix(d.Path, pathPrefix)) {
				return nil
			}
			if total >= offset && len(out) < limit {
				out = append(out, d)
			}
			total++
			return nil
		})
	})
	return out, total, err
}

func hasPrefix(s, prefix string) bool { return len(s) >= len(prefix) && s[:len(prefix)] == prefix }

// Document returns a document and its chunks.
func (e *Engine) Document(id string) (store.Document, []store.Chunk, error) {
	var doc store.Document
	var chunks []store.Chunk
	err := e.st.View(func(r *store.Reader) error {
		var err error
		if doc, err = r.Document(id); err != nil {
			return err
		}
		chunks, err = r.Chunks(id)
		return err
	})
	return doc, chunks, err
}

// Runs lists persisted ingestion runs, newest first.
func (e *Engine) Runs(limit int) ([]store.Run, error) {
	var runs []store.Run
	err := e.st.View(func(r *store.Reader) error {
		var err error
		runs, err = r.Runs()
		return err
	})
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID > runs[j].ID })
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, err
}
