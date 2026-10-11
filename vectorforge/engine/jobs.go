package engine

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"
)

const (
	statusRunning     = "running"
	statusSucceeded   = "succeeded"
	statusFailed      = "failed"
	statusCancelled   = "cancelled"
	statusInterrupted = "interrupted"
)

// Progress is updated while a job runs.
type Progress struct {
	FilesTotal     int `json:"files_total"`
	FilesDone      int `json:"files_done"`
	ChunksEmbedded int `json:"chunks_embedded"`
	DocsCommitted  int `json:"documents_committed"`
}

// Job is a background ingest or rebuild.
type Job struct {
	ID         string     `json:"job_id"`
	Kind       string     `json:"kind"`
	Source     string     `json:"source,omitempty"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Progress   Progress   `json:"progress"`
	Result     any        `json:"result,omitempty"`
	Error      string     `json:"error,omitempty"`
}

type jobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

const maxJobs = 100

func newJobRegistry() *jobRegistry { return &jobRegistry{jobs: map[string]*Job{}} }

// newJobID returns a sortable, unique ID such as 20261017T153000Z-9f2c1a.
func newJobID(now time.Time) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

func (r *jobRegistry) add(j *Job) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.jobs) >= maxJobs {
		var oldest *Job
		for _, x := range r.jobs {
			if x.Status != statusRunning && (oldest == nil || x.CreatedAt.Before(oldest.CreatedAt)) {
				oldest = x
			}
		}
		if oldest != nil {
			delete(r.jobs, oldest.ID)
		}
	}
	r.jobs[j.ID] = j
}

func (r *jobRegistry) update(id string, fn func(*Job)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok {
		fn(j)
	}
}

func (r *jobRegistry) get(id string) (Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *j, true
}

func (r *jobRegistry) list() []Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Job, 0, len(r.jobs))
	for _, j := range r.jobs {
		out = append(out, *j)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// Job returns a job by ID.
func (e *Engine) Job(id string) (Job, bool) { return e.jobs.get(id) }

// Jobs lists recent jobs, newest first.
func (e *Engine) Jobs() []Job { return e.jobs.list() }

// startJob runs fn in the background while holding the mutation lock, which
// the caller must already have acquired.
func (e *Engine) startJob(kind, source string, fn func(ctxJob *jobContext) (any, error)) Job {
	now := e.now()
	job := &Job{ID: newJobID(now), Kind: kind, Source: source, Status: statusRunning, CreatedAt: now}
	e.jobs.add(job)
	snapshot := *job
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer e.writeMu.Unlock()
		jc := &jobContext{engine: e, id: job.ID}
		result, err := fn(jc)
		finished := e.now()
		e.jobs.update(job.ID, func(j *Job) {
			j.FinishedAt, j.Result = &finished, result
			switch {
			case err == nil:
				j.Status = statusSucceeded
			case e.ctx.Err() != nil:
				j.Status, j.Error = statusCancelled, err.Error()
			default:
				j.Status, j.Error = statusFailed, err.Error()
			}
		})
		if err != nil {
			e.log.Warn("job failed", "job", job.ID, "kind", kind, "err", err)
		}
	}()
	return snapshot
}

type jobContext struct {
	engine *Engine
	id     string
}

func (jc *jobContext) progress(fn func(*Progress)) {
	if jc == nil {
		return
	}
	jc.engine.jobs.update(jc.id, func(j *Job) { fn(&j.Progress) })
}
