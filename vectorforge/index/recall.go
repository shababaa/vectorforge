package index

import "byodb/vectorforge/vector"

// RecallAtK is |exact top-k ∩ approximate top-k| / min(k, |exact|): the
// fraction of the true k nearest neighbours that the approximate search
// returned. It measures index fidelity only, not whether results are
// relevant to a human; relevance metrics are computed against labels.
func RecallAtK(exact, approx []vector.Hit, k int) float64 {
	n := min(k, len(exact))
	if n == 0 {
		return 1
	}
	want := make(map[int64]struct{}, n)
	for _, h := range exact[:n] {
		want[h.ID] = struct{}{}
	}
	found := 0
	for _, h := range approx[:min(k, len(approx))] {
		if _, ok := want[h.ID]; ok {
			found++
		}
	}
	return float64(found) / float64(n)
}
