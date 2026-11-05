package minimal_test

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/DiyazY/di-agent/internal/minimal"
)

// TestAutocorrelatedNoiseIsNotADiscovery: fifty pairs of independent AR(1) series at
// φ = 0.9 — telemetry's usual shape, each reading mostly the one before — sit beside
// one pair with a real relationship. Over a 120-reading window chance alone gives the
// null pairs |r| near 0.3 routinely, and on the raw count a p-value near 1e-3, which
// the Benjamini–Hochberg bar admits. On the effective sample size (about 13 readings'
// worth in theory, about 18 as estimated from one window) those p-values are near 0.3
// and the bar holds; the real pair, |r| near 0.99, still clears it. The bound on null
// pairs is loose because the count is one draw: seeds 1 to 20 admit 0 to 2 of 50.
func TestAutocorrelatedNoiseIsNotADiscovery(t *testing.T) {
	const nulls, ticks, phi = 50, 240, 0.9
	p := minimal.NewMICorrelationProposer(coveredNone{}, 0.3, 30, 120, 0)
	rng := rand.New(rand.NewSource(11))
	xs, ys := make([]float64, nulls+1), make([]float64, nulls+1)
	for range ticks {
		for i := range xs {
			xs[i] = phi*xs[i] + rng.NormFloat64()
			ys[i] = phi*ys[i] + rng.NormFloat64()
		}
		for i := 0; i < nulls; i++ {
			_ = p.Observe(fmt.Sprintf("a%02d", i), fmt.Sprintf("b%02d", i), xs[i], ys[i])
		}
		// The real pair: latency follows load with noise of its own.
		_ = p.Observe("load", "latency", xs[nulls], xs[nulls]+0.3*rng.NormFloat64())
	}
	h, err := p.GetHistory()
	if err != nil {
		t.Fatal(err)
	}
	falses, real := 0, false
	for _, c := range h {
		if c.CandidateID == "load->latency" {
			real = true
			t.Logf("real pair: |r| = %.3f, p = %.2g", c.MIScore, c.PValue)
			continue
		}
		falses++
	}
	t.Logf("null pairs ever admitted: %d of %d", falses, nulls)
	if !real {
		t.Error("a strong relationship between autocorrelated series must still be admitted")
	}
	if falses > 4 {
		t.Errorf("%d of %d independent AR(1) pairs were admitted; the bar is not holding", falses, nulls)
	}
}
