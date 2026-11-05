package minimal

import (
	"fmt"
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

// Confirm takes a candidate the proposer deferred — its deferrals are choices about
// the queue — and refuses one the operator or the agent settled. Reopen, which the
// facade calls when the declaration is refused, puts the candidate back as it was.
func TestConfirmTakesProposerDeferralsAndRefusesSettledOnes(t *testing.T) {
	for _, tc := range []struct {
		status types.CandidateStatus
		reason string
		ok     bool
	}{
		{types.Pending, "", true},
		{types.Deferred, deferredByCap, true},
		{types.Deferred, deferredUntested, true},
		{types.Deferred, deferredGrouped, true},
		{types.Deferred, deferredByBar, true},
		{types.Deferred, deferredWeakened, true},
		{types.Deferred, byOperator, false},
		{types.Deferred, byAgent, false},
		{types.Rejected, byOperator, false},
		{types.Rejected, byAgent, false},
	} {
		t.Run(fmt.Sprintf("%v/%s", tc.status, tc.reason), func(t *testing.T) {
			p, _ := checkpointFixture(t, 29)
			c := persistenceCandidate(t, p)
			prior := &types.CandidateReview{By: types.ReviewerRules, Action: types.ReviewUndecided}
			p.candidates[c.CandidateID].Status, p.candidates[c.CandidateID].Reason = tc.status, tc.reason
			p.candidates[c.CandidateID].Review = prior
			_, err := p.Confirm(c.CandidateID, c.FromID, types.CandidateReview{})
			if (err == nil) != tc.ok {
				t.Fatalf("Confirm err = %v; want ok %v", err, tc.ok)
			}
			after := persistenceCandidate(t, p)
			if tc.ok && after.Status != types.Confirmed || !tc.ok && (after.Status != tc.status || after.Reason != tc.reason) {
				t.Errorf("after Confirm: %v/%q", after.Status, after.Reason)
			}
			if !tc.ok {
				return
			}
			if err := p.Reopen(c.CandidateID); err != nil {
				t.Fatal(err)
			}
			if after := persistenceCandidate(t, p); after.Status != tc.status || after.Reason != tc.reason ||
				after.Review == nil || *after.Review != *prior {
				t.Errorf("after Reopen: %v/%q review %+v; want %v/%q with the review it had", after.Status, after.Reason, after.Review, tc.status, tc.reason)
			}
		})
	}
}
