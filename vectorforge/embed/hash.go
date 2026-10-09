package embed

import (
	"context"
	"hash/fnv"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode"

	"byodb/vectorforge/vector"
)

// Hash is a deterministic feature-hashing embedder: each lowercase word adds
// a signed unit to one of Dimension buckets. Cosine similarity between Hash
// vectors measures word overlap, which makes search results predictable in
// tests. It has no semantic understanding and is not used outside tests.
type Hash struct {
	Dimension int
	ModelID   string
	calls     atomic.Int64
	texts     atomic.Int64
}

// NewHash returns a Hash embedder whose model ID encodes its dimension.
func NewHash(dim int) *Hash {
	return &Hash{Dimension: dim, ModelID: "test/hash-" + strconv.Itoa(dim)}
}

// Texts reports how many texts have been embedded, for tests that count work.
func (h *Hash) Texts() int64 { return h.texts.Load() }

func (h *Hash) Info(context.Context) (Info, error) {
	return Info{ModelID: h.ModelID, Dimension: h.Dimension, MaxSeqLength: 1 << 20, MaxBatch: 256}, nil
}

func (h *Hash) Embed(ctx context.Context, texts []string, _ Kind) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	h.calls.Add(1)
	h.texts.Add(int64(len(texts)))
	out := Result{Vectors: make([][]float32, len(texts)), Tokens: make([]int, len(texts)), Truncated: make([]bool, len(texts))}
	for i, text := range texts {
		v := make([]float32, h.Dimension)
		words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		for _, w := range words {
			f := fnv.New64a()
			f.Write([]byte(w))
			sum := f.Sum64()
			if sum>>63 == 1 {
				v[sum%uint64(h.Dimension)]--
			} else {
				v[sum%uint64(h.Dimension)]++
			}
		}
		if _, err := vector.Normalize(v, v); err != nil {
			clear(v)
			v[0] = 1 // no words, or perfectly cancelling hashes
		}
		out.Vectors[i], out.Tokens[i] = v, len(words)
	}
	return out, nil
}
