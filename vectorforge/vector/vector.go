// Package vector implements the dense float32 vector arithmetic used by
// VectorForge: similarity functions, normalization, validation, a compact
// byte codec, and bounded top-k selection.
//
// All collection vectors are L2-normalized before they are stored, so the
// dot product of two stored vectors equals their cosine similarity. Hot-path
// functions (Dot, L2Squared) assume validated, equal-length inputs and panic
// on a length mismatch, like an out-of-range slice index. The checked
// functions (Cosine, Validate, Normalize) return errors for untrusted input.
package vector

import (
	"errors"
	"fmt"
	"math"
)

var (
	ErrEmpty             = errors.New("vector is empty")
	ErrDimensionMismatch = errors.New("vector dimension mismatch")
	ErrNonFinite         = errors.New("vector contains NaN or infinite values")
	ErrZeroVector        = errors.New("vector has zero magnitude")
)

// UnitTolerance is the maximum accepted deviation of ||v|| from 1 for a vector
// that claims to be normalized. float32 rounding over a few hundred dimensions
// stays far below this bound.
const UnitTolerance = 1e-3

// Dot returns sum(a[i]*b[i]). Four independent accumulators break the
// loop-carried dependency so the CPU can overlap multiplications; the final
// pairwise sum keeps rounding error comparable to a sequential loop.
func Dot(a, b []float32) float32 {
	if len(a) != len(b) {
		panic(fmt.Sprintf("vector.Dot: length mismatch %d != %d", len(a), len(b)))
	}
	var s0, s1, s2, s3 float32
	i := 0
	n := len(a) &^ 7
	for ; i < n; i += 8 {
		x := a[i : i+8 : i+8] // fixed-length re-slices let the compiler drop bounds checks
		y := b[i : i+8 : i+8]
		s0 += x[0]*y[0] + x[4]*y[4]
		s1 += x[1]*y[1] + x[5]*y[5]
		s2 += x[2]*y[2] + x[6]*y[6]
		s3 += x[3]*y[3] + x[7]*y[7]
	}
	for ; i < len(a); i++ {
		s0 += a[i] * b[i]
	}
	return (s0 + s1) + (s2 + s3)
}

// L2Squared returns the squared Euclidean distance sum((a[i]-b[i])^2). For
// unit vectors it is 2 - 2*cos(a, b), so it ranks neighbours identically to
// cosine similarity; it is kept for diagnostics and tests of that identity.
func L2Squared(a, b []float32) float32 {
	if len(a) != len(b) {
		panic(fmt.Sprintf("vector.L2Squared: length mismatch %d != %d", len(a), len(b)))
	}
	var s0, s1, s2, s3 float32
	i := 0
	n := len(a) &^ 3
	for ; i < n; i += 4 {
		x := a[i : i+4 : i+4]
		y := b[i : i+4 : i+4]
		d0, d1, d2, d3 := x[0]-y[0], x[1]-y[1], x[2]-y[2], x[3]-y[3]
		s0 += d0 * d0
		s1 += d1 * d1
		s2 += d2 * d2
		s3 += d3 * d3
	}
	for ; i < len(a); i++ {
		d := a[i] - b[i]
		s0 += d * d
	}
	return (s0 + s1) + (s2 + s3)
}

// Norm returns the Euclidean length of v, accumulated in float64 so that a
// normalization check is not dominated by float32 summation error.
func Norm(v []float32) float64 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return math.Sqrt(sum)
}

// Validate checks that v is non-empty, has dimension dim (when dim > 0), and
// contains only finite values with a non-zero magnitude.
func Validate(v []float32, dim int) error {
	if len(v) == 0 {
		return ErrEmpty
	}
	if dim > 0 && len(v) != dim {
		return fmt.Errorf("%w: got %d, want %d", ErrDimensionMismatch, len(v), dim)
	}
	// Squares of finite float32 values cannot overflow a float64 sum.
	var sum float64
	for _, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return ErrNonFinite
		}
		sum += f * f
	}
	if sum == 0 {
		return ErrZeroVector
	}
	return nil
}

// Normalize writes v/||v|| into dst (which may alias v) and returns it. It
// rejects vectors that cannot be meaningfully normalized.
func Normalize(dst, v []float32) ([]float32, error) {
	if err := Validate(v, 0); err != nil {
		return nil, err
	}
	norm := Norm(v)
	if cap(dst) < len(v) {
		dst = make([]float32, len(v))
	}
	dst = dst[:len(v)]
	inv := 1 / norm
	for i, x := range v {
		dst[i] = float32(float64(x) * inv)
	}
	return dst, nil
}

// IsUnit reports whether v has length 1 within UnitTolerance.
func IsUnit(v []float32) bool {
	return math.Abs(Norm(v)-1) <= UnitTolerance
}

// Cosine returns the cosine similarity of two arbitrary (not necessarily
// normalized) vectors. It is the checked reference used by tests and input
// validation; search paths use Dot on pre-normalized vectors instead.
func Cosine(a, b []float32) (float32, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("%w: %d != %d", ErrDimensionMismatch, len(a), len(b))
	}
	if err := Validate(a, 0); err != nil {
		return 0, err
	}
	if err := Validate(b, 0); err != nil {
		return 0, err
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	c := dot / (Norm(a) * Norm(b))
	// Rounding can push identical or opposite vectors marginally outside [-1, 1].
	return float32(math.Max(-1, math.Min(1, c))), nil
}
