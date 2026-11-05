// Package stats holds the one Pearson estimator, the one Fisher p-value and the one
// paired-observation window the map and the proposer share. Two copies of an
// estimator are two chances for the same pair of readings to be scored differently.
package stats

import (
	"fmt"
	"math"
	"slices"
)

// PairWindow is a fixed-capacity ring of paired observations with a dedup set keyed
// by pair identity, so a replayed batch cannot fold the same physical pair twice.
type PairWindow struct {
	xs, ys   []float64
	next     int
	capacity int
	seen     map[string]struct{}
}

// NewPairWindow builds a ring of the given capacity; 0 or less is unbounded. The
// capacity belongs to the window, not to each Fold call: the ring's invariants
// (len ≤ capacity, next < capacity) hold only if every fold agrees on it.
func NewPairWindow(capacity int) *PairWindow {
	return &PairWindow{capacity: capacity, seen: make(map[string]struct{})}
}

// Fold adds (x, y) under identity unless that identity was already folded. The
// dedup set is trimmed when it exceeds 4*capacity, because an identity that can no
// longer be in the window cannot be a duplicate of anything in it. Returns false
// when the pair was a duplicate. An empty identity skips the check, for a caller
// that refuses repeats itself: the set is most of a window's memory.
func (w *PairWindow) Fold(identity string, x, y float64) bool {
	if !w.remember(identity) {
		return false
	}
	if w.capacity <= 0 || len(w.xs) < w.capacity {
		w.xs = append(w.xs, x)
		w.ys = append(w.ys, y)
		return true
	}
	w.xs[w.next] = x
	w.ys[w.next] = y
	w.next = (w.next + 1) % w.capacity
	return true
}

// ReplaceLast overwrites the most recently folded pair with (x, y) under identity,
// for a caller that has found a better partner for one of its readings. It folds
// instead when the window is empty, and returns false when identity was already
// folded.
func (w *PairWindow) ReplaceLast(identity string, x, y float64) bool {
	if len(w.xs) == 0 {
		return w.Fold(identity, x, y)
	}
	if !w.remember(identity) {
		return false
	}
	last := len(w.xs) - 1
	if w.capacity > 0 && len(w.xs) == w.capacity {
		last = (w.next - 1 + w.capacity) % w.capacity
	}
	w.xs[last], w.ys[last] = x, y
	return true
}

// remember records identity, and reports false when it was already folded.
func (w *PairWindow) remember(identity string) bool {
	if identity == "" {
		return true
	}
	if _, dup := w.seen[identity]; dup {
		return false
	}
	w.seen[identity] = struct{}{}
	if w.capacity > 0 && len(w.seen) > 4*w.capacity {
		w.seen = map[string]struct{}{identity: {}}
	}
	return true
}

func (w *PairWindow) Len() int { return len(w.xs) }

// WindowSnapshot preserves ring order and duplicate identities together.
type WindowSnapshot struct {
	X, Y           []float64
	Next, Capacity int
	Seen           []string
}

func (w *PairWindow) Snapshot() WindowSnapshot {
	s := WindowSnapshot{X: slices.Clone(w.xs), Y: slices.Clone(w.ys), Next: w.next, Capacity: w.capacity}
	for id := range w.seen {
		s.Seen = append(s.Seen, id)
	}
	slices.Sort(s.Seen)
	return s
}

func RestoreWindow(s WindowSnapshot) (*PairWindow, error) {
	if len(s.X) != len(s.Y) || s.Capacity <= 0 || len(s.X) > s.Capacity ||
		s.Next < 0 || s.Next >= s.Capacity || len(s.X) < s.Capacity && s.Next != 0 ||
		len(s.Seen) > 4*s.Capacity {
		return nil, fmt.Errorf("invalid paired-observation window")
	}
	w := NewPairWindow(s.Capacity)
	for i := range s.X {
		if math.IsNaN(s.X[i]) || math.IsInf(s.X[i], 0) || math.IsNaN(s.Y[i]) || math.IsInf(s.Y[i], 0) {
			return nil, fmt.Errorf("non-finite paired observation")
		}
	}
	w.xs, w.ys, w.next = slices.Clone(s.X), slices.Clone(s.Y), s.Next
	for _, id := range s.Seen {
		w.seen[id] = struct{}{}
	}
	return w, nil
}

// Spread returns max − min of each side over the window: how far each series moved.
// Correlation is scale-free, so a series that barely moves can still correlate
// perfectly; the spread is what says whether the movement is worth anything.
func (w *PairWindow) Spread() (x, y float64) { return spread(w.xs), spread(w.ys) }

func spread(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	lo, hi := v[0], v[0]
	for _, x := range v[1:] {
		lo, hi = math.Min(lo, x), math.Max(hi, x)
	}
	return hi - lo
}

// Distinct returns, for each side, the readings that are not its commonest value plus
// one for that value: the window's length when no value repeats, k+1 for a rate that
// sits at zero and spikes k times. Pearson over a window that is mostly one value
// carries the evidence of those readings, not of its length.
func (w *PairWindow) Distinct() (x, y int) { return distinct(w.xs), distinct(w.ys) }

func distinct(v []float64) int {
	if len(v) == 0 {
		return 0
	}
	counts := make(map[float64]int, len(v))
	most := 0
	for _, x := range v {
		counts[x]++
		most = max(most, counts[x])
	}
	return len(v) - most + 1
}

// Pearson returns the correlation over the window, and false when it is undefined.
func (w *PairWindow) Pearson() (float64, bool) { return Pearson(w.xs, w.ys) }

// Pearson returns the correlation coefficient, and false when it is undefined: fewer
// than three points, mismatched lengths, or a constant series on either side. A
// constant series is the common case on a quiet system; reporting r = 0 there would
// be a claim, and "undefined" is the honest answer.
func Pearson(xs, ys []float64) (float64, bool) {
	n := len(xs)
	if n < 3 || n != len(ys) {
		return 0, false
	}
	var sx, sy float64
	for i := 0; i < n; i++ {
		sx += xs[i]
		sy += ys[i]
	}
	mx, my := sx/float64(n), sy/float64(n)
	var sxy, sxx, syy float64
	for i := 0; i < n; i++ {
		dx, dy := xs[i]-mx, ys[i]-my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx <= 1e-12 || syy <= 1e-12 {
		return 0, false
	}
	return sxy / math.Sqrt(sxx*syy), true
}

// EffectiveN is the window's effective sample size for its Pearson r, walking the
// ring in arrival order without copying it (see EffectiveN). A ring that has wrapped
// holds its oldest reading at next, so arrival order starts there.
func (w *PairWindow) EffectiveN() float64 { return effectiveN(w.xs, w.ys, w.next) }

// EffectiveN is how many independent pairs the correlation of two time-ordered series
// rests on. Telemetry is autocorrelated: a reading mostly repeats the one before, so
// n readings carry far less than n readings' evidence, and a p-value computed on n
// calls chance co-movement significant (two independent AR(1) series at φ = 0.9 pass
// p ≤ 0.05 about half the time over 120 readings). The lag-1 correction of Bartlett,
// as used by Bretherton et al. (1999, J. Climate 12:1990), is
//
//	n_eff = n · (1 − r1x·r1y) / (1 + r1x·r1y)
//
// with r1 each series' lag-1 autocorrelation. It is asymptotically exact for two
// AR(1) series — a large-sample result — and the standard first approximation
// otherwise. A constant series has r1 = 0 and leaves n as it is. The result is
// clamped to [3, n]: n because a negative product (one series alternating) would
// otherwise claim more evidence than there are readings, and 3 because Fisher's z
// needs n − 3 > 0 to say anything — at the floor the p-value is 1. Series whose
// lag-1 autocorrelations are both near 0.99 (a slow drift, two ramps) reach the floor
// within one window of 120. A window shorter than 3 keeps its length, and so do
// series of unequal length: neither has a Pearson r to correct.
func EffectiveN(xs, ys []float64) float64 { return effectiveN(xs, ys, 0) }

// effectiveN reads xs and ys in the order xs[start], …, xs[n−1], xs[0], …, xs[start−1].
func effectiveN(xs, ys []float64, start int) float64 {
	n := float64(len(xs))
	if len(xs) < 3 || len(xs) != len(ys) {
		return n
	}
	rr := lag1Product(xs, ys, start)
	return math.Min(n, math.Max(3, n*(1-rr)/(1+rr)))
}

// lag1Product is r1x·r1y, each the sample lag-1 autocorrelation
// Σ(v_t−m)(v_{t+1}−m) / Σ(v_t−m)² of its series read from start round to start−1,
// and 0 when either series is constant. Both series are walked in one pass, and the
// mean and the sum of squares do not depend on where the walk starts; only the lagged
// products do.
func lag1Product(xs, ys []float64, start int) float64 {
	var mx, my float64
	for i := range xs {
		mx += xs[i]
		my += ys[i]
	}
	n := float64(len(xs))
	mx, my = mx/n, my/n
	px, py := xs[start]-mx, ys[start]-my
	var nx, ny float64
	sx, sy := px*px, py*py
	for i := 1; i < len(xs); i++ {
		j := start + i
		if j >= len(xs) {
			j -= len(xs)
		}
		dx, dy := xs[j]-mx, ys[j]-my
		nx += px * dx
		ny += py * dy
		sx += dx * dx
		sy += dy * dy
		px, py = dx, dy
	}
	if sx <= 1e-12 || sy <= 1e-12 {
		return 0
	}
	return nx / sx * ny / sy
}

// FisherPValue is the two-tailed p-value for Pearson r using the Fisher z-transform:
// z = atanh(r)·sqrt(n−3), P = erfc(|z|/sqrt 2). n is a float so an effective sample
// size can be passed as it is (see EffectiveN). Returns 1.0 when n ≤ 3.
func FisherPValue(r float64, n float64) float64 {
	if n <= 3 {
		return 1.0
	}
	if r >= 1 {
		r = 1 - 1e-12
	}
	if r <= -1 {
		r = -1 + 1e-12
	}
	z := math.Atanh(r) * math.Sqrt(n-3)
	return math.Erfc(math.Abs(z) / math.Sqrt2)
}
