package market

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestServiceJobLifecycleAndIdempotency(t *testing.T) {
	_, repository := openTestRepository(t)
	request := []byte(`{"symbol":"SYNTH","interval":"1d"}`)
	job, created, err := repository.CreateServiceJob("recompute_features", request, "request-key-0001")
	if err != nil || !created || job.Status != JobQueued || job.RequestDigest == "" {
		t.Fatalf("create=(%+v,%t,%v)", job, created, err)
	}
	repeated, created, err := repository.CreateServiceJob("recompute_features", []byte("{\n\"interval\":\"1d\",\"symbol\":\"SYNTH\"}"), "request-key-0001")
	if err != nil || created || repeated.JobID != job.JobID {
		t.Fatalf("idempotent retry=(%+v,%t,%v)", repeated, created, err)
	}
	if _, _, err := repository.CreateServiceJob("recompute_features", []byte(`{"symbol":"OTHER"}`), "request-key-0001"); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("idempotency conflict=%v", err)
	}
	claimed, ok, err := repository.ClaimServiceJob()
	if err != nil || !ok || claimed.JobID != job.JobID || claimed.Status != JobRunning || claimed.StartedAt == 0 {
		t.Fatalf("claim=(%+v,%t,%v)", claimed, ok, err)
	}
	finished, err := repository.FinishServiceJob(job.JobID, JobSucceeded, []byte(`{"features_computed":12}`), "")
	if err != nil || finished.Status != JobSucceeded || finished.FinishedAt == 0 {
		t.Fatalf("finish=(%+v,%v)", finished, err)
	}
	var result map[string]int
	if err := json.Unmarshal([]byte(finished.ResultJSON), &result); err != nil || result["features_computed"] != 12 {
		t.Fatalf("result=%q err=%v", finished.ResultJSON, err)
	}
	stored, ok, err := repository.ServiceJob(job.JobID)
	if err != nil || !ok || stored.Status != JobSucceeded {
		t.Fatalf("stored=(%+v,%t,%v)", stored, ok, err)
	}
}

func TestServiceJobCancellationAndRecovery(t *testing.T) {
	_, repository := openTestRepository(t)
	queued, _, err := repository.CreateServiceJob("seed_demo", []byte(`{}`), "cancel-key-0001")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := repository.CancelServiceJob(queued.JobID)
	if err != nil || cancelled.Status != JobCancelled || !cancelled.CancelRequested {
		t.Fatalf("cancel=(%+v,%v)", cancelled, err)
	}
	running, _, err := repository.CreateServiceJob("seed_demo", []byte(`{}`), "recover-key-001")
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := repository.ClaimServiceJob()
	if err != nil || !ok || claimed.JobID != running.JobID {
		t.Fatalf("claim=(%+v,%t,%v)", claimed, ok, err)
	}
	count, err := repository.RecoverInterruptedJobs()
	if err != nil || count != 1 {
		t.Fatalf("recover=(%d,%v)", count, err)
	}
	failed, ok, err := repository.ServiceJob(running.JobID)
	if err != nil || !ok || failed.Status != JobFailed || failed.ErrorCode != "server_restarted" {
		t.Fatalf("recovered job=(%+v,%t,%v)", failed, ok, err)
	}
}

func TestApplicationMetadataRoundTrip(t *testing.T) {
	_, repository := openTestRepository(t)
	if err := repository.PutAppMetadata("example", map[string]any{"version": 1, "ready": true}); err != nil {
		t.Fatal(err)
	}
	raw, ok, err := repository.AppMetadata("example")
	if err != nil || !ok || string(raw) != `{"ready":true,"version":1}` {
		t.Fatalf("metadata=(%s,%t,%v)", raw, ok, err)
	}
}
