package stats

import (
	"math"
	"math/rand"
	"testing"
)

// ar1 is an AR(1) series x_t = φ·x_{t−1} + ε_t with unit-variance white noise,
// started from its stationary distribution so the first reading is not special.
func ar1(rng *rand.Rand, n int, phi float64) []float64 {
	xs := make([]float64, n)
	xs[0] = rng.NormFloat64() / math.Sqrt(1-phi*phi)
	for i := 1; i < n; i++ {
		xs[i] = phi*xs[i-1] + rng.NormFloat64()
	}
	return xs
}

// Two independent AR(1) series with φ = 0.9 correlate by chance far more often than
// independent samples do: over 120 readings the naive Fisher test calls a large share
// of them significant at 0.05. With the effective sample size the share falls back
// near the nominal rate. The seeds are fixed, so the shares are exact numbers, not
// draws; the bounds are loose so a change of estimator does not trip them.
func TestEffectiveNHoldsTheFalsePositiveRateOnAutocorrelatedSeries(t *testing.T) {
	const n, trials, alpha = 120, 2000, 0.05
	rng := rand.New(rand.NewSource(1))
	naive, corrected := 0, 0
	for i := 0; i < trials; i++ {
		xs, ys := ar1(rng, n, 0.9), ar1(rng, n, 0.9)
		r, ok := Pearson(xs, ys)
		if !ok {
			continue
		}
		if FisherPValue(r, float64(n)) <= alpha {
			naive++
		}
		if FisherPValue(r, EffectiveN(xs, ys)) <= alpha {
			corrected++
		}
	}
	naiveRate, correctedRate := float64(naive)/trials, float64(corrected)/trials
	t.Logf("false-positive share at α=%.2f: naive %.3f, effective-n %.3f", alpha, naiveRate, correctedRate)
	if naiveRate < 0.3 {
		t.Fatalf("the naive rate should be far above α on φ=0.9 series; got %.3f", naiveRate)
	}
	if correctedRate > 2*alpha {
		t.Errorf("effective-n false-positive share %.3f, want at most %.2f", correctedRate, 2*alpha)
	}
}

func TestEffectiveNOfWhiteNoiseIsAboutN(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	const n = 1000
	xs, ys := make([]float64, n), make([]float64, n)
	for i := range xs {
		xs[i], ys[i] = rng.NormFloat64(), rng.NormFloat64()
	}
	if ne := EffectiveN(xs, ys); ne < 0.95*n || ne > n {
		t.Errorf("white noise: n_eff = %.1f, want within [%.0f, %d]", ne, 0.95*n, n)
	}
}

func TestEffectiveNOfAR1MatchesTheLag1Formula(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	const n, phi = 5000, 0.9
	xs, ys := ar1(rng, n, phi), ar1(rng, n, phi)
	want := n * (1 - phi*phi) / (1 + phi*phi)
	if ne := EffectiveN(xs, ys); math.Abs(ne-want) > 0.15*want {
		t.Errorf("φ=0.9: n_eff = %.1f, want about %.1f", ne, want)
	}
}

func TestEffectiveNIsClamped(t *testing.T) {
	ramp := make([]float64, 100)
	for i := range ramp {
		ramp[i] = float64(i)
	}
	// Two ramps correlate perfectly whether or not they are related: a trend shared
	// over one window is one observation, not a hundred.
	if ne := EffectiveN(ramp, ramp); ne < 3 || ne >= 4 {
		t.Errorf("two ramps carry almost no independent evidence; n_eff = %v, want in [3, 4)", ne)
	}
	alt := make([]float64, 100)
	for i := range alt {
		alt[i] = float64(i % 2)
	}
	if ne := EffectiveN(alt, ramp); ne != 100 {
		t.Errorf("a negative lag-1 product must not inflate n past the window; n_eff = %v", ne)
	}
	if ne := EffectiveN([]float64{1, 2}, []float64{1, 2}); ne != 2 {
		t.Errorf("a window shorter than the floor keeps its length; n_eff = %v", ne)
	}
}

// The window is a ring: once full, the oldest reading sits at next, not at 0. The
// lag-1 autocorrelation must walk the readings in the order they arrived, or the
// wrap point pairs the newest reading with the oldest.
func TestPairWindowEffectiveNWalksTheRingInOrder(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	xs, ys := ar1(rng, 250, 0.8), ar1(rng, 250, 0.8)
	w := NewPairWindow(100)
	for i := range xs {
		w.Fold("", xs[i], ys[i])
	}
	want := EffectiveN(xs[150:], ys[150:])
	if got := w.EffectiveN(); math.Abs(got-want) > 1e-9 {
		t.Errorf("ring n_eff = %v, chronological = %v", got, want)
	}
}

func TestFisherPValueTakesAFractionalN(t *testing.T) {
	if p := FisherPValue(0.9, 3.5); p >= 1 || p <= FisherPValue(0.9, 4) {
		t.Errorf("p at n=3.5 should lie between p at n=3 (1) and p at n=4; got %v", p)
	}
}
