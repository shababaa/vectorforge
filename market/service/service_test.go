package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"byodb"
	"byodb/market"
)

func testRepository(t *testing.T) *market.Repository {
	t.Helper()
	db, err := byodb.OpenDB(filepath.Join(t.TempDir(), "service.db"))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := market.NewRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return repository
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func waitForJob(t *testing.T, repository *market.Repository, id string) market.ServiceJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, ok, err := repository.ServiceJob(id)
		if err != nil {
			t.Fatal(err)
		}
		if ok && (job.Status == market.JobSucceeded || job.Status == market.JobFailed || job.Status == market.JobCancelled) {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish before deadline")
	return market.ServiceJob{}
}

func TestJobManagerExecutesAndCancels(t *testing.T) {
	repository := testRepository(t)
	manager, err := NewJobManager(repository, testLogger(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Register("echo", func(ctx context.Context, payload json.RawMessage) (any, error) {
		var request struct {
			Value string `json:"value"`
		}
		if err := decodePayload(payload, &request); err != nil {
			return nil, err
		}
		return map[string]string{"value": request.Value}, nil
	}); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	if err := manager.Register("block", func(ctx context.Context, _ json.RawMessage) (any, error) {
		close(blocked)
		<-ctx.Done()
		return nil, ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := manager.Start(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if err := manager.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	job, created, err := manager.Submit("echo", []byte(`{"value":"hello"}`), "echo-key-000001")
	if err != nil || !created {
		t.Fatalf("submit=(%+v,%t,%v)", job, created, err)
	}
	finished := waitForJob(t, repository, job.JobID)
	if finished.Status != market.JobSucceeded || !strings.Contains(finished.ResultJSON, "hello") {
		t.Fatalf("finished=%+v", finished)
	}
	job, _, err = manager.Submit("block", []byte(`{}`), "block-key-00001")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("blocking job did not start")
	}
	if _, err := manager.Cancel(job.JobID); err != nil {
		t.Fatal(err)
	}
	finished = waitForJob(t, repository, job.JobID)
	if finished.Status != market.JobCancelled || finished.ErrorCode != "cancelled" {
		t.Fatalf("cancelled=%+v", finished)
	}
}

func TestSeededHTTPServiceContract(t *testing.T) {
	repository := testRepository(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	manifest, err := repository.SeedServiceDemo(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := repository.SeedServiceDemo(ctx, time.Now().UTC())
	if err != nil || repeated.ExperimentID != manifest.ExperimentID {
		t.Fatalf("repeat seed=(%+v,%v)", repeated, err)
	}
	manager, err := NewJobManager(repository, testLogger(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterBuiltinJobs(manager, JobDependencies{Repository: repository}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Shutdown(stop)
	})
	service, err := NewServer(Config{Repository: repository, Jobs: manager, Logger: testLogger(), APIToken: "test-secret", RequestTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler())
	defer server.Close()

	getJSON := func(path string, target any) *http.Response {
		t.Helper()
		request, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		request.Header.Set("X-Request-ID", "contract-request-01")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(response.Body)
			t.Fatalf("GET %s: %d %s", path, response.StatusCode, body)
		}
		if target != nil {
			if err := json.NewDecoder(response.Body).Decode(target); err != nil {
				t.Fatal(err)
			}
		}
		return response
	}
	var overviewEnvelope struct {
		Data Overview `json:"data"`
	}
	response := getJSON("/api/v1/overview?symbol=SYNTH&interval=1d&limit=80", &overviewEnvelope)
	if response.Header.Get("X-Request-ID") != "contract-request-01" || len(overviewEnvelope.Data.Candles) != 80 || overviewEnvelope.Data.Model == nil || overviewEnvelope.Data.Analysis == nil || len(overviewEnvelope.Data.Forecasts) != 1 {
		t.Fatalf("overview=%+v headers=%v", overviewEnvelope.Data, response.Header)
	}
	var wireEnvelope struct {
		Data struct {
			Forecasts []map[string]json.RawMessage `json:"forecasts"`
		} `json:"data"`
	}
	getJSON("/api/v1/overview?symbol=SYNTH&interval=1d&limit=80", &wireEnvelope)
	forecast := wireEnvelope.Data.Forecasts[0]
	for _, field := range []string{"predicted_close", "baseline_close", "lower_bound", "upper_bound", "confidence_ppm", "target_timestamp"} {
		if _, ok := forecast[field]; !ok {
			t.Fatalf("forecast wire contract is missing %q: %v", field, forecast)
		}
	}
	if _, leaked := forecast["PredictedClose"]; leaked {
		t.Fatalf("forecast leaked a Go field name: %v", forecast)
	}
	var ready struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	getJSON("/readyz", &ready)
	if ready.Data.Status != "ready" {
		t.Fatalf("ready=%+v", ready)
	}
	page, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	pageBody, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != http.StatusOK || !bytes.Contains(pageBody, []byte("Market intelligence console")) || !strings.Contains(page.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatalf("dashboard status=%d", page.StatusCode)
	}
	metrics, err := http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	metricsBody, _ := io.ReadAll(metrics.Body)
	metrics.Body.Close()
	if !bytes.Contains(metricsBody, []byte("marketdb_http_requests_total")) || !bytes.Contains(metricsBody, []byte("marketdb_storage_pages")) {
		t.Fatalf("metrics=%s", metricsBody)
	}

	requestBody := []byte(`{"kind":"recompute_features","payload":{"symbol":"SYNTH","interval":"1d"}}`)
	unauthorized, _ := http.Post(server.URL+"/api/v1/jobs", "application/json", bytes.NewReader(requestBody))
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.StatusCode)
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/jobs", bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer test-secret")
	request.Header.Set("Idempotency-Key", "http-contract-key-01")
	accepted, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer accepted.Body.Close()
	if accepted.StatusCode != http.StatusAccepted || accepted.Header.Get("Location") == "" {
		body, _ := io.ReadAll(accepted.Body)
		t.Fatalf("accepted=%d %s", accepted.StatusCode, body)
	}
}
