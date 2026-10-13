package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"byodb/vectorforge/embed"
	"byodb/vectorforge/engine"
	"byodb/vectorforge/ingest"
)

type envelope struct {
	Data      json.RawMessage `json:"data"`
	Error     *struct{ Code, Message string }
	RequestID string `json:"request_id"`
}

type harness struct {
	t   *testing.T
	srv *Server
	eng *engine.Engine
}

func newHarness(t *testing.T, emb embed.Embedder, opts Options, readOnly bool) *harness {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "corpus")
	for rel, content := range map[string]string{
		"a.md": "# Commit protocol\n\nData pages are fsynced before the meta page.\n",
		"b.go": "package b\n\n// Split divides a full leaf page.\nfunc Split() {}\n",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755)
		os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644)
	}
	db := filepath.Join(dir, "vf.db")
	cfg := engine.Config{Path: db, Embedder: emb, Sources: []ingest.Source{{Name: "corpus", Root: root}}}
	if readOnly {
		e, err := engine.Open(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		e.Close()
		cfg.ReadOnly = true
	}
	e, err := engine.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return &harness{t: t, srv: New(e, opts), eng: e}
}

type reqOpt func(*http.Request)

func remote(addr string) reqOpt { return func(r *http.Request) { r.RemoteAddr = addr } }
func header(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

func (h *harness) do(method, path, body string, opts ...reqOpt) (int, envelope, http.Header) {
	h.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:50000"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		h.t.Fatalf("%s %s: response is not JSON: %q", method, path, rec.Body.String())
	}
	return rec.Code, env, rec.Header()
}

func (h *harness) expect(method, path, body string, want int, wantCode string, opts ...reqOpt) envelope {
	h.t.Helper()
	status, env, _ := h.do(method, path, body, opts...)
	if status != want {
		h.t.Fatalf("%s %s: status %d, want %d (%+v %s)", method, path, status, want, env.Error, env.Data)
	}
	if wantCode != "" && (env.Error == nil || env.Error.Code != wantCode) {
		h.t.Fatalf("%s %s: error %+v, want code %s", method, path, env.Error, wantCode)
	}
	if env.RequestID == "" {
		h.t.Fatalf("%s %s: missing request_id", method, path)
	}
	return env
}

func (h *harness) waitJob(id string) engine.Job {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		env := h.expect("GET", "/api/v1/jobs/"+id, "", 200, "")
		var job engine.Job
		json.Unmarshal(env.Data, &job)
		if job.Status != "running" {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("job %s did not finish", id)
	return engine.Job{}
}

func TestEndToEndThroughHTTP(t *testing.T) {
	h := newHarness(t, embed.NewHash(64), Options{}, false)
	status, env, hdr := h.do("GET", "/health", "")
	if status != 200 || hdr.Get("X-Content-Type-Options") != "nosniff" || hdr.Get("Content-Security-Policy") == "" {
		t.Fatalf("health %d headers %v", status, hdr)
	}
	var health map[string]any
	json.Unmarshal(env.Data, &health)
	if health["status"] != "ok" || health["dimension"].(float64) != 64 {
		t.Fatalf("health %v", health)
	}

	env = h.expect("POST", "/api/v1/ingest", `{"source":"corpus"}`, 202, "")
	var job engine.Job
	json.Unmarshal(env.Data, &job)
	if done := h.waitJob(job.ID); done.Status != "succeeded" {
		t.Fatalf("ingest job %+v", done)
	}

	env = h.expect("POST", "/api/v1/search", `{"query":"meta page fsync","top_k":2,"strategy":"exact"}`, 200, "")
	var resp engine.SearchResponse
	json.Unmarshal(env.Data, &resp)
	if len(resp.Results) == 0 || resp.Results[0].Path != "a.md" || resp.Timing.TotalMs <= 0 {
		t.Fatalf("search %+v", resp)
	}

	env = h.expect("POST", "/api/v1/documents", `{"path":"notes/x.md","content":"# Vacuum\n\nFree pages are reused after commits."}`, 201, "")
	var up engine.UploadResult
	json.Unmarshal(env.Data, &up)
	h.expect("POST", "/api/v1/documents", `{"path":"notes/x.md","content":"# Vacuum\n\nFree pages are reused after commits."}`, 200, "")

	env = h.expect("GET", "/api/v1/documents?limit=10", "", 200, "")
	var list struct {
		Total int `json:"total"`
	}
	json.Unmarshal(env.Data, &list)
	if list.Total != 3 {
		t.Fatalf("documents total %d", list.Total)
	}
	h.expect("GET", "/api/v1/documents/"+up.Document.ID, "", 200, "")
	h.expect("DELETE", "/api/v1/documents/"+up.Document.ID, "", 200, "")
	h.expect("DELETE", "/api/v1/documents/"+up.Document.ID, "", 404, "not_found")
	h.expect("GET", "/api/v1/documents/"+up.Document.ID, "", 404, "not_found")

	h.expect("POST", "/api/v1/search", `{"query":"x","strategy":"ivf"}`, 409, "index_not_trained")
	env = h.expect("POST", "/api/v1/index/rebuild", `{"nlist":2}`, 202, "")
	json.Unmarshal(env.Data, &job)
	if done := h.waitJob(job.ID); done.Status != "succeeded" {
		t.Fatalf("rebuild job %+v", done)
	}
	env = h.expect("POST", "/api/v1/search/compare", `{"query":"leaf page split","top_k":2,"nprobe":2}`, 200, "")
	var cmp engine.CompareResponse
	json.Unmarshal(env.Data, &cmp)
	if cmp.RecallAtK != 1 {
		t.Fatalf("full-probe compare recall %v", cmp.RecallAtK)
	}
	env = h.expect("GET", "/api/v1/index/stats", "", 200, "")
	var st engine.Stats
	json.Unmarshal(env.Data, &st)
	if !st.IVF.Trained || st.Documents != 2 || st.IVF.Meta.NList != 2 {
		t.Fatalf("stats %+v", st)
	}
	env = h.expect("GET", "/api/v1/index/verify", "", 200, "")
	var c engine.Consistency
	json.Unmarshal(env.Data, &c)
	if !c.OK {
		t.Fatalf("verify %+v", c)
	}
	h.expect("GET", "/api/v1/runs", "", 200, "")
	h.expect("GET", "/api/v1/sources", "", 200, "")
}

func TestValidationErrors(t *testing.T) {
	h := newHarness(t, embed.NewHash(64), Options{}, false)
	h.expect("POST", "/api/v1/search", `{"query":`, 400, "invalid_json")
	h.expect("POST", "/api/v1/search", `{"query":"x","bogus":1}`, 400, "invalid_json")
	h.expect("POST", "/api/v1/search", `{"query":"x"} {"query":"y"}`, 400, "invalid_json")
	h.expect("POST", "/api/v1/search", `{"query":"   "}`, 400, "invalid_request")
	h.expect("POST", "/api/v1/search", `{"query":"x","top_k":1000}`, 400, "invalid_request")
	h.expect("POST", "/api/v1/search", `{"query":"x","strategy":"hnsw"}`, 400, "invalid_request")
	h.expect("POST", "/api/v1/search", `{"query":"x"}`, 415, "unsupported_media_type", header("Content-Type", "text/plain"))
	h.expect("POST", "/api/v1/search", `{"query":"`+strings.Repeat("a", maxJSONBody)+`"}`, 413, "body_too_large")
	h.expect("POST", "/api/v1/ingest", `{}`, 400, "invalid_request")
	h.expect("POST", "/api/v1/ingest", `{"source":"etc"}`, 404, "not_found")
	h.expect("POST", "/api/v1/documents", `{"path":"../../secret","content":"x"}`, 400, "invalid_request")
	h.expect("POST", "/api/v1/index/rebuild", `{"nlist":-1}`, 400, "invalid_request")
	h.expect("POST", "/api/v1/index/rebuild", `{"nlist":4}`, 400, "invalid_request") // no vectors yet
	h.expect("GET", "/api/v1/documents?limit=0", "", 400, "invalid_request")
	h.expect("GET", "/api/v1/documents?offset=x", "", 400, "invalid_request")
	h.expect("GET", "/api/v1/jobs/nope", "", 404, "not_found")
	h.expect("GET", "/api/v1/nothing", "", 404, "not_found")
	status, _, hdr := h.do("PUT", "/api/v1/search", `{}`)
	if status != http.StatusMethodNotAllowed || hdr.Get("Allow") != "POST" {
		t.Fatalf("PUT search status %d allow %q", status, hdr.Get("Allow"))
	}
	h.expect("GET", "/api/v1/documents/x/extra", "", 404, "not_found")
}

func TestMutationAuthorization(t *testing.T) {
	h := newHarness(t, embed.NewHash(64), Options{}, false)
	body := `{"path":"a.md","content":"hello"}`
	h.expect("POST", "/api/v1/documents", body, 403, "remote_mutation_rejected", remote("10.1.2.3:4000"))
	h.expect("POST", "/api/v1/documents", body, 403, "origin_rejected", header("Origin", "https://evil.example"))
	h.expect("POST", "/api/v1/search", `{"query":"hello"}`, 200, "", remote("10.1.2.3:4000")) // reads stay open
	h.expect("POST", "/api/v1/documents", body, 201, "", remote("[::1]:4000"))

	h = newHarness(t, embed.NewHash(64), Options{Token: "s3cret-token"}, false)
	h.expect("POST", "/api/v1/documents", body, 401, "unauthorized")
	h.expect("POST", "/api/v1/documents", body, 401, "unauthorized", header("Authorization", "Bearer wrong"))
	h.expect("POST", "/api/v1/documents", body, 201, "", remote("10.1.2.3:4000"), header("Authorization", "Bearer s3cret-token"))

	h = newHarness(t, embed.NewHash(64), Options{}, true)
	h.expect("POST", "/api/v1/documents", body, 403, "read_only")
	h.expect("POST", "/api/v1/ingest", `{"source":"corpus"}`, 403, "read_only")
	h.expect("DELETE", "/api/v1/documents/abc", "", 403, "read_only")
	h.expect("POST", "/api/v1/search", `{"query":"hello"}`, 200, "")
}

type gate struct {
	embed.Embedder
	entered, release chan struct{}
	once             sync.Once
}

func (g *gate) Embed(ctx context.Context, texts []string, kind embed.Kind) (embed.Result, error) {
	if kind == embed.KindDocument {
		g.once.Do(func() { close(g.entered) })
		<-g.release
	}
	return g.Embedder.Embed(ctx, texts, kind)
}

func TestBusyReturnsConflict(t *testing.T) {
	g := &gate{Embedder: embed.NewHash(64), entered: make(chan struct{}), release: make(chan struct{})}
	h := newHarness(t, g, Options{}, false)
	env := h.expect("POST", "/api/v1/ingest", `{"source":"corpus"}`, 202, "")
	<-g.entered
	h.expect("POST", "/api/v1/ingest", `{"source":"corpus"}`, 409, "busy")
	h.expect("POST", "/api/v1/index/rebuild", ``, 409, "busy")
	close(g.release)
	var job engine.Job
	json.Unmarshal(env.Data, &job)
	h.waitJob(job.ID)
}

type downEmbedder struct{ embed.Embedder }

func (downEmbedder) Embed(context.Context, []string, embed.Kind) (embed.Result, error) {
	return embed.Result{}, errors.New("connection refused")
}

type rejectingEmbedder struct{ embed.Embedder }

func (rejectingEmbedder) Embed(context.Context, []string, embed.Kind) (embed.Result, error) {
	return embed.Result{}, &embed.StatusError{Code: 422, Detail: "text too long"}
}

func TestEmbedderFailuresMapToStatuses(t *testing.T) {
	h := newHarness(t, downEmbedder{embed.NewHash(64)}, Options{}, false)
	h.expect("POST", "/api/v1/search", `{"query":"x"}`, 503, "embedder_unavailable")
	h = newHarness(t, rejectingEmbedder{embed.NewHash(64)}, Options{}, false)
	h.expect("POST", "/api/v1/search", `{"query":"x"}`, 400, "invalid_request")
}

func TestPanicsBecomeJSON500(t *testing.T) {
	h := newHarness(t, embed.NewHash(64), Options{}, false)
	handler := h.srv.middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "internal_error") || strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("panic response %d %s", rec.Code, rec.Body.String())
	}
}

func TestBenchmarksAreServedReadOnly(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ivf-sweep.json"), []byte(`{"runs":[1,2]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{`), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(`x`), 0o644)
	os.MkdirAll(filepath.Join(dir, "bench", "runs"), 0o755)
	os.WriteFile(filepath.Join(dir, "bench", "synthetic.json"), []byte(`{"suite":"synthetic"}`), 0o644)
	os.WriteFile(filepath.Join(dir, "bench", "runs", "deep.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(filepath.Dir(dir), "outside.json"), []byte(`{}`), 0o644)
	h := newHarness(t, embed.NewHash(64), Options{BenchmarksDir: dir}, false)
	env := h.expect("GET", "/api/v1/benchmarks", "", 200, "")
	var names []string
	json.Unmarshal(env.Data, &names)
	if strings.Join(names, ",") != "bench/synthetic.json,broken.json,ivf-sweep.json" {
		t.Fatalf("names %v", names)
	}
	env = h.expect("GET", "/api/v1/benchmarks/ivf-sweep.json", "", 200, "")
	if string(env.Data) != `{"runs":[1,2]}` {
		t.Fatalf("data %s", env.Data)
	}
	env = h.expect("GET", "/api/v1/benchmarks/bench/synthetic.json", "", 200, "")
	if string(env.Data) != `{"suite":"synthetic"}` {
		t.Fatalf("data %s", env.Data)
	}
	h.expect("GET", "/api/v1/benchmarks/..%2Fvf.db", "", 404, "not_found")
	h.expect("GET", "/api/v1/benchmarks/..%2Foutside.json", "", 404, "not_found")
	h.expect("GET", "/api/v1/benchmarks/bench/runs/deep.json", "", 404, "not_found")
	h.expect("GET", "/api/v1/benchmarks/bench%5Csynthetic.json", "", 404, "not_found")
	h.expect("GET", "/api/v1/benchmarks/notes.txt", "", 404, "not_found")
	h.expect("GET", "/api/v1/benchmarks/broken.json", "", 500, "internal_error")
}
