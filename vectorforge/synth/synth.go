// Package synth generates reproducible synthetic unit vectors for index tests
// and stress benchmarks. Synthetic vectors measure index behaviour only; they
// are never used as evidence of semantic retrieval quality.
package synth

import (
	"math"
	"math/rand"
)

// Clustered returns n unit vectors (row-major) drawn from a mixture of
// `clusters` Gaussian blobs around random unit centres. spread is the
// expected norm of the noise added to a centre before re-normalization: a
// point's expected cosine to its centre is about 1/sqrt(1+spread^2), so
// spread=1 gives ~0.71, similar to the neighbourhood structure of sentence
// embeddings. clusters <= 0 produces isotropic (structureless) data.
func Clustered(n, dim, clusters int, spread float64, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	centres := make([][]float64, max(clusters, 0))
	for i := range centres {
		centres[i] = randomUnit(r, dim)
	}
	return sample(r, centres, n, dim, spread)
}

// Queries draws n fresh points from the same mixture as Clustered with the
// same parameters and seed, without reproducing the dataset points.
func Queries(n, dim, clusters int, spread float64, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	centres := make([][]float64, max(clusters, 0))
	for i := range centres {
		centres[i] = randomUnit(r, dim)
	}
	q := rand.New(rand.NewSource(seed ^ 0x5eed5eed))
	return sample(q, centres, n, dim, spread)
}

func sample(r *rand.Rand, centres [][]float64, n, dim int, spread float64) []float32 {
	out := make([]float32, n*dim)
	scale := spread / math.Sqrt(float64(dim))
	point := make([]float64, dim)
	for i := 0; i < n; i++ {
		if len(centres) == 0 {
			copy(point, randomUnit(r, dim))
		} else {
			c := centres[r.Intn(len(centres))]
			for j := range point {
				point[j] = c[j] + scale*r.NormFloat64()
			}
		}
		var norm float64
		for _, x := range point {
			norm += x * x
		}
		inv := 1 / math.Sqrt(norm)
		row := out[i*dim : (i+1)*dim]
		for j, x := range point {
			row[j] = float32(x * inv)
		}
	}
	return out
}

func randomUnit(r *rand.Rand, dim int) []float64 {
	v := make([]float64, dim)
	var norm float64
	for i := range v {
		v[i] = r.NormFloat64()
		norm += v[i] * v[i]
	}
	inv := 1 / math.Sqrt(norm)
	for i := range v {
		v[i] *= inv
	}
	return v
}
