// Package service exposes the market repository through a bounded HTTP and
// background-work interface. It intentionally uses the Go standard library so
// the concurrency and failure semantics remain visible to students.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"byodb/market"
)

type JobHandler func(context.Context, json.RawMessage) (any, error)

type JobError struct {
	Code string
	Err  error
}

func (e *JobError) Error() string { return e.Code }
func (e *JobError) Unwrap() error { return e.Err }

type JobMetrics struct {
	Submitted uint64 `json:"submitted"`
	Succeeded uint64 `json:"succeeded"`
	Failed    uint64 `json:"failed"`
	Cancelled uint64 `json:"cancelled"`
	Active    int64  `json:"active"`
}

type JobManager struct {
	repository *market.Repository
	logger     *slog.Logger
	workers    int
	wake       chan struct{}

	mu       sync.Mutex
	handlers map[string]JobHandler
	cancels  map[string]context.CancelFunc
	cancel   context.CancelFunc
	done     chan struct{}
	started  bool

	submitted atomic.Uint64
	succeeded atomic.Uint64
	failed    atomic.Uint64
	cancelled atomic.Uint64
	active    atomic.Int64
}

func NewJobManager(repository *market.Repository, logger *slog.Logger, workers int) (*JobManager, error) {
	if repository == nil {
		return nil, errors.New("job manager requires a repository")
	}
	if workers < 1 || workers > 4 {
		return nil, errors.New("job workers must be between 1 and 4")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &JobManager{repository: repository, logger: logger, workers: workers, wake: make(chan struct{}, 1), handlers: map[string]JobHandler{}, cancels: map[string]context.CancelFunc{}}, nil
}

func (m *JobManager) Register(kind string, handler JobHandler) error {
	if handler == nil {
		return errors.New("job handler is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("cannot register a job handler after start")
	}
	if _, exists := m.handlers[kind]; exists {
		return fmt.Errorf("job handler %q already registered", kind)
	}
	m.handlers[kind] = handler
	return nil
}

func (m *JobManager) Capabilities() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.handlers))
	for kind := range m.handlers {
		out = append(out, kind)
	}
	// A small insertion sort avoids adding a package for one tiny control list.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func (m *JobManager) Start(parent context.Context) (int, error) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return 0, errors.New("job manager already started")
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancel, m.done, m.started = cancel, make(chan struct{}), true
	m.mu.Unlock()
	recovered, err := m.repository.RecoverInterruptedJobs()
	if err != nil {
		cancel()
		m.mu.Lock()
		close(m.done)
		m.cancel, m.done, m.started = nil, nil, false
		m.mu.Unlock()
		return 0, err
	}
	var workers sync.WaitGroup
	workers.Add(m.workers)
	for i := 0; i < m.workers; i++ {
		go func(worker int) {
			defer workers.Done()
			m.worker(ctx, worker)
		}(i + 1)
	}
	go func() {
		workers.Wait()
		close(m.done)
	}()
	if recovered > 0 {
		m.logger.Warn("interrupted jobs recovered", "count", recovered)
	}
	m.notify()
	return recovered, nil
}

func (m *JobManager) Submit(kind string, payload []byte, idempotencyKey string) (market.ServiceJob, bool, error) {
	m.mu.Lock()
	_, supported := m.handlers[kind]
	started := m.started
	m.mu.Unlock()
	if !supported {
		return market.ServiceJob{}, false, &JobError{Code: "unsupported_job_kind"}
	}
	if !started {
		return market.ServiceJob{}, false, errors.New("job manager is not running")
	}
	job, created, err := m.repository.CreateServiceJob(kind, payload, idempotencyKey)
	if err == nil && created {
		m.submitted.Add(1)
		m.notify()
	}
	return job, created, err
}

func (m *JobManager) Cancel(id string) (market.ServiceJob, error) {
	job, err := m.repository.CancelServiceJob(id)
	if err != nil {
		return job, err
	}
	m.mu.Lock()
	cancel := m.cancels[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return job, nil
}

func (m *JobManager) Metrics() JobMetrics {
	return JobMetrics{Submitted: m.submitted.Load(), Succeeded: m.succeeded.Load(), Failed: m.failed.Load(), Cancelled: m.cancelled.Load(), Active: m.active.Load()}
}

func (m *JobManager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return nil
	}
	cancel, done := m.cancel, m.done
	m.mu.Unlock()
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *JobManager) notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *JobManager) worker(ctx context.Context, worker int) {
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	for {
		job, ok, err := m.repository.ClaimServiceJob()
		if err != nil {
			m.logger.Error("job claim failed", "worker", worker, "error_code", "storage_error")
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if ok {
			m.execute(ctx, worker, job)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case <-poll.C:
		}
	}
}

func (m *JobManager) execute(parent context.Context, worker int, job market.ServiceJob) {
	ctx, cancel := context.WithCancel(parent)
	m.mu.Lock()
	m.cancels[job.JobID] = cancel
	handler := m.handlers[job.Kind]
	m.mu.Unlock()
	m.active.Add(1)
	started := time.Now()
	status, errorCode := market.JobSucceeded, ""
	var result any
	var err error
	if handler == nil {
		status, errorCode = market.JobFailed, "unsupported_job_kind"
	} else {
		result, err = handler(ctx, json.RawMessage(job.RequestJSON))
		if err != nil {
			status, errorCode = classifyJobError(err)
		}
	}
	cancel()
	m.mu.Lock()
	delete(m.cancels, job.JobID)
	m.mu.Unlock()
	m.active.Add(-1)
	var resultJSON []byte
	if status == market.JobSucceeded {
		resultJSON, err = json.Marshal(result)
		if err != nil {
			status, errorCode = market.JobFailed, "result_encoding_failed"
		}
	}
	finished, finishErr := m.repository.FinishServiceJob(job.JobID, status, resultJSON, errorCode)
	if finishErr != nil && status == market.JobSucceeded {
		finished, finishErr = m.repository.FinishServiceJob(job.JobID, market.JobFailed, nil, "result_encoding_failed")
	}
	if finishErr != nil {
		m.logger.Error("job finalization failed", "job_id", job.JobID, "kind", job.Kind, "worker", worker, "error_code", "storage_error")
		return
	}
	switch finished.Status {
	case market.JobSucceeded:
		m.succeeded.Add(1)
	case market.JobCancelled:
		m.cancelled.Add(1)
	default:
		m.failed.Add(1)
	}
	m.logger.Info("job finished", "job_id", job.JobID, "kind", job.Kind, "status", finished.Status, "error_code", finished.ErrorCode, "worker", worker, "duration_ms", time.Since(started).Milliseconds())
}

func classifyJobError(err error) (string, string) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return market.JobCancelled, "cancelled"
	}
	var jobErr *JobError
	if errors.As(err, &jobErr) && jobErr.Code != "" {
		return market.JobFailed, jobErr.Code
	}
	if errors.Is(err, market.ErrInvalidMarketData) {
		return market.JobFailed, "invalid_request"
	}
	if errors.Is(err, market.ErrInsufficientHistory) {
		return market.JobFailed, "insufficient_history"
	}
	return market.JobFailed, "internal_error"
}
