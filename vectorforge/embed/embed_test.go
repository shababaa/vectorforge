package embed

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"byodb/vectorforge/vector"
)

type fakeService struct {
	modelID  string
	dim      int
	maxBatch int
	fail5xx  atomic.Int32 // number of upcoming requests to fail with 503
	batches  atomic.Int32
	mutate   func(*batchResponse)
}

func (f *fakeService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.fail5xx.Load() > 0 {
		f.fail5xx.Add(-1)
		http.Error(w, "warming up", http.StatusServiceUnavailable)
		return
	}
	switch r.URL.Path {
	case "/health":
		json.NewEncoder(w).Encode(healthResponse{Status: "ok", ModelID: f.modelID, Dimension: f.dim, Normalized: true, MaxBatch: f.maxBatch})
	case "/embed/batch":
		var req batchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Texts) == 0 || len(req.Texts) > f.maxBatch {
			http.Error(w, `{"detail":"bad batch"}`, http.StatusUnprocessableEntity)
			return
		}
		f.batches.Add(1)
		resp := batchResponse{ModelID: f.modelID, Dimension: f.dim}
		for i := range req.Texts {
			v := make([]float32, f.dim)
			v[i%f.dim] = 1
			resp.Embeddings = append(resp.Embeddings, v)
			resp.Tokens = append(resp.Tokens, len(req.Texts[i]))
			resp.Truncated = append(resp.Truncated, false)
		}
		if f.mutate != nil {
			f.mutate(&resp)
		}
		json.NewEncoder(w).Encode(resp)
	default:
		http.NotFound(w, r)
	}
}

func newFake(t *testing.T) (*fakeService, *Client) {
	t.Helper()
	f := &fakeService{modelID: "fake@1", dim: 8, maxBatch: 3}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func TestClientBatchesAndPreservesOrder(t *testing.T) {
	f, c := newFake(t)
	texts := []string{"a", "bb", "ccc", "dddd", "eeeee", "ffffff", "g"}
	res, err := c.Embed(context.Background(), texts, KindDocument)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.batches.Load(); got != 3 {
		t.Fatalf("batches = %d, want 3 (max batch 3)", got)
	}
	if len(res.Vectors) != len(texts) {
		t.Fatalf("got %d vectors", len(res.Vectors))
	}
	for i, n := range res.Tokens {
		if n != len(texts[i]) {
			t.Fatalf("token count %d out of order: %v", i, res.Tokens)
		}
	}
}

func TestClientRetriesUnavailableButNotRejectedInput(t *testing.T) {
	f, c := newFake(t)
	f.fail5xx.Store(2)
	if _, err := c.Info(context.Background()); err != nil {
		t.Fatalf("two 503s then success should succeed: %v", err)
	}
	f.fail5xx.Store(attempts)
	_, err := c.Embed(context.Background(), []string{"x"}, KindQuery)
	var status *StatusError
	if !errors.As(err, &status) || status.Code != 503 || !IsUnavailable(err) {
		t.Fatalf("persistent 503 should surface as unavailable, got %v", err)
	}
	f.maxBatch = 0 // the fake now rejects every batch with 422
	_, err = c.Embed(context.Background(), []string{"x"}, KindQuery)
	if !errors.As(err, &status) || status.Code != 422 || IsUnavailable(err) {
		t.Fatalf("422 should not be retried or reported as unavailable, got %v", err)
	}
}

func TestClientRejectsInconsistentResponses(t *testing.T) {
	cases := map[string]struct {
		mutate func(*batchResponse)
		want   error
	}{
		"model swap":  {func(r *batchResponse) { r.ModelID = "other@2" }, ErrModelChanged},
		"wrong count": {func(r *batchResponse) { r.Embeddings = r.Embeddings[:0] }, ErrBadResponse},
		"not unit":    {func(r *batchResponse) { r.Embeddings[0][0] = 3 }, ErrBadResponse},
		"wrong dim":   {func(r *batchResponse) { r.Embeddings[0] = r.Embeddings[0][:4] }, ErrBadResponse},
		"nan":         {func(r *batchResponse) { r.Embeddings[0][1] = float32(math.NaN()) }, ErrBadResponse},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f, c := newFake(t)
			if name == "nan" {
				// JSON cannot carry NaN, so validate the helper directly.
				v := [][]float32{{1, float32(math.NaN())}}
				if err := normalizeAll(v, 2); !errors.Is(err, ErrBadResponse) {
					t.Fatalf("NaN accepted: %v", err)
				}
				return
			}
			f.mutate = tc.mutate
			if _, err := c.Embed(context.Background(), []string{"x", "y"}, KindDocument); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestClientRejectsBadURLs(t *testing.T) {
	for _, u := range []string{"", "localhost:8090", "ftp://x", "http://"} {
		if _, err := NewClient(u, 0); err == nil {
			t.Fatalf("accepted %q", u)
		}
	}
}

func TestWaitReadyGivesUpAtDeadline(t *testing.T) {
	c, _ := NewClient("http://127.0.0.1:1", time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	if _, err := c.WaitReady(ctx); err == nil {
		t.Fatal("expected failure for an unreachable service")
	}
}

func TestHashIsDeterministicAndLexical(t *testing.T) {
	h := NewHash(64)
	res, err := h.Embed(context.Background(), []string{"copy on write tree", "Copy-on-write TREE", "banana bread", ""}, KindDocument)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range res.Vectors {
		if !vector.IsUnit(v) {
			t.Fatalf("vector %d not unit", i)
		}
	}
	if s := vector.Dot(res.Vectors[0], res.Vectors[1]); s < 0.999 {
		t.Fatalf("case/punctuation variants should match exactly, got %f", s)
	}
	if vector.Dot(res.Vectors[0], res.Vectors[2]) > 0.5 {
		t.Fatal("unrelated texts should not be similar")
	}
	if h.Texts() != 4 {
		t.Fatalf("texts = %d", h.Texts())
	}
}
