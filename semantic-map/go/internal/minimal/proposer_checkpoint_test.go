package minimal

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/types"
)

func checkpointFixture(t *testing.T, n int) (*MICorrelationProposer, time.Time) {
	t.Helper()
	p := NewMICorrelationProposer(nil, 0.9, 6, 12, 0)
	base := time.Now().Add(-time.Minute)
	for i := 0; i < n; i++ {
		checkpointFeed(t, p, base.Add(time.Duration(i)*time.Second), i)
	}
	return p, base
}

func checkpointFeed(t *testing.T, p *MICorrelationProposer, at time.Time, i int) {
	t.Helper()
	rng := &[2]float64{0, 1}
	x := float64(i%12) / 12
	for _, v := range []types.PropertyReading{
		{ID: "x", Value: x, Range: rng, At: at},
		{ID: "y", Value: 0.1 + 0.8*x, Range: rng, At: at},
	} {
		if err := p.ObserveProperty(v); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDiscoveryCheckpointResumesFreshEvidenceAndDeduplication(t *testing.T) {
	p, base := checkpointFixture(t, 29)
	candidate := persistenceCandidate(t, p)
	if err := p.Annotate(candidate.CandidateID, types.CandidateReview{By: types.ReviewerRules, Action: types.ReviewUndecided}); err != nil {
		t.Fatal(err)
	}
	raw, err := p.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	q := NewMICorrelationProposer(nil, 0.9, 6, 12, 0)
	note, err := q.RestoreCheckpoint(raw, time.Now(), 2*time.Minute)
	if err != nil || !strings.Contains(note, "restored 1") {
		t.Fatalf("restore: %q %v", note, err)
	}
	before := persistenceCandidate(t, q)
	if before.PersistentWindows != 2 || before.PersistencePairs != 5 || before.Review != nil {
		t.Fatalf("lost evidence progress: %+v", before)
	}
	checkpointFeed(t, q, base.Add(28*time.Second), 28)
	if c := persistenceCandidate(t, q); c.PersistentWindows != 2 || c.PersistencePairs != 5 {
		t.Fatalf("replayed timestamp advanced progress: %+v", c)
	}
	for i := 29; i < 36; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		checkpointFeed(t, p, at, i)
		checkpointFeed(t, q, at, i)
	}
	a, b := persistenceCandidate(t, p), persistenceCandidate(t, q)
	if a.PersistentWindows != 3 || b.PersistentWindows != 3 || a.MIScore != b.MIScore {
		t.Fatalf("restore differs from uninterrupted execution: %+v vs %+v", a, b)
	}
}

func TestDiscoveryCheckpointDiscardsExpiredOrChangedPolicy(t *testing.T) {
	p, _ := checkpointFixture(t, 29)
	raw, err := p.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		now       time.Time
		threshold float64
		gap       time.Duration
	}{
		{"expired", time.Now().Add(3 * time.Minute), 0.9, 2 * time.Minute},
		{"clock backwards", time.Now().Add(-time.Minute), 0.9, 2 * time.Minute},
		{"threshold changed", time.Now(), 0.8, 2 * time.Minute},
		{"freshness policy changed", time.Now(), 0.9, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := NewMICorrelationProposer(nil, tc.threshold, 6, 12, 0)
			note, err := q.RestoreCheckpoint(raw, tc.now, tc.gap)
			if err != nil || !strings.Contains(note, "discarded") || len(q.buffers) != 0 {
				t.Fatalf("expected explicit cold start, got %q %v", note, err)
			}
		})
	}
}

func TestDiscoveryCheckpointRejectsBrokenWindow(t *testing.T) {
	p, _ := checkpointFixture(t, 29)
	raw, err := p.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	var s discoveryCheckpoint
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	s.Pairs[0].Window.Next = -1
	raw, err = json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	q := NewMICorrelationProposer(nil, 0.9, 6, 12, 0)
	if _, err := q.RestoreCheckpoint(raw, time.Now(), 2*time.Minute); err == nil {
		t.Fatal("broken window accepted")
	}
}

func TestDiscoveryResetsAcrossAnObservationGap(t *testing.T) {
	p, base := checkpointFixture(t, 29)
	checkpointFeed(t, p, base.Add(5*time.Minute), 29)
	if c := persistenceCandidate(t, p); c.PersistentWindows != 0 || p.buffers["x→y"].Len() != 1 {
		t.Fatalf("long observation gap retained old evidence: %+v", c)
	}
}

func TestDiscoveryCheckpointPreservesReplacementRollback(t *testing.T) {
	p, base := checkpointFixture(t, 29)
	at := base.Add(29 * time.Second)
	if err := p.ObserveProperty(types.PropertyReading{ID: "x", Value: float64(29%12) / 12,
		At: at, Range: &[2]float64{0, 1}}); err != nil {
		t.Fatal(err)
	}
	raw, err := p.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	q := NewMICorrelationProposer(nil, 0.9, 6, 12, 0)
	if _, err := q.RestoreCheckpoint(raw, time.Now(), 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	// x has arrived but its closer y partner has not. Both executions must roll
	// back the provisional evidence rather than count a second observation.
	for i := 29; i < 36; i++ {
		checkpointFeed(t, p, base.Add(time.Duration(i)*time.Second), i)
		checkpointFeed(t, q, base.Add(time.Duration(i)*time.Second), i)
	}
	a, b := persistenceCandidate(t, p), persistenceCandidate(t, q)
	if a.PersistentWindows != 3 || a.PersistentWindows != b.PersistentWindows ||
		a.PersistencePairs != b.PersistencePairs || a.MIScore != b.MIScore {
		t.Fatalf("restored replacement differs: %+v vs %+v", a, b)
	}
}

func TestDiscoveryCheckpointDoesNotInventTestsDuringWarmup(t *testing.T) {
	p, _ := checkpointFixture(t, 3)
	raw, err := p.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	q := NewMICorrelationProposer(nil, 0.9, 6, 12, 0)
	if _, err := q.RestoreCheckpoint(raw, time.Now(), 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(q.pvals) != 0 || q.slots["x→y"].n != 0 {
		t.Fatalf("warmup gained synthetic FDR evidence: %v %+v", q.pvals, q.slots["x→y"])
	}
}

func TestDiscoveryCheckpointDropsStalePairsAndPhantomConfirmations(t *testing.T) {
	p, _ := checkpointFixture(t, 36)
	c := persistenceCandidate(t, p)
	if _, err := p.Confirm(c.CandidateID, "", types.CandidateReview{Association: true}); err != nil {
		t.Fatal(err)
	}
	raw, err := p.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	var s discoveryCheckpoint
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Minute).UnixNano()
	s.Pairs[0].LastPair = [2]int64{old, old}
	raw, err = json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	q := NewMICorrelationProposer(nil, 0.9, 6, 12, 0)
	if _, err := q.RestoreCheckpoint(raw, time.Now(), 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	c = persistenceCandidate(t, q)
	if len(q.buffers) != 0 || c.Status != types.Pending || c.PersistentWindows != 0 || c.Review != nil {
		t.Fatalf("stale evidence survived a phantom confirmation: %+v", c)
	}
}
