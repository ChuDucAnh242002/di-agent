package minimal

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/DiyazY/di-agent/pkg/types"
)

func TestPersistenceNeedsFreshFullWindows(t *testing.T) {
	const size = 12
	p := NewMICorrelationProposer(nil, 0.9, 6, size, 0)
	for i := 0; i < 3*size; i++ {
		x := float64(i%size) / size
		if err := p.Observe("x", "y", x, x); err != nil {
			t.Fatal(err)
		}
		c := persistenceCandidate(t, p)
		if want := (i + 1) / size; c != nil && c.PersistentWindows != want {
			t.Fatalf("after %d pairs: windows %d, want %d", i+1, c.PersistentWindows, want)
		}
		// Reads and replacement partners must not advance persistence.
		for j := 0; j < 3; j++ {
			_, _ = p.GetCandidates()
			if err := p.observeLocked("x", "y", x, x, fmt.Sprintf("replacement-%d-%d", i, j), pairMeta{replace: true}); err != nil {
				t.Fatal(err)
			}
		}
		if c := persistenceCandidate(t, p); c != nil && c.PersistentWindows != (i+1)/size {
			t.Fatalf("replacement advanced persistence: %+v", c)
		}
	}
	if c := persistenceCandidate(t, p); c == nil || c.PersistentWindows != 3 {
		t.Fatalf("three windows: %+v", c)
	}
}

func TestPersistenceResetsOnWeakEvidenceAndSignChange(t *testing.T) {
	p := NewMICorrelationProposer(nil, 0.9, 6, 12, 0)
	for i := 0; i < 36; i++ {
		x := float64(i%12) / 12
		if err := p.Observe("x", "y", x, x); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Observe("x", "y", 0.5, 100); err != nil {
		t.Fatal(err)
	}
	if c := persistenceCandidate(t, p); c == nil || c.PersistentWindows != 0 {
		t.Fatalf("weak evidence did not reset persistence: %+v", c)
	}
	for i := 0; i < 72; i++ {
		x := float64(i%12) / 12
		if err := p.Observe("x", "y", x, -x); err != nil {
			t.Fatal(err)
		}
	}
	if c := persistenceCandidate(t, p); c == nil || c.Direction != types.Negative || c.PersistentWindows < 3 {
		t.Fatalf("negative evidence did not build a new run: %+v", c)
	}
	// A correction to the most recent pair can invalidate the run too.
	if err := p.observeLocked("x", "y", 0.5, 100, "bad-replacement", pairMeta{replace: true}); err != nil {
		t.Fatal(err)
	}
	if c := persistenceCandidate(t, p); c == nil || c.PersistentWindows != 0 {
		t.Fatalf("bad replacement did not reset persistence: %+v", c)
	}
	if err := p.observeLocked("x", "y", 11.0/12, -11.0/12, "corrected-replacement", pairMeta{replace: true}); err != nil {
		t.Fatal(err)
	}
	if c := persistenceCandidate(t, p); c == nil || c.PersistentWindows < 3 {
		t.Fatalf("corrected partner did not restore valid persistence: %+v", c)
	}
}

func TestPersistenceRequiresCurrentVariationAndMetadata(t *testing.T) {
	rng := &[2]float64{0, 1}
	meta := pairMeta{property: true, xRange: rng, yRange: rng}
	for _, tc := range []struct {
		name string
		meta pairMeta
	}{
		{"missing range", pairMeta{property: true, xRange: rng}},
		{"insufficient spread", pairMeta{property: true, xRange: &[2]float64{0, 1e9}, yRange: rng}},
		{"kind changed", pairMeta{property: true, xRange: rng, yRange: rng, kind: types.CandidateKindComposition}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewMICorrelationProposer(nil, 0.9, 6, 12, 0)
			for i := 0; i < 36; i++ {
				x := float64(i%12) / 12
				if err := p.observeLocked("x", "y", x, x, fmt.Sprint(i), meta); err != nil {
					t.Fatal(err)
				}
			}
			if c := persistenceCandidate(t, p); c == nil || c.PersistentWindows != 3 {
				t.Fatalf("setup: %+v", c)
			}
			if err := p.observeLocked("x", "y", 0, 0, "changed", tc.meta); err != nil {
				t.Fatal(err)
			}
			if c := persistenceCandidate(t, p); c == nil || c.PersistentWindows != 0 {
				t.Fatalf("changed metadata did not reset persistence: %+v", c)
			}
		})
	}
}

func TestPersistenceDoesNotReuseForgottenWindows(t *testing.T) {
	p := NewMICorrelationProposer(nil, 0.9, 6, 12, 0)
	for i := 0; i < 36; i++ {
		if err := p.Observe("x", "y", float64(i), float64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Forget("x"); err != nil {
		t.Fatal(err)
	}
	if len(p.persistence) != 0 {
		t.Fatal("forgotten pair retained persistence")
	}
	for i := 0; i < 6; i++ {
		if err := p.Observe("x", "y", float64(i), float64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if c := persistenceCandidate(t, p); c == nil || c.PersistentWindows != 0 {
		t.Fatalf("new pair inherited persistence: %+v", c)
	}
}

func TestPersistenceResetsWhenTheSignificanceBarTightens(t *testing.T) {
	p := NewMICorrelationProposer(nil, 0.9, 6, 12, 0)
	for i := 0; i < 36; i++ {
		x := float64(i%12) / 12
		y := x + 0.01*float64(i%3)
		if err := p.Observe("x", "y", x, y); err != nil {
			t.Fatal(err)
		}
	}
	p.SetFDR(1e-300)
	if err := p.Observe("x", "y", 0, 0); err != nil {
		t.Fatal(err)
	}
	if c := persistenceCandidate(t, p); c == nil || c.PersistentWindows != 0 || c.Reason != deferredByBar {
		t.Fatalf("tightened bar did not reset persistence: %+v", c)
	}
}

// The persistence gate's p ≤ 0.05 is on the effective sample size too. With the
// corrected bar off, that limit is the only p-value gate: a pair driven by an AR(1)
// series that moves tick to tick still builds its windows, and a slow shared wave —
// |r| near 1, but one cycle per window, lag-1 near 0.998 on each side, so n_eff sits at
// the floor — builds none, where on the raw count it would build all three.
func TestPersistenceGateUsesTheEffectiveSampleSize(t *testing.T) {
	const size, ticks = 120, 4 * 120
	for _, tc := range []struct {
		name    string
		drive   func(i int, prev float64, rng *rand.Rand) float64
		noise   float64
		windows int
	}{
		{"AR(1) driver", func(_ int, prev float64, rng *rand.Rand) float64 { return 0.9*prev + rng.NormFloat64() }, 0.3, 3},
		{"slow shared wave", func(i int, _ float64, _ *rand.Rand) float64 { return math.Sin(2 * math.Pi * float64(i) / size) }, 0.01, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewMICorrelationProposer(nil, 0.85, 30, size, 0)
			p.SetFDR(0)
			rng := rand.New(rand.NewSource(1))
			x := 0.0
			for i := 0; i < ticks; i++ {
				x = tc.drive(i, x, rng)
				if err := p.Observe("x", "y", x, x+tc.noise*rng.NormFloat64()); err != nil {
					t.Fatal(err)
				}
			}
			c := persistenceCandidate(t, p)
			if c == nil || c.MIScore < 0.85 {
				t.Fatalf("fixture: the pair must clear the threshold on |r|; %+v", c)
			}
			if c.PersistentWindows != tc.windows {
				t.Errorf("|r| = %.3f, p = %.2g: %d persistent windows, want %d",
					c.MIScore, c.PValue, c.PersistentWindows, tc.windows)
			}
		})
	}
}

func persistenceCandidate(t *testing.T, p *MICorrelationProposer) *types.CandidateEdge {
	t.Helper()
	h, err := p.GetHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(h) == 0 {
		return nil
	}
	if len(h) != 1 {
		t.Fatalf("expected one candidate, got %d", len(h))
	}
	return h[0]
}
