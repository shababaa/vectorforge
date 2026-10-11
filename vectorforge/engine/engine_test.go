package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"byodb/vectorforge/embed"
	"byodb/vectorforge/ingest"
	"byodb/vectorforge/store"
)

const testDim = 64

var baseFiles = map[string]string{
	"docs/durability.md": "# Durability\n\nCommits write the data pages, fsync them, then write and fsync the meta page.\n\n" +
		"## Crash recovery\n\nOn open the newest valid meta page wins, so a torn commit is invisible.\n",
	"docs/cooking.md": "# Banana bread\n\nMash ripe bananas with butter and sugar, then bake for an hour.\n",
	"src/tree.go": "package tree\n\n// Insert adds a key to the copy on write tree.\nfunc Insert(key []byte) {}\n\n" +
		"// Delete removes a key and frees pages.\nfunc Delete(key []byte) {}\n",
	"notes.txt": "Snapshot isolation lets readers see a consistent version while a writer commits.\n",
}

func writeTree(t testing.TB, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type fixture struct {
	dir, root, db string
	emb           embed.Embedder
	cfg           func(*Config)
}

func newFixture(t *testing.T, files map[string]string) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{dir: dir, root: filepath.Join(dir, "corpus"), db: filepath.Join(dir, "vf.db"), emb: embed.NewHash(testDim)}
	writeTree(t, f.root, files)
	return f
}

func (f *fixture) config() Config {
	cfg := Config{Path: f.db, Embedder: f.emb, Sources: []ingest.Source{{Name: "test", Root: f.root}}}
	if f.cfg != nil {
		f.cfg(&cfg)
	}
	return cfg
}

func (f *fixture) open(t *testing.T) *Engine {
	t.Helper()
	e, err := Open(context.Background(), f.config())
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func mustIngest(t *testing.T, e *Engine) IngestSummary {
	t.Helper()
	s, err := e.Ingest(context.Background(), "test", false)
	if err != nil {
		t.Fatalf("ingest: %v (summary %+v)", err, s)
	}
	return s
}

func search(t *testing.T, e *Engine, req SearchRequest) SearchResponse {
	t.Helper()
	resp, err := e.Search(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func mustVerify(t *testing.T, e *Engine) Consistency {
	t.Helper()
	c, err := e.Verify()
	if err != nil || !c.OK {
		t.Fatalf("inconsistent collection: %+v (err %v)", c, err)
	}
	return c
}

func paths(resp SearchResponse) []string {
	out := make([]string, len(resp.Results))
	for i, h := range resp.Results {
		out[i] = h.Path
	}
	return out
}

func TestIngestSearchSurvivesRestart(t *testing.T) {
	f := newFixture(t, baseFiles)
	e := f.open(t)
	s := mustIngest(t, e)
	if s.Added != 4 || s.FilesSeen != 4 || s.Failed != 0 {
		t.Fatalf("summary %+v", s)
	}
	query := SearchRequest{Query: "fsync the meta page after data pages", TopK: 3, Strategy: StrategyExact}
	before := search(t, e, query)
	if len(before.Results) == 0 || before.Results[0].Path != "docs/durability.md" {
		t.Fatalf("unexpected top hit: %v", paths(before))
	}
	if !strings.Contains(before.Results[0].Section, "Durability") || before.Results[0].Snippet == "" {
		t.Fatalf("hit lacks section or snippet: %+v", before.Results[0])
	}
	c := mustVerify(t, e)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	e = f.open(t)
	defer e.Close()
	if e.startup.Vectors != c.Vectors || e.startup.Documents != 4 {
		t.Fatalf("startup report %+v, want %d vectors", e.startup, c.Vectors)
	}
	after := search(t, e, query)
	if len(after.Results) != len(before.Results) {
		t.Fatalf("result count changed across restart: %d vs %d", len(after.Results), len(before.Results))
	}
	for i := range before.Results {
		b, a := before.Results[i], after.Results[i]
		if b.VectorID != a.VectorID || b.Score != a.Score || b.Path != a.Path {
			t.Fatalf("result %d changed across restart: %+v vs %+v", i, b, a)
		}
	}
	mustVerify(t, e)
}

func TestIncrementalIngestOnlyEmbedsChanges(t *testing.T) {
	f := newFixture(t, baseFiles)
	hash := f.emb.(*embed.Hash)
	e := f.open(t)
	defer e.Close()
	mustIngest(t, e)
	embedded := hash.Texts()

	s := mustIngest(t, e)
	if s.Unchanged != 4 || s.Added+s.Updated+s.Deleted != 0 || hash.Texts() != embedded {
		t.Fatalf("re-ingest of unchanged files did work: %+v", s)
	}

	before := search(t, e, SearchRequest{Query: "banana bread bake", TopK: 1, Strategy: StrategyExact})
	oldID := before.Results[0].VectorID
	writeTree(t, f.root, map[string]string{
		"docs/cooking.md": "# Sourdough\n\nFeed the starter, fold the dough and bake in a hot oven.\n",
		"src/new.py":      "def merge_pages(left, right):\n    \"\"\"Merge two sibling pages after delete.\"\"\"\n    return left + right\n",
	})
	if err := os.Remove(filepath.Join(f.root, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	s = mustIngest(t, e)
	if s.Updated != 1 || s.Added != 1 || s.Deleted != 1 || s.Unchanged != 2 {
		t.Fatalf("summary %+v", s)
	}
	resp := search(t, e, SearchRequest{Query: "sourdough starter dough", TopK: 1, Strategy: StrategyExact})
	if resp.Results[0].Path != "docs/cooking.md" || resp.Results[0].VectorID <= oldID {
		t.Fatalf("updated document not re-embedded with a fresh vector ID: %+v", resp.Results[0])
	}
	for _, h := range search(t, e, SearchRequest{Query: "snapshot isolation readers", TopK: 10, Strategy: StrategyExact}).Results {
		if h.Path == "notes.txt" {
			t.Fatal("deleted file still searchable")
		}
	}
	mustVerify(t, e)
	docs, total, err := e.Documents("test", "", "", 0, 100)
	if err != nil || total != 4 || len(docs) != 4 {
		t.Fatalf("documents %d/%d err %v", len(docs), total, err)
	}
}

func manyFiles(n int) map[string]string {
	topics := []string{"btree pages split merge", "fsync durability crash meta", "http handler request router",
		"json encoder struct tags", "mutex lock contention goroutine", "kmeans centroid cluster vector"}
	files := map[string]string{}
	for i := 0; i < n; i++ {
		topic := topics[i%len(topics)]
		files[fmt.Sprintf("gen/f%03d.txt", i)] = fmt.Sprintf("%s item%d variant%d %s", topic, i, i%7, topic)
	}
	return files
}

func TestIVFIsPersistedNotRetrained(t *testing.T) {
	f := newFixture(t, manyFiles(240))
	e := f.open(t)
	mustIngest(t, e)
	meta, err := e.RebuildIVF(context.Background(), RebuildOptions{NList: 12})
	if err != nil {
		t.Fatal(err)
	}
	if meta.NList != 12 || meta.TrainedVectors != 240 || meta.Generation != 1 {
		t.Fatalf("meta %+v", meta)
	}
	// Mutations after training are assigned to existing lists and persisted.
	writeTree(t, f.root, map[string]string{"gen/extra.txt": "kmeans centroid cluster vector extra"})
	mustIngest(t, e)
	lists := map[int64]int{}
	e.mu.RLock()
	for _, id := range e.flat.IDs() {
		lists[id], _ = e.ivf.ListOf(id)
	}
	e.mu.RUnlock()
	query := SearchRequest{Query: "mutex lock contention", TopK: 5, Strategy: StrategyIVF, NProbe: 2}
	before := search(t, e, query)
	mustVerify(t, e)
	e.Close()

	e = f.open(t)
	defer e.Close()
	st, err := e.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if !e.startup.IVFLoaded || e.startup.IVFRetrained || e.startup.MissingPostings != 0 {
		t.Fatalf("startup %+v", e.startup)
	}
	if st.IVF.Meta.TrainedAt != meta.TrainedAt || st.IVF.Meta.Generation != 1 || st.IVF.VectorsSinceTrain != 1 {
		t.Fatalf("IVF metadata changed on reopen: %+v", st.IVF.Meta)
	}
	e.mu.RLock()
	for id, list := range lists {
		if got, _ := e.ivf.ListOf(id); got != list {
			t.Fatalf("vector %d moved from list %d to %d across restart", id, list, got)
		}
	}
	e.mu.RUnlock()
	after := search(t, e, query)
	for i := range before.Results {
		if before.Results[i].VectorID != after.Results[i].VectorID {
			t.Fatalf("IVF results changed across restart: %v vs %v", paths(before), paths(after))
		}
	}
	full, err := e.Compare(context.Background(), SearchRequest{Query: "json encoder", TopK: 10, NProbe: 12})
	if err != nil || full.RecallAtK != 1 || len(full.MissedByIVF) != 0 {
		t.Fatalf("probing every list must equal exact search: recall %v err %v", full.RecallAtK, err)
	}
	mustVerify(t, e)
}

func TestRebuildIsDeterministic(t *testing.T) {
	f := newFixture(t, manyFiles(120))
	e := f.open(t)
	defer e.Close()
	mustIngest(t, e)
	snapshot := func() []float32 {
		if _, err := e.RebuildIVF(context.Background(), RebuildOptions{NList: 6, Seed: 7}); err != nil {
			t.Fatal(err)
		}
		e.mu.RLock()
		defer e.mu.RUnlock()
		return e.ivf.Centroids()
	}
	a, b := snapshot(), snapshot()
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("same seed and data produced different centroids")
		}
	}
	if e.ivfMeta.Generation != 2 {
		t.Fatalf("generation %d", e.ivfMeta.Generation)
	}
}

func TestAutoTrainAfterIngest(t *testing.T) {
	f := newFixture(t, manyFiles(50))
	f.cfg = func(c *Config) { c.IVF.AutoTrainAt = 40 }
	e := f.open(t)
	defer e.Close()
	if s := mustIngest(t, e); !s.AutoTrained {
		t.Fatalf("expected auto-training: %+v", s)
	}
	resp := search(t, e, SearchRequest{Query: "btree pages"})
	if resp.Strategy != StrategyIVF || resp.NList != 7 {
		t.Fatalf("auto strategy should use the trained index with nlist=round(sqrt(50)): %+v", resp)
	}
}

func TestFilters(t *testing.T) {
	f := newFixture(t, baseFiles)
	e := f.open(t)
	defer e.Close()
	mustIngest(t, e)
	resp := search(t, e, SearchRequest{Query: "pages", TopK: 10, Strategy: StrategyExact, Filters: Filters{Kinds: []string{"go"}}})
	if len(resp.Results) == 0 {
		t.Fatal("no Go results")
	}
	for _, h := range resp.Results {
		if h.Kind != "go" {
			t.Fatalf("kind filter leaked %s", h.Path)
		}
	}
	resp = search(t, e, SearchRequest{Query: "pages", TopK: 10, Strategy: StrategyExact, Filters: Filters{PathPrefix: "docs/"}})
	for _, h := range resp.Results {
		if !strings.HasPrefix(h.Path, "docs/") {
			t.Fatalf("path filter leaked %s", h.Path)
		}
	}
	if resp.Stats.Filtered == 0 {
		t.Fatal("filtered count not reported")
	}
	resp = search(t, e, SearchRequest{Query: "pages", Filters: Filters{Sources: []string{"nope"}}})
	if len(resp.Results) != 0 {
		t.Fatal("unknown source matched")
	}
}

func TestUploadAndDelete(t *testing.T) {
	f := newFixture(t, baseFiles)
	e := f.open(t)
	defer e.Close()
	ctx := context.Background()
	res, err := e.Upload(ctx, UploadRequest{Path: "notes/raft.md", Content: "# Raft\n\nLeader election uses randomized timeouts and terms.\n",
		Metadata: map[string]string{"author": "test"}})
	if err != nil || !res.Created || res.Document.ChunkCount != 1 || res.Document.Kind != "markdown" {
		t.Fatalf("upload: %+v %v", res, err)
	}
	again, err := e.Upload(ctx, UploadRequest{Path: "notes/raft.md", Content: "# Raft\n\nLeader election uses randomized timeouts and terms.\n",
		Metadata: map[string]string{"author": "test"}})
	if err != nil || again.Changed {
		t.Fatalf("identical re-upload should be a no-op: %+v %v", again, err)
	}
	resp := search(t, e, SearchRequest{Query: "leader election randomized timeouts", TopK: 1})
	if resp.Results[0].DocID != res.Document.ID || resp.Results[0].Source != UploadSource {
		t.Fatalf("upload not searchable: %v", paths(resp))
	}
	if err := e.DeleteDocument(res.Document.ID); err != nil {
		t.Fatal(err)
	}
	resp = search(t, e, SearchRequest{Query: "leader election randomized timeouts", TopK: 1})
	if len(resp.Results) > 0 && resp.Results[0].DocID == res.Document.ID {
		t.Fatal("deleted upload still searchable")
	}
	if err := e.DeleteDocument(res.Document.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	mustVerify(t, e)

	bad := []UploadRequest{
		{Path: "../escape.md", Content: "x"},
		{Path: "/abs.md", Content: "x"},
		{Path: "ok.md", Content: ""},
		{Path: "ok.md", Content: "bin\x00ary"},
		{Path: "ok.md", Content: strings.Repeat("a", MaxUploadBytes+1)},
		{Path: "ok.md", Content: "x", Kind: "cobol"},
	}
	for _, req := range bad {
		if _, err := e.Upload(ctx, req); !errors.Is(err, ErrInvalid) {
			t.Fatalf("upload %q accepted or wrong error: %v", req.Path, err)
		}
	}
}

func TestSearchValidation(t *testing.T) {
	f := newFixture(t, baseFiles)
	e := f.open(t)
	defer e.Close()
	for _, req := range []SearchRequest{
		{Query: "  "},
		{Query: "x", TopK: MaxTopK + 1},
		{Query: "x", TopK: -1},
		{Query: "x", Strategy: "hnsw"},
		{Query: "x", NProbe: -2},
		{Query: strings.Repeat("y", MaxQueryChars+1)},
	} {
		if _, err := e.Search(context.Background(), req); !errors.Is(err, ErrInvalid) {
			t.Fatalf("request %+v: got %v", req, err)
		}
	}
	if _, err := e.Search(context.Background(), SearchRequest{Query: "x", Strategy: StrategyIVF}); !errors.Is(err, ErrUntrained) {
		t.Fatalf("ivf before training: %v", err)
	}
	resp := search(t, e, SearchRequest{Query: "anything"})
	if resp.Strategy != StrategyExact || len(resp.Results) != 0 {
		t.Fatalf("empty collection search: %+v", resp)
	}
}

func TestReadOnlyAndModelSafeguards(t *testing.T) {
	f := newFixture(t, baseFiles)
	e := f.open(t)
	mustIngest(t, e)
	e.Close()

	f.cfg = func(c *Config) { c.ReadOnly = true }
	ro := f.open(t)
	if resp := search(t, ro, SearchRequest{Query: "banana"}); len(resp.Results) == 0 {
		t.Fatal("read-only search found nothing")
	}
	if _, err := ro.Ingest(context.Background(), "test", false); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only ingest: %v", err)
	}
	if _, err := ro.Upload(context.Background(), UploadRequest{Path: "a.md", Content: "x"}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only upload: %v", err)
	}
	if _, err := ro.RebuildIVF(context.Background(), RebuildOptions{}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only rebuild: %v", err)
	}
	ro.Close()

	f.cfg = nil
	f.emb = embed.NewHash(32)
	if _, err := Open(context.Background(), f.config()); !errors.Is(err, store.ErrConfigMismatch) {
		t.Fatalf("opening with a different model should fail: %v", err)
	}
	if _, err := Open(context.Background(), Config{Path: filepath.Join(f.dir, "x.db"), Embedder: f.emb,
		Sources: []ingest.Source{{Name: UploadSource, Root: f.root}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reserved source name accepted: %v", err)
	}
}

// failingEmbedder fails every call after the first `ok` calls.
type failingEmbedder struct {
	embed.Embedder
	ok    int32
	calls atomic.Int32
}

func (f *failingEmbedder) Embed(ctx context.Context, texts []string, kind embed.Kind) (embed.Result, error) {
	if kind == embed.KindDocument && f.calls.Add(1) > f.ok {
		return embed.Result{}, errors.New("embedding service unavailable")
	}
	return f.Embedder.Embed(ctx, texts, kind)
}

func TestFailedBatchLeavesWholeDocumentsAndResumes(t *testing.T) {
	f := newFixture(t, manyFiles(30))
	f.emb = &failingEmbedder{Embedder: embed.NewHash(testDim), ok: 2}
	f.cfg = func(c *Config) { c.BatchChunks = 5 }
	e := f.open(t)
	s, err := e.Ingest(context.Background(), "test", false)
	if err == nil || s.Added != 10 {
		t.Fatalf("expected failure after two 5-document batches: %+v %v", s, err)
	}
	mustVerify(t, e)
	runs, _ := e.Runs(0)
	if len(runs) != 1 || runs[0].Status != statusFailed {
		t.Fatalf("run not recorded as failed: %+v", runs)
	}
	e.Close()

	f.emb = embed.NewHash(testDim)
	e = f.open(t)
	defer e.Close()
	s = mustIngest(t, e)
	if s.Unchanged != 10 || s.Added != 20 {
		t.Fatalf("resume should skip committed documents: %+v", s)
	}
	if c := mustVerify(t, e); c.Documents != 30 {
		t.Fatalf("documents %d", c.Documents)
	}
}

// gatedEmbedder blocks document embedding until released, so a test can
// observe the engine while an ingest is in flight.
type gatedEmbedder struct {
	embed.Embedder
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gatedEmbedder) Embed(ctx context.Context, texts []string, kind embed.Kind) (embed.Result, error) {
	if kind == embed.KindDocument {
		g.once.Do(func() { close(g.entered) })
		select {
		case <-g.release:
		case <-ctx.Done():
			return embed.Result{}, ctx.Err()
		}
	}
	return g.Embedder.Embed(ctx, texts, kind)
}

func TestBackgroundJobBlocksOtherMutations(t *testing.T) {
	f := newFixture(t, baseFiles)
	gate := &gatedEmbedder{Embedder: embed.NewHash(testDim), entered: make(chan struct{}), release: make(chan struct{})}
	f.emb = gate
	e := f.open(t)
	defer e.Close()
	job, err := e.StartIngest("test", false)
	if err != nil {
		t.Fatal(err)
	}
	<-gate.entered
	if _, err := e.StartIngest("test", false); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent ingest: %v", err)
	}
	if _, err := e.Upload(context.Background(), UploadRequest{Path: "a.md", Content: "x"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("upload during ingest: %v", err)
	}
	if st, _ := e.Stats(); !st.Busy {
		t.Fatal("stats should report the running mutation")
	}
	// Searches proceed while the ingest waits on the embedder.
	search(t, e, SearchRequest{Query: "anything"})
	close(gate.release)
	deadline := time.Now().Add(10 * time.Second)
	for {
		j, _ := e.Job(job.ID)
		if j.Status == statusSucceeded {
			if j.Progress.FilesDone != 4 || j.Progress.DocsCommitted != 4 {
				t.Fatalf("progress %+v", j.Progress)
			}
			break
		}
		if j.Status == statusFailed || time.Now().After(deadline) {
			t.Fatalf("job %+v", j)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := e.StartIngest("missing", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown source: %v", err)
	}
}

func TestConcurrentSearchesDuringMutations(t *testing.T) {
	f := newFixture(t, manyFiles(120))
	e := f.open(t)
	defer e.Close()
	mustIngest(t, e)
	if _, err := e.RebuildIVF(context.Background(), RebuildOptions{NList: 8}); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var searches atomic.Int64
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			strategies := []string{StrategyExact, StrategyIVF}
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				resp, err := e.Search(context.Background(), SearchRequest{Query: "btree pages kmeans", TopK: 5, Strategy: strategies[(w+i)%2]})
				if err != nil {
					t.Error(err)
					return
				}
				for _, h := range resp.Results {
					if h.Snippet == "" || h.Path == "" {
						t.Errorf("incomplete hit %+v", h)
						return
					}
				}
				searches.Add(1)
			}
		}(w)
	}
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		res, err := e.Upload(ctx, UploadRequest{Path: fmt.Sprintf("up/%d.txt", i), Content: fmt.Sprintf("btree pages kmeans upload %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			if err := e.DeleteDocument(res.Document.ID); err != nil {
				t.Fatal(err)
			}
		}
		if i == 10 {
			if _, err := e.RebuildIVF(ctx, RebuildOptions{NList: 8}); err != nil {
				t.Fatal(err)
			}
		}
	}
	close(stop)
	wg.Wait()
	if searches.Load() == 0 {
		t.Fatal("no searches ran")
	}
	if c := mustVerify(t, e); c.Documents != 130 {
		t.Fatalf("documents %d", c.Documents)
	}
}

// crashEmbedder kills the process on its n-th document batch, after earlier
// batches have committed. It runs only inside the child process below.
type crashEmbedder struct {
	embed.Embedder
	n     int32
	calls atomic.Int32
}

func (c *crashEmbedder) Embed(ctx context.Context, texts []string, kind embed.Kind) (embed.Result, error) {
	if kind == embed.KindDocument && c.calls.Add(1) == c.n {
		os.Exit(86)
	}
	return c.Embedder.Embed(ctx, texts, kind)
}

func TestCrashChild(t *testing.T) {
	dir := os.Getenv("VF_ENGINE_CRASH_DIR")
	if dir == "" {
		t.Skip("helper process for TestCrashMidIngestRecovers")
	}
	e, err := Open(context.Background(), Config{Path: filepath.Join(dir, "vf.db"),
		Embedder: &crashEmbedder{Embedder: embed.NewHash(testDim), n: 4}, BatchChunks: 4,
		Sources: []ingest.Source{{Name: "test", Root: filepath.Join(dir, "corpus")}}})
	if err != nil {
		t.Fatal(err)
	}
	e.Ingest(context.Background(), "test", false)
	t.Fatal("embedder should have terminated the process")
}

func TestCrashMidIngestRecovers(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a child process")
	}
	f := newFixture(t, manyFiles(40))
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "VF_ENGINE_CRASH_DIR="+f.dir)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 86 {
		t.Fatalf("child did not crash as planned: %v\n%s", err, out)
	}
	e := f.open(t)
	defer e.Close()
	if e.startup.InterruptedRuns != 1 {
		t.Fatalf("crashed run not marked interrupted: %+v", e.startup)
	}
	c := mustVerify(t, e)
	if c.Documents != 12 {
		t.Fatalf("expected the three committed 4-document batches to survive, got %d documents", c.Documents)
	}
	s := mustIngest(t, e)
	if s.Unchanged != 12 || s.Added != 28 {
		t.Fatalf("resume after crash: %+v", s)
	}
	mustVerify(t, e)
	runs, _ := e.Runs(0)
	statuses := map[string]int{}
	for _, r := range runs {
		statuses[r.Status]++
	}
	if statuses[statusInterrupted] != 1 || statuses[statusSucceeded] != 1 {
		t.Fatalf("run history %+v", statuses)
	}
}

func TestSnippetPrefersMatchingLines(t *testing.T) {
	text := "alpha\nbeta\ngamma\nthe meta page is fsynced\ndelta"
	if got := Snippet(text, "fsync meta", 30); !strings.Contains(got, "meta page") {
		t.Fatalf("snippet %q", got)
	}
	if got := Snippet(strings.Repeat("é", 100), "x", 21); len(got) > 24 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long line snippet %q", got)
	}
}
