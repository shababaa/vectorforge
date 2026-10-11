package engine

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"byodb/vectorforge/embed"
	"byodb/vectorforge/hrtime"
	"byodb/vectorforge/index"
	"byodb/vectorforge/store"
	"byodb/vectorforge/vector"
)

const (
	MaxTopK       = 100
	MaxQueryChars = 2000
	snippetChars  = 360
)

const (
	StrategyAuto  = "auto"
	StrategyExact = "exact"
	StrategyIVF   = "ivf"
)

// Filters restrict search to matching documents. Empty fields match all.
type Filters struct {
	Kinds      []string `json:"kinds,omitempty"`
	Sources    []string `json:"sources,omitempty"`
	PathPrefix string   `json:"path_prefix,omitempty"`
}

// SearchRequest is a semantic search query.
type SearchRequest struct {
	Query       string  `json:"query"`
	TopK        int     `json:"top_k"`
	Strategy    string  `json:"strategy"`
	NProbe      int     `json:"nprobe,omitempty"`
	Filters     Filters `json:"filters"`
	IncludeText bool    `json:"include_text"`
}

// Hit is one ranked chunk.
type Hit struct {
	Rank      int     `json:"rank"`
	Score     float32 `json:"score"`
	VectorID  int64   `json:"vector_id"`
	DocID     string  `json:"doc_id"`
	Source    string  `json:"source"`
	Path      string  `json:"path"`
	Kind      string  `json:"kind"`
	Ordinal   int     `json:"chunk_ordinal"`
	Section   string  `json:"section"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Snippet   string  `json:"snippet"`
	Text      string  `json:"text,omitempty"`
}

// Timing breaks down where a search spent its time, in milliseconds.
type Timing struct {
	EmbedMs  float64 `json:"embed_ms"`
	SearchMs float64 `json:"search_ms"`
	FetchMs  float64 `json:"fetch_ms"`
	TotalMs  float64 `json:"total_ms"`
}

// SearchResponse is the ranked result of a query.
type SearchResponse struct {
	Query    string            `json:"query"`
	Strategy string            `json:"strategy"`
	TopK     int               `json:"top_k"`
	NProbe   int               `json:"nprobe,omitempty"`
	NList    int               `json:"nlist,omitempty"`
	ModelID  string            `json:"model_id"`
	Vectors  int               `json:"vectors_indexed"`
	Results  []Hit             `json:"results"`
	Stats    index.SearchStats `json:"stats"`
	Stale    int               `json:"stale_hits_skipped"`
	Timing   Timing            `json:"timing"`
}

func ms(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 }

func (req *SearchRequest) normalize() error {
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		return fmt.Errorf("%w: query is required", ErrInvalid)
	}
	if utf8.RuneCountInString(req.Query) > MaxQueryChars {
		return fmt.Errorf("%w: query exceeds %d characters", ErrInvalid, MaxQueryChars)
	}
	if req.TopK == 0 {
		req.TopK = 10
	}
	if req.TopK < 1 || req.TopK > MaxTopK {
		return fmt.Errorf("%w: top_k must be between 1 and %d", ErrInvalid, MaxTopK)
	}
	switch req.Strategy {
	case "":
		req.Strategy = StrategyAuto
	case StrategyAuto, StrategyExact, StrategyIVF:
	default:
		return fmt.Errorf("%w: strategy must be exact, ivf or auto", ErrInvalid)
	}
	if req.NProbe < 0 {
		return fmt.Errorf("%w: nprobe must be positive", ErrInvalid)
	}
	if len(req.Filters.Kinds) > 16 || len(req.Filters.Sources) > 16 || len(req.Filters.PathPrefix) > store.MaxPathBytes {
		return fmt.Errorf("%w: filters are too large", ErrInvalid)
	}
	return nil
}

// Search embeds the query and returns the top-k chunks.
func (e *Engine) Search(ctx context.Context, req SearchRequest) (SearchResponse, error) {
	started := hrtime.Now()
	if err := req.normalize(); err != nil {
		return SearchResponse{}, err
	}
	q, embedTime, err := e.embedQuery(ctx, req.Query)
	if err != nil {
		return SearchResponse{}, err
	}
	resp, refs, err := e.searchVector(q, req)
	if err != nil {
		return SearchResponse{}, err
	}
	resp.Timing.EmbedMs = ms(embedTime)
	if err := e.fetch(&resp, refs, req); err != nil {
		return SearchResponse{}, err
	}
	resp.Timing.TotalMs = ms(hrtime.Since(started))
	return resp, nil
}

func (e *Engine) embedQuery(ctx context.Context, query string) ([]float32, time.Duration, error) {
	started := hrtime.Now()
	res, err := e.emb.Embed(ctx, []string{query}, embed.KindQuery)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: embed query: %w", ErrEmbedder, err)
	}
	if len(res.Vectors) != 1 || len(res.Vectors[0]) != e.info.Dimension {
		return nil, 0, fmt.Errorf("embed query: unexpected embedding shape")
	}
	return res.Vectors[0], hrtime.Since(started), nil
}

type hitRef struct {
	hit vector.Hit
	ref docRef
}

// searchVector ranks vectors under the read lock and copies the document
// references it needs, so fetching chunk text happens without the lock.
func (e *Engine) searchVector(q []float32, req SearchRequest) (SearchResponse, []hitRef, error) {
	resp := SearchResponse{Query: req.Query, TopK: req.TopK, ModelID: e.info.ModelID}
	e.mu.RLock()
	defer e.mu.RUnlock()
	strategy := req.Strategy
	if strategy == StrategyAuto {
		strategy = StrategyExact
		if e.ivf != nil {
			strategy = StrategyIVF
		}
	}
	filter := e.filterLocked(req.Filters)
	started := hrtime.Now()
	var hits []vector.Hit
	switch strategy {
	case StrategyExact:
		hits, resp.Stats = e.flat.Search(q, req.TopK, filter)
	case StrategyIVF:
		if e.ivf == nil {
			return SearchResponse{}, nil, ErrUntrained
		}
		nprobe := req.NProbe
		if nprobe == 0 {
			nprobe = e.cfg.IVF.NProbe
		}
		nprobe = min(nprobe, e.ivf.NList())
		hits, resp.Stats = e.ivf.Search(q, req.TopK, nprobe, filter)
		resp.NProbe, resp.NList = nprobe, e.ivf.NList()
	}
	resp.Timing.SearchMs = ms(hrtime.Since(started))
	resp.Strategy, resp.Vectors = strategy, e.flat.Len()
	refs := make([]hitRef, 0, len(hits))
	for _, h := range hits {
		if ref, ok := e.refs[h.ID]; ok {
			refs = append(refs, hitRef{hit: h, ref: *ref})
		}
	}
	return resp, refs, nil
}

// filterLocked compiles filters into an index predicate; e.mu must be held.
func (e *Engine) filterLocked(f Filters) index.Filter {
	if len(f.Kinds) == 0 && len(f.Sources) == 0 && f.PathPrefix == "" {
		return nil
	}
	kinds, sources := toSet(f.Kinds), toSet(f.Sources)
	return func(id int64) bool {
		ref, ok := e.refs[id]
		if !ok {
			return false
		}
		if kinds != nil && !kinds[ref.Kind] {
			return false
		}
		if sources != nil && !sources[ref.Source] {
			return false
		}
		return strings.HasPrefix(ref.Path, f.PathPrefix)
	}
}

func toSet(xs []string) map[string]bool {
	if len(xs) == 0 {
		return nil
	}
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// fetch loads chunk text for ranked hits from a database snapshot. A hit
// whose chunk was deleted after ranking is skipped and counted as stale.
func (e *Engine) fetch(resp *SearchResponse, refs []hitRef, req SearchRequest) error {
	started := hrtime.Now()
	resp.Results = make([]Hit, 0, len(refs))
	err := e.st.View(func(r *store.Reader) error {
		for _, hr := range refs {
			c, err := r.ChunkByVectorID(hr.hit.ID)
			if isNotFound(err) || (err == nil && c.DocID != hr.ref.ID) {
				resp.Stale++
				continue
			}
			if err != nil {
				return err
			}
			h := Hit{
				Rank: len(resp.Results) + 1, Score: hr.hit.Score, VectorID: hr.hit.ID, DocID: c.DocID,
				Source: hr.ref.Source, Path: hr.ref.Path, Kind: hr.ref.Kind, Ordinal: c.Ordinal, Section: c.Section,
				StartLine: c.StartLine, EndLine: c.EndLine, Snippet: Snippet(c.Text, req.Query, snippetChars),
			}
			if req.IncludeText {
				h.Text = c.Text
			}
			resp.Results = append(resp.Results, h)
		}
		return nil
	})
	resp.Timing.FetchMs = ms(hrtime.Since(started))
	return err
}

// CompareResponse runs one query embedding through both indexes.
type CompareResponse struct {
	Query     string         `json:"query"`
	TopK      int            `json:"top_k"`
	Exact     SearchResponse `json:"exact"`
	IVF       SearchResponse `json:"ivf"`
	RecallAtK float64        `json:"recall_at_k"`
	// MissedByIVF lists exact top-k vector IDs that IVF did not return.
	MissedByIVF []int64 `json:"missed_by_ivf"`
}

// Compare shows what the approximate index returns relative to exact search.
func (e *Engine) Compare(ctx context.Context, req SearchRequest) (CompareResponse, error) {
	if err := req.normalize(); err != nil {
		return CompareResponse{}, err
	}
	q, embedTime, err := e.embedQuery(ctx, req.Query)
	if err != nil {
		return CompareResponse{}, err
	}
	out := CompareResponse{Query: req.Query, TopK: req.TopK}
	var exactRefs, ivfRefs []hitRef
	req.Strategy = StrategyExact
	if out.Exact, exactRefs, err = e.searchVector(q, req); err != nil {
		return CompareResponse{}, err
	}
	req.Strategy = StrategyIVF
	if out.IVF, ivfRefs, err = e.searchVector(q, req); err != nil {
		return CompareResponse{}, err
	}
	exactHits, ivfHits := hitsOf(exactRefs), hitsOf(ivfRefs)
	out.RecallAtK = index.RecallAtK(exactHits, ivfHits, req.TopK)
	found := map[int64]bool{}
	for _, h := range ivfHits {
		found[h.ID] = true
	}
	for _, h := range exactHits {
		if !found[h.ID] {
			out.MissedByIVF = append(out.MissedByIVF, h.ID)
		}
	}
	for _, side := range []struct {
		resp *SearchResponse
		refs []hitRef
	}{{&out.Exact, exactRefs}, {&out.IVF, ivfRefs}} {
		side.resp.Timing.EmbedMs = ms(embedTime)
		if err := e.fetch(side.resp, side.refs, req); err != nil {
			return CompareResponse{}, err
		}
		side.resp.Timing.TotalMs = side.resp.Timing.EmbedMs + side.resp.Timing.SearchMs + side.resp.Timing.FetchMs
	}
	return out, nil
}

func hitsOf(refs []hitRef) []vector.Hit {
	out := make([]vector.Hit, len(refs))
	for i, r := range refs {
		out[i] = r.hit
	}
	return out
}

// Snippet picks the run of whole lines (at most maxChars) that contains the
// most distinct query words, so results show why they matched. Ranking is
// purely by embedding similarity; the snippet is presentation only.
func Snippet(text, query string, maxChars int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	terms := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) >= 3 {
			terms[w] = true
		}
	}
	bestStart, bestEnd, bestScore := 0, 0, -1
	for i := range lines {
		size, seen := 0, map[string]bool{}
		j := i
		for ; j < len(lines) && (j == i || size+len(lines[j])+1 <= maxChars); j++ {
			size += len(lines[j]) + 1
			lower := strings.ToLower(lines[j])
			for t := range terms {
				if !seen[t] && strings.Contains(lower, t) {
					seen[t] = true
				}
			}
		}
		if len(seen) > bestScore {
			bestStart, bestEnd, bestScore = i, j, len(seen)
		}
		if len(terms) == 0 {
			break
		}
	}
	out := strings.Join(lines[bestStart:bestEnd], "\n")
	if len(out) > maxChars {
		cut := maxChars
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut] + "…"
	}
	return out
}
