package vector

import "sort"

// Hit is one scored candidate.
type Hit struct {
	ID    int64   `json:"id"`
	Score float32 `json:"score"`
}

// better reports whether a ranks ahead of b: higher score first, then lower
// ID. The ID tie-break makes exact and approximate results comparable even
// when duplicate vectors have identical scores.
func better(a, b Hit) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.ID < b.ID
}

// TopK keeps the k best hits seen so far in a binary min-heap whose root is
// the worst retained hit. Each Push is O(1) when the candidate cannot enter
// the heap and O(log k) otherwise, so selecting from n candidates is
// O(n log k) instead of the O(n log n) of sorting everything.
type TopK struct {
	k    int
	heap []Hit
}

// NewTopK returns an empty selector for k results. k <= 0 retains nothing.
func NewTopK(k int) *TopK {
	t := &TopK{}
	t.Reset(k)
	return t
}

// Reset empties the selector and sets a new k, reusing its storage.
func (t *TopK) Reset(k int) {
	t.k = max(k, 0)
	if cap(t.heap) < t.k {
		t.heap = make([]Hit, 0, t.k)
	}
	t.heap = t.heap[:0]
}

// Len returns the number of retained hits.
func (t *TopK) Len() int { return len(t.heap) }

// Full reports whether k hits are retained.
func (t *TopK) Full() bool { return len(t.heap) >= t.k }

// Worst returns the lowest-ranked retained hit; ok is false when empty.
func (t *TopK) Worst() (Hit, bool) {
	if len(t.heap) == 0 {
		return Hit{}, false
	}
	return t.heap[0], true
}

// Push offers a candidate.
func (t *TopK) Push(id int64, score float32) {
	h := Hit{ID: id, Score: score}
	if len(t.heap) < t.k {
		t.heap = append(t.heap, h)
		t.up(len(t.heap) - 1)
		return
	}
	if t.k == 0 || !better(h, t.heap[0]) {
		return
	}
	t.heap[0] = h
	t.down(0)
}

// Results returns the retained hits ordered best first. The selector remains
// usable; the returned slice is a copy.
func (t *TopK) Results() []Hit {
	out := append([]Hit(nil), t.heap...)
	sort.Slice(out, func(i, j int) bool { return better(out[i], out[j]) })
	return out
}

func (t *TopK) up(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if !better(t.heap[parent], t.heap[i]) { // parent must be the worse one
			return
		}
		t.heap[parent], t.heap[i] = t.heap[i], t.heap[parent]
		i = parent
	}
}

func (t *TopK) down(i int) {
	n := len(t.heap)
	for {
		worst := i
		left, right := 2*i+1, 2*i+2
		if left < n && better(t.heap[worst], t.heap[left]) {
			worst = left
		}
		if right < n && better(t.heap[worst], t.heap[right]) {
			worst = right
		}
		if worst == i {
			return
		}
		t.heap[i], t.heap[worst] = t.heap[worst], t.heap[i]
		i = worst
	}
}
