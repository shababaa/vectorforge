// Package embed turns text into unit-length vectors.
//
// The production embedder is the Python Sentence Transformers service
// (vectorforge/ml); Client talks to it over HTTP and re-validates every
// vector it returns. Hash is a deterministic lexical embedder for tests.
package embed

import (
	"context"
	"errors"
	"fmt"

	"byodb/vectorforge/vector"
)

// Kind tells asymmetric models whether text is a query or a document.
type Kind string

const (
	KindQuery    Kind = "query"
	KindDocument Kind = "document"
)

// Info identifies the embedding space.
type Info struct {
	ModelID      string `json:"model_id"`
	Dimension    int    `json:"dimension"`
	MaxSeqLength int    `json:"max_seq_length"`
	MaxBatch     int    `json:"max_batch"`
	MaxTextChars int    `json:"max_text_chars"`
}

// Result holds one vector per input text, in input order.
type Result struct {
	Vectors   [][]float32
	Tokens    []int
	Truncated []bool
}

// Embedder is implemented by Client and Hash.
type Embedder interface {
	Info(ctx context.Context) (Info, error)
	Embed(ctx context.Context, texts []string, kind Kind) (Result, error)
}

var (
	ErrModelChanged = errors.New("embedding model changed")
	ErrBadResponse  = errors.New("invalid embedding response")
)

// normalizeAll checks every vector and rescales it to exactly unit length in
// float64. The service already normalizes; a vector far from unit length
// means the service is misconfigured, so it is rejected rather than fixed.
func normalizeAll(vectors [][]float32, dim int) error {
	for i, v := range vectors {
		if err := vector.Validate(v, dim); err != nil {
			return fmt.Errorf("%w: vector %d: %v", ErrBadResponse, i, err)
		}
		if !vector.IsUnit(v) {
			return fmt.Errorf("%w: vector %d has norm %.6f; the service must return normalized embeddings",
				ErrBadResponse, i, vector.Norm(v))
		}
		if _, err := vector.Normalize(v, v); err != nil {
			return fmt.Errorf("%w: vector %d: %v", ErrBadResponse, i, err)
		}
	}
	return nil
}
