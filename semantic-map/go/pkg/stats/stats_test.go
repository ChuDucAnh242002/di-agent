package stats

import (
	"math"
	"testing"
)

func TestPearsonPerfectPositive(t *testing.T) {
	xs := []float64{0, 1, 2, 3, 4}
	ys := []float64{0, 2, 4, 6, 8}
	r, ok := Pearson(xs, ys)
	if !ok || math.Abs(r-1) > 1e-12 {
		t.Fatalf("r=%.6f ok=%v; want 1, true", r, ok)
	}
}

func TestPearsonUndefinedOnConstantOrShort(t *testing.T) {
	if _, ok := Pearson([]float64{1, 1, 1}, []float64{1, 2, 3}); ok {
		t.Error("constant series must be undefined, not r=0")
	}
	if _, ok := Pearson([]float64{1, 2}, []float64{1, 2}); ok {
		t.Error("fewer than three points must be undefined")
	}
}

func TestFisherPValue(t *testing.T) {
	if p := FisherPValue(0.9, 3); p != 1.0 {
		t.Errorf("n<4 must return 1.0, got %v", p)
	}
	if p := FisherPValue(0.95, 100); p >= 0.001 {
		t.Errorf("strong correlation over 100 points must be significant, got %v", p)
	}
}

func TestPairWindowDedupsAndBounds(t *testing.T) {
	w := NewPairWindow(3)
	if !w.Fold("a", 1, 2) || w.Fold("a", 1, 2) {
		t.Fatal("first fold of an identity must succeed and the second must be rejected")
	}
	w.Fold("b", 2, 4)
	w.Fold("c", 3, 6)
	w.Fold("d", 4, 8) // evicts the oldest; capacity 3
	if w.Len() != 3 {
		t.Fatalf("len=%d, want 3", w.Len())
	}
	r, ok := w.Pearson()
	if !ok || math.Abs(r-1) > 1e-12 {
		t.Fatalf("r=%.6f ok=%v; want 1, true", r, ok)
	}
}

func TestPairWindowReplaceLast(t *testing.T) {
	w := NewPairWindow(3)
	if !w.ReplaceLast("a", 1, 1) || w.Len() != 1 {
		t.Fatalf("ReplaceLast on an empty window must fold; len=%d", w.Len())
	}
	for i, id := range []string{"b", "c", "d"} { // the ring wraps: d overwrote a
		w.Fold(id, float64(i+2), float64(i+2))
	}
	if !w.ReplaceLast("d2", 9, 8) {
		t.Fatal("ReplaceLast refused a fresh identity")
	}
	if w.Len() != 3 || w.xs[0] != 9 || w.ys[0] != 8 {
		t.Errorf("after the wrap the last fold sits at slot 0; got xs=%v ys=%v", w.xs, w.ys)
	}
	if w.ReplaceLast("b", 0, 0) {
		t.Error("ReplaceLast must refuse an identity already folded")
	}
}

func TestDistinctCountsTheEvidenceInAMostlyFlatWindow(t *testing.T) {
	w := NewPairWindow(10)
	for i := 0; i < 10; i++ {
		x, y := 0.0, float64(i)
		if i == 3 || i == 7 {
			x = 5
		}
		w.Fold("", x, y)
	}
	if x, y := w.Distinct(); x != 3 || y != 10 {
		t.Errorf("distinct %d, %d; want 3 for two spikes off zero and 10 for ten values", x, y)
	}
}
