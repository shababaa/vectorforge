package market

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"byodb"
)

const maxServiceJSONBytes = 1536

var ErrJobConflict = errors.New("idempotency key was already used for a different job request")

func canonicalObject(raw []byte) (string, error) {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return "", fmt.Errorf("%w: service payload must be a JSON object", ErrInvalidMarketData)
	}
	b, err := json.Marshal(object)
	if err != nil || len(b) > maxServiceJSONBytes {
		return "", fmt.Errorf("%w: service payload must be at most %d bytes", ErrInvalidMarketData, maxServiceJSONBytes)
	}
	return string(b), nil
}

func validJobKind(kind string) bool {
	if len(kind) < 1 || len(kind) > 48 {
		return false
	}
	for _, c := range kind {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func serviceJobRecord(v ServiceJob) byodb.Record {
	return *(&byodb.Record{}).AddString("job_id", v.JobID).AddString("kind", v.Kind).AddString("status", v.Status).
		AddString("request_json", v.RequestJSON).AddString("result_json", v.ResultJSON).AddString("error_code", v.ErrorCode).
		AddString("idempotency_hash", v.IdempotencyHash).AddString("request_digest", v.RequestDigest).
		AddInt64("created_at", v.CreatedAt).AddInt64("started_at", v.StartedAt).AddInt64("finished_at", v.FinishedAt).
		AddInt64("cancel_requested", boolInt(v.CancelRequested))
}

func serviceJobFromRecord(r byodb.Record) ServiceJob {
	return ServiceJob{
		JobID: r.Get("job_id").String(), Kind: r.Get("kind").String(), Status: r.Get("status").String(),
		RequestJSON: r.Get("request_json").String(), ResultJSON: r.Get("result_json").String(), ErrorCode: r.Get("error_code").String(),
		IdempotencyHash: r.Get("idempotency_hash").String(), RequestDigest: r.Get("request_digest").String(),
		CreatedAt: r.Get("created_at").I64, StartedAt: r.Get("started_at").I64, FinishedAt: r.Get("finished_at").I64,
		CancelRequested: r.Get("cancel_requested").I64 != 0,
	}
}

// CreateServiceJob stores a queued job before a worker sees it. A caller may
// provide an idempotency key; the stored hash avoids retaining the caller's
// token while producing the same job ID for safe HTTP retries.
func (r *Repository) CreateServiceJob(kind string, request []byte, idempotencyKey string) (ServiceJob, bool, error) {
	kind = strings.TrimSpace(kind)
	if !validJobKind(kind) {
		return ServiceJob{}, false, fmt.Errorf("%w: invalid service job kind", ErrInvalidMarketData)
	}
	canonical, err := canonicalObject(request)
	if err != nil {
		return ServiceJob{}, false, err
	}
	digestBytes := sha256.Sum256([]byte(kind + "\x00" + canonical))
	digest := hex.EncodeToString(digestBytes[:])
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	jobID, idempotencyHash := "", ""
	if idempotencyKey != "" {
		if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 || strings.ContainsAny(idempotencyKey, "\r\n\x00") {
			return ServiceJob{}, false, fmt.Errorf("%w: idempotency key must contain 8-128 safe characters", ErrInvalidMarketData)
		}
		h := sha256.Sum256([]byte(idempotencyKey))
		idempotencyHash = hex.EncodeToString(h[:])
		jobID = "job_" + idempotencyHash[:32]
	} else if jobID, err = newID("job"); err != nil {
		return ServiceJob{}, false, err
	}
	job := ServiceJob{JobID: jobID, Kind: kind, Status: JobQueued, RequestJSON: canonical, IdempotencyHash: idempotencyHash, RequestDigest: digest, CreatedAt: r.now().UTC().Unix()}
	created := false
	err = r.writeTransaction(func(tx *byodb.DBTX) error {
		created = false
		key := (&byodb.Record{}).AddString("job_id", jobID)
		exists, err := tx.Get(tableJobs, key)
		if err != nil {
			return err
		}
		if exists {
			prior := serviceJobFromRecord(*key)
			if prior.RequestDigest != digest || prior.Kind != kind || idempotencyKey == "" {
				return ErrJobConflict
			}
			job = prior
			return nil
		}
		changed, err := tx.Set(tableJobs, serviceJobRecord(job), byodb.MODE_INSERT_ONLY)
		created = changed
		return err
	})
	return job, created, err
}

func (r *Repository) ServiceJob(id string) (ServiceJob, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 80 {
		return ServiceJob{}, false, fmt.Errorf("%w: invalid job id", ErrInvalidMarketData)
	}
	rec := (&byodb.Record{}).AddString("job_id", id)
	ok, err := r.db.Get(tableJobs, rec)
	if err != nil || !ok {
		return ServiceJob{}, ok, err
	}
	return serviceJobFromRecord(*rec), true, nil
}

// ClaimServiceJob atomically transitions the oldest queued job to running.
func (r *Repository) ClaimServiceJob() (ServiceJob, bool, error) {
	var claimed ServiceJob
	found := false
	err := r.writeTransaction(func(tx *byodb.DBTX) error {
		claimed, found = ServiceJob{}, false
		key := *(&byodb.Record{}).AddString("status", JobQueued)
		sc := &byodb.Scanner{Cmp1: byodb.CMP_GE, Key1: key, Cmp2: byodb.CMP_LE, Key2: key.Clone(), Index: []string{"status", "created_at", "job_id"}}
		if err := tx.Scan(tableJobs, sc); err != nil || !sc.Valid() {
			return err
		}
		var rec byodb.Record
		if err := sc.Deref(&rec); err != nil {
			return err
		}
		claimed = serviceJobFromRecord(rec)
		if claimed.CancelRequested {
			claimed.Status, claimed.FinishedAt = JobCancelled, r.now().UTC().Unix()
		} else {
			claimed.Status, claimed.StartedAt = JobRunning, r.now().UTC().Unix()
			found = true
		}
		_, err := tx.Set(tableJobs, serviceJobRecord(claimed), byodb.MODE_UPDATE_ONLY)
		return err
	})
	return claimed, found, err
}

func terminalJobStatus(status string) bool {
	return status == JobSucceeded || status == JobFailed || status == JobCancelled
}

// FinishServiceJob records only a safe error code; detailed errors belong in
// structured server logs and are never exposed through the API.
func (r *Repository) FinishServiceJob(id, status string, result []byte, errorCode string) (ServiceJob, error) {
	if status != JobSucceeded && status != JobFailed && status != JobCancelled {
		return ServiceJob{}, fmt.Errorf("%w: invalid terminal job status", ErrInvalidMarketData)
	}
	canonical := ""
	var err error
	if len(result) != 0 {
		canonical, err = canonicalObject(result)
		if err != nil {
			return ServiceJob{}, err
		}
	}
	if len(errorCode) > 64 || strings.ContainsAny(errorCode, "\r\n\x00") {
		return ServiceJob{}, fmt.Errorf("%w: invalid job error code", ErrInvalidMarketData)
	}
	var job ServiceJob
	err = r.writeTransaction(func(tx *byodb.DBTX) error {
		key := (&byodb.Record{}).AddString("job_id", strings.TrimSpace(id))
		ok, err := tx.Get(tableJobs, key)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("service job not found")
		}
		job = serviceJobFromRecord(*key)
		if terminalJobStatus(job.Status) {
			return nil
		}
		if job.Status != JobRunning {
			return errors.New("service job is not running")
		}
		if job.CancelRequested {
			status, canonical, errorCode = JobCancelled, "", "cancelled"
		}
		job.Status, job.ResultJSON, job.ErrorCode = status, canonical, errorCode
		job.FinishedAt = r.now().UTC().Unix()
		_, err = tx.Set(tableJobs, serviceJobRecord(job), byodb.MODE_UPDATE_ONLY)
		return err
	})
	return job, err
}

func (r *Repository) CancelServiceJob(id string) (ServiceJob, error) {
	var job ServiceJob
	err := r.writeTransaction(func(tx *byodb.DBTX) error {
		key := (&byodb.Record{}).AddString("job_id", strings.TrimSpace(id))
		ok, err := tx.Get(tableJobs, key)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("service job not found")
		}
		job = serviceJobFromRecord(*key)
		if terminalJobStatus(job.Status) {
			return nil
		}
		job.CancelRequested = true
		if job.Status == JobQueued {
			job.Status, job.ErrorCode, job.FinishedAt = JobCancelled, "cancelled", r.now().UTC().Unix()
		}
		_, err = tx.Set(tableJobs, serviceJobRecord(job), byodb.MODE_UPDATE_ONLY)
		return err
	})
	return job, err
}

// RecoverInterruptedJobs makes crash behavior explicit: in-flight work is not
// silently replayed, while jobs that never started remain queued.
func (r *Repository) RecoverInterruptedJobs() (int, error) {
	recovered := 0
	err := r.writeTransaction(func(tx *byodb.DBTX) error {
		recovered = 0
		key := *(&byodb.Record{}).AddString("status", JobRunning)
		sc := &byodb.Scanner{Cmp1: byodb.CMP_GE, Key1: key, Cmp2: byodb.CMP_LE, Key2: key.Clone(), Index: []string{"status", "created_at", "job_id"}}
		if err := tx.Scan(tableJobs, sc); err != nil {
			return err
		}
		var jobs []ServiceJob
		for sc.Valid() {
			if len(jobs) >= 1000 {
				return errors.New("too many interrupted service jobs")
			}
			var rec byodb.Record
			if err := sc.Deref(&rec); err != nil {
				return err
			}
			jobs = append(jobs, serviceJobFromRecord(rec))
			sc.Next()
		}
		for _, job := range jobs {
			job.Status, job.ErrorCode, job.FinishedAt = JobFailed, "server_restarted", r.now().UTC().Unix()
			if _, err := tx.Set(tableJobs, serviceJobRecord(job), byodb.MODE_UPDATE_ONLY); err != nil {
				return err
			}
			recovered++
		}
		return nil
	})
	return recovered, err
}

func (r *Repository) AppMetadata(key string) (json.RawMessage, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 80 {
		return nil, false, fmt.Errorf("%w: invalid metadata key", ErrInvalidMarketData)
	}
	rec := (&byodb.Record{}).AddString("key", key)
	ok, err := r.db.Get(tableAppMetadata, rec)
	if err != nil || !ok {
		return nil, ok, err
	}
	return json.RawMessage(rec.Get("value_json").String()), true, nil
}

func (r *Repository) PutAppMetadata(key string, value any) error {
	key = strings.TrimSpace(key)
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	canonical, err := canonicalObject(b)
	if err != nil || key == "" || len(key) > 80 {
		return fmt.Errorf("%w: invalid application metadata", ErrInvalidMarketData)
	}
	rec := *(&byodb.Record{}).AddString("key", key).AddString("value_json", canonical).AddInt64("updated_at", r.now().UTC().Unix())
	_, err = r.db.Upsert(tableAppMetadata, rec)
	return err
}
