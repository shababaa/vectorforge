package vector

import (
	"errors"
	"math"
	"math/rand"
	"sort"
	"testing"
)

func dotReference(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

func randomVector(r *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(r.NormFloat64())
	}
	return v
}

func TestDotMatchesFloat64Reference(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	// Odd lengths exercise the scalar tail after the 8-wide loop.
	for _, dim := range []int{1, 3, 7, 8, 9, 31, 128, 383, 384, 768} {
		a, b := randomVector(r, dim), randomVector(r, dim)
		got, want := float64(Dot(a, b)), dotReference(a, b)
		if math.Abs(got-want) > 1e-4*math.Max(1, math.Abs(want)) {
			t.Fatalf("dim %d: Dot=%v, reference=%v", dim, got, want)
		}
	}
}

func TestDotPanicsOnLengthMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Dot accepted mismatched lengths")
		}
	}()
	Dot([]float32{1, 2}, []float32{1})
}

func TestCosineGeometry(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float32
	}{
		{"identical", []float32{1, 2, 3}, []float32{1, 2, 3}, 1},
		{"scaled identical", []float32{1, 2, 3}, []float32{10, 20, 30}, 1},
		{"orthogonal", []float32{1, 0, 0}, []float32{0, 1, 0}, 0},
		{"opposite", []float32{1, -2, 3}, []float32{-1, 2, -3}, -1},
	}
	for _, test := range tests {
		got, err := Cosine(test.a, test.b)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if math.Abs(float64(got-test.want)) > 1e-6 {
			t.Fatalf("%s: cosine=%v, want %v", test.name, got, test.want)
		}
	}
}

func TestCosineRejectsInvalidInput(t *testing.T) {
	nan := float32(math.NaN())
	inf := float32(math.Inf(1))
	cases := []struct {
		name string
		a, b []float32
		want error
	}{
		{"zero vector", []float32{0, 0, 0}, []float32{1, 0, 0}, ErrZeroVector},
		{"dimension mismatch", []float32{1, 0}, []float32{1, 0, 0}, ErrDimensionMismatch},
		{"NaN", []float32{nan, 1}, []float32{1, 1}, ErrNonFinite},
		{"Inf", []float32{1, 1}, []float32{inf, 1}, ErrNonFinite},
		{"empty", nil, nil, ErrEmpty},
	}
	for _, c := range cases {
		if _, err := Cosine(c.a, c.b); !errors.Is(err, c.want) {
			t.Fatalf("%s: err=%v, want %v", c.name, err, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	if err := Validate([]float32{1, 2}, 3); !errors.Is(err, ErrDimensionMismatch) {
		t.Fatalf("dimension: %v", err)
	}
	if err := Validate([]float32{0, 0}, 2); !errors.Is(err, ErrZeroVector) {
		t.Fatalf("zero: %v", err)
	}
	if err := Validate([]float32{float32(math.Inf(-1)), 0}, 2); !errors.Is(err, ErrNonFinite) {
		t.Fatalf("inf: %v", err)
	}
	// The largest finite float32 must not be reported as an overflow.
	if err := Validate([]float32{math.MaxFloat32, math.MaxFloat32}, 2); err != nil {
		t.Fatalf("max float32: %v", err)
	}
	if err := Validate([]float32{1e-30, 0}, 0); err != nil {
		t.Fatalf("tiny but non-zero: %v", err)
	}
}

func TestNormalize(t *testing.T) {
	v := []float32{3, 4}
	out, err := Normalize(nil, v)
	if err != nil {
		t.Fatal(err)
	}
	if out[0] != 0.6 || out[1] != 0.8 || !IsUnit(out) {
		t.Fatalf("normalize(3,4)=%v", out)
	}
	if v[0] != 3 {
		t.Fatal("Normalize modified its input when dst was nil")
	}
	inPlace, err := Normalize(v, v)
	if err != nil || &inPlace[0] != &v[0] || !IsUnit(v) {
		t.Fatalf("in-place normalize: %v %v", inPlace, err)
	}
	// Very large components would overflow a float32 sum of squares.
	big, err := Normalize(nil, []float32{math.MaxFloat32, math.MaxFloat32})
	if err != nil || !IsUnit(big) {
		t.Fatalf("large normalize: %v %v", big, err)
	}
	if _, err := Normalize(nil, []float32{0, 0}); !errors.Is(err, ErrZeroVector) {
		t.Fatalf("zero: %v", err)
	}
}

func TestDotEqualsCosineForUnitVectors(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for range 50 {
		a, _ := Normalize(nil, randomVector(r, 384))
		b, _ := Normalize(nil, randomVector(r, 384))
		c, err := Cosine(a, b)
		if err != nil {
			t.Fatal(err)
		}
		if d := Dot(a, b); math.Abs(float64(d-c)) > 1e-5 {
			t.Fatalf("dot=%v cosine=%v", d, c)
		}
		// ||a-b||^2 = 2 - 2cos for unit vectors: Euclidean and cosine ranking agree.
		if l2 := L2Squared(a, b); math.Abs(float64(l2-(2-2*c))) > 1e-4 {
			t.Fatalf("l2^2=%v, 2-2cos=%v", l2, 2-2*c)
		}
	}
}

func TestL2Squared(t *testing.T) {
	if got := L2Squared([]float32{1, 2, 3, 4, 5}, []float32{1, 2, 3, 4, 5}); got != 0 {
		t.Fatalf("identical distance=%v", got)
	}
	if got := L2Squared([]float32{0, 0, 0, 0, 0}, []float32{1, 2, 0, 0, 2}); got != 9 {
		t.Fatalf("distance^2=%v, want 9", got)
	}
}

func TestCodecRoundTrip(t *testing.T) {
	negativeZero := float32(math.Copysign(0, -1))
	in := []float32{0, negativeZero, 1, -1, math.SmallestNonzeroFloat32, math.MaxFloat32, 3.14159}
	encoded := Encode(nil, in)
	if len(encoded) != 4*len(in) {
		t.Fatalf("encoded length %d", len(encoded))
	}
	out, err := Decode(nil, encoded)
	if err != nil {
		t.Fatal(err)
	}
	for i := range in {
		if math.Float32bits(in[i]) != math.Float32bits(out[i]) {
			t.Fatalf("value %d: %v != %v", i, in[i], out[i])
		}
	}
	if _, err := Decode(nil, encoded[:5]); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("truncated decode error: %v", err)
	}
}

func TestSegments(t *testing.T) {
	if SegmentCount(384) != 2 || SegmentCount(256) != 1 || SegmentCount(257) != 2 {
		t.Fatal("unexpected segment counts")
	}
	v := make([]float32, 384)
	for i := range v {
		v[i] = float32(i)
	}
	first, second := Segment(v, 0), Segment(v, 1)
	if len(first) != 256 || len(second) != 128 || second[0] != 256 {
		t.Fatalf("segments %d %d", len(first), len(second))
	}
}

func TestTopKMatchesSort(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for _, k := range []int{1, 5, 10, 100, 1000} {
		hits := make([]Hit, 500)
		top := NewTopK(k)
		for i := range hits {
			// Coarse scores create many ties that the ID tie-break must order.
			hits[i] = Hit{ID: int64(r.Intn(1_000_000)), Score: float32(r.Intn(20))}
			top.Push(hits[i].ID, hits[i].Score)
		}
		sort.Slice(hits, func(i, j int) bool { return better(hits[i], hits[j]) })
		want := hits[:min(k, len(hits))]
		got := top.Results()
		if len(got) != len(want) {
			t.Fatalf("k=%d: got %d results", k, len(got))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("k=%d rank %d: got %+v, want %+v", k, i, got[i], want[i])
			}
		}
	}
}

func TestTopKEdgeCases(t *testing.T) {
	zero := NewTopK(0)
	zero.Push(1, 1)
	if zero.Len() != 0 {
		t.Fatal("k=0 retained a hit")
	}
	top := NewTopK(2)
	if _, ok := top.Worst(); ok {
		t.Fatal("empty selector reported a worst hit")
	}
	top.Push(1, 0.5)
	top.Push(2, 0.9)
	top.Push(3, 0.1) // cannot enter
	top.Push(4, 0.7) // evicts 0.5
	got := top.Results()
	if len(got) != 2 || got[0].ID != 2 || got[1].ID != 4 {
		t.Fatalf("results %+v", got)
	}
	top.Reset(1)
	if top.Len() != 0 || top.Full() {
		t.Fatal("reset did not empty the selector")
	}
}

func dotNaive(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

var sink float32

func BenchmarkDot384(b *testing.B) {
	r := rand.New(rand.NewSource(4))
	x, y := randomVector(r, 384), randomVector(r, 384)
	b.SetBytes(2 * 4 * 384)
	for i := 0; i < b.N; i++ {
		sink = Dot(x, y)
	}
}

func BenchmarkDotNaive384(b *testing.B) {
	r := rand.New(rand.NewSource(4))
	x, y := randomVector(r, 384), randomVector(r, 384)
	b.SetBytes(2 * 4 * 384)
	for i := 0; i < b.N; i++ {
		sink = dotNaive(x, y)
	}
}

func BenchmarkTopKPush(b *testing.B) {
	r := rand.New(rand.NewSource(5))
	scores := make([]float32, 4096)
	for i := range scores {
		scores[i] = r.Float32()
	}
	top := NewTopK(10)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		top.Reset(10)
		for id, s := range scores {
			top.Push(int64(id), s)
		}
	}
}
