package main

import "testing"

func TestPageAfterStable(t *testing.T) {
	photos := make([]photo, 0, 30)
	for i := int64(1); i <= 30; i++ {
		photos = append(photos, photo{ID: i, Width: 10, Height: 10})
	}
	const seed int64 = 42
	sortByRank(photos, seed)
	first := pageAfter(photos, seed, 0, 0, 10)
	if len(first) != 10 {
		t.Fatalf("first page %d", len(first))
	}
	rank, id := parseCursor(formatCursor(seed, first[len(first)-1]))
	second := pageAfter(photos, seed, rank, id, 10)
	if len(second) != 10 {
		t.Fatalf("second page %d", len(second))
	}
	seen := map[int64]bool{}
	for _, p := range append(append([]photo{}, first...), second...) {
		if seen[p.ID] {
			t.Fatalf("duplicate %d", p.ID)
		}
		seen[p.ID] = true
	}
	again := pageAfter(func() []photo {
		cp := append([]photo{}, photos...)
		sortByRank(cp, seed)
		return cp
	}(), seed, 0, 0, 10)
	if again[0].ID != first[0].ID || again[9].ID != first[9].ID {
		t.Fatal("same seed should keep order")
	}
	cp := append([]photo{}, photos...)
	sortByRank(cp, 99)
	other := pageAfter(cp, 99, 0, 0, 10)
	same := 0
	for i := range first {
		if first[i].ID == other[i].ID {
			same++
		}
	}
	if same == len(first) {
		t.Fatal("different seed should reshuffle")
	}
}
