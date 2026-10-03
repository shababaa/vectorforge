package index

import (
	"errors"
	"fmt"
	"math"
	"math/rand"

	"byodb/vectorforge/vector"
)

// KMeansConfig configures spherical k-means training.
type KMeansConfig struct {
	K             int
	MaxIterations int     // default 25
	Seed          int64   // all randomness derives from this seed
	Tolerance     float64 // stop when the relative objective gain falls below this; default 1e-4
	// MaxPointsPerCentroid caps the training sample at K*MaxPointsPerCentroid
	// points (default 256). Centroid quality saturates well before every
	// point is used, while training cost is linear in the sample size.
	MaxPointsPerCentroid int
	Workers              int // assignment goroutines; default GOMAXPROCS
}

// KMeansResult reports trained centroids and convergence diagnostics.
type KMeansResult struct {
	Centroids    []float32 `json:"-"`
	Iterations   int       `json:"iterations"`
	Converged    bool      `json:"converged"`
	Objective    float64   `json:"objective"` // mean cosine of sample points to their centroid
	History      []float64 `json:"objective_history"`
	SampleSize   int       `json:"sample_size"`
	EmptyReseeds int       `json:"empty_reseeds"`
}

func (c *KMeansConfig) withDefaults() KMeansConfig {
	out := *c
	if out.MaxIterations == 0 {
		out.MaxIterations = 25
	}
	if out.Tolerance == 0 {
		out.Tolerance = 1e-4
	}
	if out.MaxPointsPerCentroid == 0 {
		out.MaxPointsPerCentroid = 256
	}
	return out
}

// TrainKMeans clusters unit vectors (row-major, n*dim values) into K unit
// centroids that maximize the mean cosine similarity between each point and
// its assigned centroid.
//
// Spherical k-means is ordinary Lloyd iteration with one change: after
// averaging a cluster's points, the centroid is projected back onto the unit
// sphere. For unit vectors ||x-c||^2 = 2 - 2(x·c), so maximizing dot product
// is minimizing Euclidean distance, and the index's training, assignment and
// query scoring all use the same similarity.
func TrainKMeans(data []float32, dim int, cfg KMeansConfig) (KMeansResult, error) {
	cfg = cfg.withDefaults()
	if dim <= 0 || len(data)%dim != 0 {
		return KMeansResult{}, fmt.Errorf("k-means: data length %d is not a multiple of dimension %d", len(data), dim)
	}
	n := len(data) / dim
	if cfg.K < 1 {
		return KMeansResult{}, errors.New("k-means: K must be at least 1")
	}
	if n < cfg.K {
		return KMeansResult{}, fmt.Errorf("k-means: %d points cannot train %d centroids", n, cfg.K)
	}
	if cfg.MaxIterations < 1 || cfg.MaxPointsPerCentroid < 1 || cfg.Tolerance < 0 {
		return KMeansResult{}, errors.New("k-means: invalid iteration, sample or tolerance setting")
	}
	rng := rand.New(rand.NewSource(cfg.Seed))
	points := sampleRows(data, dim, n, cfg.K*cfg.MaxPointsPerCentroid, rng)
	m := len(points) / dim

	centroids := seedPlusPlus(points, dim, cfg.K, rng, cfg.Workers)
	assign := make([]int32, m)
	sims := make([]float32, m)
	counts := make([]int, cfg.K)
	sums := make([]float64, cfg.K*dim)
	result := KMeansResult{SampleSize: m}
	prev := math.Inf(-1)
	for iter := 1; iter <= cfg.MaxIterations; iter++ {
		changed := assignNearest(points, dim, centroids, assign, sims, cfg.Workers, iter == 1)
		objective := meanFloat32(sims)
		result.History = append(result.History, objective)
		result.Iterations, result.Objective = iter, objective

		clear(counts)
		for _, c := range assign {
			counts[c]++
		}
		result.EmptyReseeds += reseedEmpty(assign, sims, counts)
		updateCentroids(points, dim, assign, centroids, sums)

		if changed == 0 || (iter > 1 && objective-prev <= cfg.Tolerance*math.Abs(prev)) {
			result.Converged = true
			break
		}
		prev = objective
	}
	result.Centroids = centroids
	return result, nil
}

// sampleRows returns at most limit rows chosen by a seeded permutation. When
// no sampling is needed it returns data itself.
func sampleRows(data []float32, dim, n, limit int, rng *rand.Rand) []float32 {
	if n <= limit {
		return data
	}
	perm := rng.Perm(n)[:limit]
	out := make([]float32, 0, limit*dim)
	for _, row := range perm {
		out = append(out, data[row*dim:(row+1)*dim]...)
	}
	return out
}

// seedPlusPlus implements k-means++ seeding: each new centroid is a data
// point drawn with probability proportional to its squared distance from the
// nearest centroid chosen so far, which spreads the initial centroids out and
// gives an O(log K)-competitive starting objective in expectation.
func seedPlusPlus(points []float32, dim, k int, rng *rand.Rand, workers int) []float32 {
	m := len(points) / dim
	centroids := make([]float32, 0, k*dim)
	first := rng.Intn(m)
	centroids = append(centroids, points[first*dim:(first+1)*dim]...)
	best := make([]float32, m)
	parallelRange(m, workers, func(start, end int) {
		c := centroids[:dim]
		for i := start; i < end; i++ {
			best[i] = vector.Dot(points[i*dim:(i+1)*dim], c)
		}
	})
	for len(centroids) < k*dim {
		// ||x-c||^2 = 2(1 - x·c) for unit vectors; the constant factor cancels.
		var total float64
		for _, s := range best {
			total += math.Max(0, 1-float64(s))
		}
		pick := 0
		if total <= 0 {
			pick = rng.Intn(m) // every point coincides with a centroid
		} else {
			target := rng.Float64() * total
			for i, s := range best {
				target -= math.Max(0, 1-float64(s))
				if target <= 0 {
					pick = i
					break
				}
				pick = i
			}
		}
		centroids = append(centroids, points[pick*dim:(pick+1)*dim]...)
		c := centroids[len(centroids)-dim:]
		parallelRange(m, workers, func(start, end int) {
			for i := start; i < end; i++ {
				if s := vector.Dot(points[i*dim:(i+1)*dim], c); s > best[i] {
					best[i] = s
				}
			}
		})
	}
	return centroids
}

// assignNearest sets assign[i] to the highest-dot centroid of point i and
// returns how many assignments changed. Ties choose the lower centroid index.
func assignNearest(points []float32, dim int, centroids []float32, assign []int32, sims []float32, workers int, first bool) int {
	m := len(points) / dim
	changedBy := make([]int, m)
	parallelRange(m, workers, func(start, end int) {
		for i := start; i < end; i++ {
			c, s := nearest(points[i*dim:(i+1)*dim], centroids, dim)
			if first || assign[i] != int32(c) {
				changedBy[i] = 1
			}
			assign[i], sims[i] = int32(c), s
		}
	})
	changed := 0
	for _, c := range changedBy {
		changed += c
	}
	return changed
}

func nearest(v, centroids []float32, dim int) (int, float32) {
	best, bestScore := 0, float32(math.Inf(-1))
	for c := 0; c*dim < len(centroids); c++ {
		if s := vector.Dot(v, centroids[c*dim:(c+1)*dim]); s > bestScore {
			best, bestScore = c, s
		}
	}
	return best, bestScore
}

// reseedEmpty gives each empty cluster the worst-fitting point of the
// currently largest cluster. Without this, a centroid that loses all points
// would stay frozen and waste one inverted list.
func reseedEmpty(assign []int32, sims []float32, counts []int) int {
	reseeded := 0
	for empty := range counts {
		if counts[empty] != 0 {
			continue
		}
		largest := 0
		for c := range counts {
			if counts[c] > counts[largest] {
				largest = c
			}
		}
		if counts[largest] < 2 {
			return reseeded
		}
		worst := -1
		for i, c := range assign {
			if int(c) == largest && (worst < 0 || sims[i] < sims[worst]) {
				worst = i
			}
		}
		assign[worst], sims[worst] = int32(empty), 1
		counts[largest]--
		counts[empty]++
		reseeded++
	}
	return reseeded
}

// updateCentroids sets each centroid to the normalized mean of its points.
// Accumulation is sequential in point order with float64 sums, so results do
// not depend on goroutine scheduling or worker count.
func updateCentroids(points []float32, dim int, assign []int32, centroids []float32, sums []float64) {
	clear(sums)
	for i, c := range assign {
		row := points[i*dim : (i+1)*dim]
		acc := sums[int(c)*dim : (int(c)+1)*dim]
		for j, x := range row {
			acc[j] += float64(x)
		}
	}
	k := len(centroids) / dim
	for c := 0; c < k; c++ {
		acc := sums[c*dim : (c+1)*dim]
		var norm float64
		for _, x := range acc {
			norm += x * x
		}
		if norm == 0 {
			continue // keep the previous centroid; points cancelled exactly
		}
		inv := 1 / math.Sqrt(norm)
		dst := centroids[c*dim : (c+1)*dim]
		for j, x := range acc {
			dst[j] = float32(x * inv)
		}
	}
}

func meanFloat32(values []float32) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += float64(v)
	}
	return sum / float64(len(values))
}
