package minimal

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/statemap"
	"github.com/DiyazY/di-agent/pkg/stats"
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
	note, err := q.RestoreCheckpoint(raw, time.Now(), DiscoveryEvidenceMaxGap)
	if err != nil || !strings.Contains(note, "restored 1") {
		t.Fatalf("restore: %q %v", note, err)
	}
	before := persistenceCandidate(t, q)
	if before.PersistentWindows != 2 || before.PersistencePairs != 5 || before.Review == nil ||
		before.Review.Action != types.ReviewUndecided || before.Review.By != types.ReviewerRules {
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
		{"expired", time.Now().Add(25 * time.Hour), 0.9, DiscoveryEvidenceMaxGap},
		{"clock backwards", time.Now().Add(-time.Minute), 0.9, DiscoveryEvidenceMaxGap},
		{"threshold changed", time.Now(), 0.8, DiscoveryEvidenceMaxGap},
		{"freshness policy changed", time.Now(), 0.9, 12 * time.Hour},
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
	if _, err := q.RestoreCheckpoint(raw, time.Now(), DiscoveryEvidenceMaxGap); err == nil {
		t.Fatal("broken window accepted")
	}
}

func TestDiscoveryRetentionDoesNotEvictPropertiesOrCandidateHistory(t *testing.T) {
	p := NewMICorrelationProposer(nil, 0.9, 3, 12, time.Minute)
	p.SetMaxPairs(1)
	base := time.Now().Add(-time.Minute)
	for i := 0; i < 10; i++ {
		if err := p.ObserveProperty(types.PropertyReading{
			ID: fmt.Sprintf("property-%02d", i), Value: float64(i) / 10,
			At: base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.latestProps) != 10 || len(p.recent) != 10 {
		t.Fatalf("pair budget evicted property state: properties=%d recent=%d", len(p.latestProps), len(p.recent))
	}
	for i := 0; i < 5; i++ {
		from := fmt.Sprintf("property-%02d", i*2)
		to := fmt.Sprintf("property-%02d", i*2+1)
		id := candidateID(from, to, true)
		p.order = append(p.order, id)
		p.candidates[id] = &types.CandidateEdge{
			CandidateID: id, FromID: from, ToID: to, Direction: types.Positive,
			Status: types.Pending, Undirected: true,
		}
	}
	raw, err := p.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	q := NewMICorrelationProposer(nil, 0.9, 3, 12, time.Minute)
	q.SetMaxPairs(1)
	if _, err := q.RestoreCheckpoint(raw, time.Now(), DiscoveryEvidenceMaxGap); err != nil {
		t.Fatalf("restore candidate history larger than pair budget: %v", err)
	}
	if len(q.candidates) != 5 {
		t.Fatalf("restored %d candidates, want 5", len(q.candidates))
	}
}

func TestDiscoveryCheckpointPackedWindowsStayWithinSizeBudget(t *testing.T) {
	const pairCount, windowSize, propertyCount = 8000, 120, 128
	now := time.Now()
	s := discoveryCheckpoint{
		Version: 2, SavedAt: now, Properties: make([]types.PropertyReading, propertyCount),
		Recent: make(map[string]discoveryRecent, propertyCount), Pairs: make([]discoveryPair, pairCount),
	}
	for i := 0; i < propertyCount; i++ {
		id := fmt.Sprintf("property-%03d", i)
		s.Properties[i] = types.PropertyReading{ID: id, Value: 0.5, At: now}
		values := make([]float64, windowSize)
		for j := range values {
			values[j] = math.Sin(float64(i*windowSize+j) * 0.01)
		}
		s.Recent[id] = discoveryRecent{Values: values}
	}
	for i := range s.Pairs {
		x, y := make([]float64, windowSize), make([]float64, windowSize)
		for j := range x {
			x[j] = math.Sin(float64(i*windowSize+j) * 0.01)
			y[j] = math.Sin(float64(i*windowSize+j)*0.01 + 0.3)
		}
		s.Pairs[i] = discoveryPair{
			From: "property-000", To: "property-001", Class: types.PairNodeNode,
			Window:   packCheckpointWindow(stats.WindowSnapshot{X: x, Y: y, Capacity: windowSize}),
			LastPair: [2]int64{now.UnixNano(), now.UnixNano()},
		}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := json.MarshalIndent(statemap.Snapshot{Discovery: raw}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) > 27<<20 {
		t.Fatalf("8,000 pairs with 120 samples persisted to %d bytes; want <= 27 MiB", len(persisted))
	}
	t.Logf("8k-pair raw discovery=%d bytes persisted-indented=%d bytes", len(raw), len(persisted))
}

func TestDiscoveryCheckpointRestoresVersionOneWindows(t *testing.T) {
	p, _ := checkpointFixture(t, 29)
	raw, err := p.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	var s discoveryCheckpoint
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	s.Version = 1
	for i := range s.Pairs {
		window, err := s.Pairs[i].Window.restore()
		if err != nil {
			t.Fatal(err)
		}
		s.Pairs[i].Window = checkpointWindow{
			X: window.X, Y: window.Y, Next: window.Next, Capacity: window.Capacity, Seen: window.Seen,
		}
	}
	raw, err = json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	q := NewMICorrelationProposer(nil, 0.9, 6, 12, 0)
	if note, err := q.RestoreCheckpoint(raw, time.Now(), DiscoveryEvidenceMaxGap); err != nil ||
		!strings.Contains(note, "restored") {
		t.Fatalf("legacy restore: %q %v", note, err)
	}
}

func TestDiscoveryCheckpointRejectsMalformedSnapshots(t *testing.T) {
	p, _ := checkpointFixture(t, 29)
	raw, err := p.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	var original discoveryCheckpoint
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	if len(original.Pairs) == 0 || len(original.Candidates) == 0 {
		t.Fatalf("fixture lacks a pair or candidate: %+v", original)
	}
	cases := []struct {
		name   string
		change func(*discoveryCheckpoint)
	}{
		{"unsupported version", func(s *discoveryCheckpoint) { s.Version++ }},
		{"invalid recent window", func(s *discoveryCheckpoint) {
			r := s.Recent[s.Properties[0].ID]
			r.Next = -1
			s.Recent[s.Properties[0].ID] = r
		}},
		{"capacity mismatch", func(s *discoveryCheckpoint) { s.Pairs[0].Window.Capacity++ }},
		{"candidate id mismatch", func(s *discoveryCheckpoint) { s.Candidates[0].CandidateID = "wrong" }},
		{"pair class mismatch", func(s *discoveryCheckpoint) { s.Pairs[0].Class = "wrong" }},
		{"candidate orientation mismatch", func(s *discoveryCheckpoint) {
			c := s.Candidates[0]
			c.Undirected = !c.Undirected
			c.CandidateID = candidateID(c.FromID, c.ToID, c.Undirected)
		}},
		{"duplicate candidate", func(s *discoveryCheckpoint) {
			c := *s.Candidates[0]
			s.Candidates = append(s.Candidates, &c)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := original
			s.Properties = append([]types.PropertyReading(nil), original.Properties...)
			s.Pairs = append([]discoveryPair(nil), original.Pairs...)
			s.Candidates = make([]*types.CandidateEdge, len(original.Candidates))
			for i, candidate := range original.Candidates {
				c := copyCandidate(candidate)
				s.Candidates[i] = c
			}
			s.Recent = make(map[string]discoveryRecent, len(original.Recent))
			for id, recent := range original.Recent {
				recent.Values = append([]float64(nil), recent.Values...)
				s.Recent[id] = recent
			}
			tc.change(&s)
			invalid, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			q := NewMICorrelationProposer(nil, 0.9, 6, 12, 0)
			if _, err := q.RestoreCheckpoint(invalid, time.Now(), DiscoveryEvidenceMaxGap); err == nil {
				t.Fatal("malformed checkpoint was accepted")
			}
			if len(q.buffers) != 0 || len(q.candidates) != 0 || len(q.latestProps) != 0 {
				t.Fatal("failed restore left partially restored discovery state")
			}
		})
	}
	t.Run("bad json", func(t *testing.T) {
		q := NewMICorrelationProposer(nil, 0.9, 6, 12, 0)
		if _, err := q.RestoreCheckpoint(json.RawMessage("{bad"), time.Now(), DiscoveryEvidenceMaxGap); err == nil {
			t.Fatal("malformed JSON was accepted")
		}
	})
}

func TestDiscoveryCheckpointRejectsNonFiniteCandidateValue(t *testing.T) {
	p, _ := checkpointFixture(t, 29)
	raw, err := p.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	var candidates []map[string]json.RawMessage
	if err := json.Unmarshal(snapshot["Candidates"], &candidates); err != nil || len(candidates) == 0 {
		t.Fatalf("checkpoint has no candidates: %v", err)
	}
	candidates[0]["MIScore"] = json.RawMessage("1e309")
	snapshot["Candidates"], err = json.Marshal(candidates)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	q := NewMICorrelationProposer(nil, 0.9, 6, 12, 0)
	if _, err := q.RestoreCheckpoint(raw, time.Now(), DiscoveryEvidenceMaxGap); err == nil {
		t.Fatal("non-finite candidate value was accepted")
	}
}

func TestAgentDecisionsReopenAtPersistenceThresholdButOperatorRejectionDoesNot(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  func(*MICorrelationProposer, string) error
		by   string
		want types.CandidateStatus
	}{
		{"agent reject", func(p *MICorrelationProposer, id string) error {
			return p.Reject(id, types.CandidateReview{By: types.ReviewerDecisionModel})
		}, types.ReviewerDecisionModel, types.Pending},
		{"agent defer", func(p *MICorrelationProposer, id string) error {
			return p.Defer(id, types.CandidateReview{By: types.ReviewerReasoningAgent})
		}, types.ReviewerReasoningAgent, types.Pending},
		{"operator reject", func(p *MICorrelationProposer, id string) error {
			return p.Reject(id, types.CandidateReview{By: types.ReviewerOperator})
		}, types.ReviewerOperator, types.Rejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, base := checkpointFixture(t, 29)
			c := persistenceCandidate(t, p)
			if c.PersistentWindows != 2 {
				t.Fatalf("fixture has %d persistence windows, want 2", c.PersistentWindows)
			}
			if err := tc.act(p, c.CandidateID); err != nil {
				t.Fatal(err)
			}
			for i := 29; i < 36; i++ {
				checkpointFeed(t, p, base.Add(time.Duration(i)*time.Second), i)
			}
			c = persistenceCandidate(t, p)
			if c.Status != tc.want {
				t.Fatalf("after threshold crossing status=%v, want %v", c.Status, tc.want)
			}
			if tc.by == types.ReviewerOperator && c.Reason != "operator" {
				t.Fatalf("operator rejection lost permanence marker: %+v", c)
			}
		})
	}
}

func TestDiscoveryRestoreSettlesCandidateAlreadyBackedByAssociation(t *testing.T) {
	now := time.Now()
	state := statemap.New(statemap.Config{AdmitUnknown: true}, statemap.NewJournal(0))
	for _, id := range []string{"x", "y"} {
		if err := state.Record(statemap.Observation{ID: id, Value: 0.5, At: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.DeclareRelationship(statemap.Relationship{
		From: "x", To: "y", Label: "association", Sign: 1, Association: true,
		Provenance: statemap.Discovered,
	}); err != nil {
		t.Fatal(err)
	}
	p := NewMICorrelationProposer(nil, 0.9, 6, 12, 0)
	for i := 0; i < 36; i++ {
		at := now.Add(time.Duration(i) * time.Second)
		x := float64(i%12) / 12
		for _, reading := range []types.PropertyReading{
			{ID: "x", At: at, Value: x},
			{ID: "y", At: at, Value: 0.1 + 0.8*x},
		} {
			if err := p.ObserveProperty(reading); err != nil {
				t.Fatal(err)
			}
		}
	}
	raw, err := p.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	q := NewMICorrelationProposer(state, 0.9, 6, 12, 0)
	if _, err := q.RestoreCheckpoint(raw, now.Add(time.Minute), DiscoveryEvidenceMaxGap); err != nil {
		t.Fatal(err)
	}
	history, err := q.GetHistory()
	if err != nil || len(history) != 1 || history[0].Status != types.Confirmed {
		t.Fatalf("map association did not settle the stale pending candidate: %+v %v", history, err)
	}
	if pending, err := q.GetCandidates(); err != nil || len(pending) != 0 {
		t.Fatalf("candidate remains pending beside its association: %+v %v", pending, err)
	}
}

func TestDiscoveryResetsAcrossAnObservationGap(t *testing.T) {
	p, base := checkpointFixture(t, 29)
	checkpointFeed(t, p, base.Add(5*time.Minute), 29)
	if c := persistenceCandidate(t, p); c.PersistentWindows != 2 || p.buffers["x→y"].Len() != 12 {
		t.Fatalf("observation gap shorter than the discovery evidence limit discarded evidence: %+v", c)
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
	if _, err := q.RestoreCheckpoint(raw, time.Now(), DiscoveryEvidenceMaxGap); err != nil {
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
	if _, err := q.RestoreCheckpoint(raw, time.Now(), DiscoveryEvidenceMaxGap); err != nil {
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
	old := time.Now().Add(-25 * time.Hour).UnixNano()
	s.Pairs[0].LastPair = [2]int64{old, old}
	raw, err = json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	q := NewMICorrelationProposer(nil, 0.9, 6, 12, 0)
	if _, err := q.RestoreCheckpoint(raw, time.Now(), DiscoveryEvidenceMaxGap); err != nil {
		t.Fatal(err)
	}
	c = persistenceCandidate(t, q)
	if len(q.buffers) != 0 || c.Status != types.Pending || c.PersistentWindows != 0 || c.Review != nil {
		t.Fatalf("stale evidence survived a phantom confirmation: %+v", c)
	}
}
