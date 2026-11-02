package autonomy_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/internal/minimal"
	"github.com/DiyazY/di-agent/pkg/autonomy"
	"github.com/DiyazY/di-agent/pkg/semmap"
	"github.com/DiyazY/di-agent/pkg/statemap"
	"github.com/DiyazY/di-agent/pkg/types"
)

func TestPersistentCorrelationRule(t *testing.T) {
	for _, tc := range []struct {
		name       string
		windows    int
		undirected bool
		caveats    []string
		level      autonomy.Level
		wantAction string
		applied    bool
	}{
		{"legacy candidate", 0, false, nil, autonomy.Rules, types.ReviewUndecided, false},
		{"two windows", 2, false, nil, autonomy.Rules, types.ReviewUndecided, false},
		{"three windows", 3, false, nil, autonomy.Rules, types.ReviewConfirm, true},
		{"undirected", 3, true, nil, autonomy.Rules, types.ReviewUndecided, false},
		{"caveat", 3, false, []string{"missing range"}, autonomy.Rules, types.ReviewUndecided, false},
		{"suggest only", 3, false, nil, autonomy.Suggest, types.ReviewConfirm, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := directed("cpu_utilization@pod:a->cpu_pressure", "")
			c.PersistentWindows, c.Undirected, c.Caveats = tc.windows, tc.undirected, tc.caveats
			m := newFakeMap(t, c)
			cfg, err := autonomy.Build(m, autonomy.Parts{Rules: []string{"persistent-correlation"}})
			if err != nil {
				t.Fatal(err)
			}
			cfg.Level = tc.level
			run(t, m, cfg)
			if c.Review == nil || c.Review.Action != tc.wantAction || c.Review.Applied != tc.applied {
				t.Fatalf("review %+v; want %s applied=%v", c.Review, tc.wantAction, tc.applied)
			}
			if tc.wantAction == types.ReviewConfirm {
				if c.Review.Model != "persistent-correlation" {
					t.Fatalf("wrong rule recorded: %+v", c.Review)
				}
				d, ok := m.st.Journal().Decision(c.Review.DecisionID)
				if !ok || d.Inputs["candidate"].(types.CandidateEdge).PersistentWindows != 3 {
					t.Fatalf("decision lacks persistence evidence: %+v", d)
				}
			}
		})
	}
}

func TestPersistentCorrelationIsReviewedWhenWindowsAdvance(t *testing.T) {
	c := directed("cpu_utilization@pod:a->cpu_pressure", "")
	c.PersistentWindows = 1
	m := newFakeMap(t, c)
	cfg, err := autonomy.Build(m, autonomy.Parts{Rules: []string{"persistent-correlation"}})
	if err != nil {
		t.Fatal(err)
	}

	cfg.Level = autonomy.Rules
	r := run(t, m, cfg)
	first := c.Review.DecisionID
	for i := 0; i < 10; i++ {
		r.Cycle(context.Background())
	}
	if c.Review.DecisionID != first || c.Status != types.Pending {
		t.Fatal("repeated review cycles changed unchanged evidence")
	}
	c.PersistentWindows = 2
	r.Cycle(context.Background())
	if c.Review.DecisionID == first || c.Status != types.Pending {
		t.Fatal("second window was not reviewed or was approved too soon")
	}
	c.PersistentWindows = 3
	r.Cycle(context.Background())
	if c.Status != types.Confirmed || c.Review.PersistentWindows != 3 {
		t.Fatalf("unchanged r with new windows was not confirmed: %+v", c)
	}
}

func TestPersistentCorrelationCreatesATracedRelationship(t *testing.T) {
	const from, to = "cpu_utilization@pod:a", "cpu_pressure"
	state := statemap.New(statemap.Config{AdmitUnknown: true}, statemap.NewJournal(0))
	p := minimal.NewMICorrelationProposer(state, 0.9, 6, 12, 0)
	m := semmap.New(minimal.NewOntologyFromSpec(nil), nil, p, minimal.NewDisabledTuner())
	m.AttachState(state)
	cfg, err := autonomy.Build(m, autonomy.Parts{Rules: []string{"composition", "persistent-correlation"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Level, cfg.Logf = autonomy.Rules, t.Logf
	r, err := autonomy.New(m, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rng := &[2]float64{0, 1}
	for i := 0; i < 36; i++ {
		at := time.Now().Add(time.Duration(i) * 10 * time.Second)
		x := float64(i%12) / 12
		for _, reading := range []types.PropertyReading{
			{ID: from, Subject: "pod:a", At: at, Value: x, Range: rng},
			{ID: to, At: at, Value: 0.8*x + 0.1, Range: rng},
		} {
			if err := state.Record(statemap.Observation{ID: reading.ID, Subject: reading.Subject, Value: reading.Value, At: at}); err != nil {
				t.Fatal(err)
			}
			if err := p.ObserveProperty(reading); err != nil {
				t.Fatal(err)
			}
			// A replay of the same timestamp must not supply another observation.
			if err := p.ObserveProperty(reading); err != nil {
				t.Fatal(err)
			}
		}
		r.Cycle(context.Background())
		if i < 35 && state.Covered(from, to, 1) {
			t.Fatalf("confirmed after %d pairs, before three full windows", i+1)
		}
	}
	rel, ok := state.Relationship(statemap.RelationshipID(from, to, "discovered"))
	if !ok || rel.Provenance != statemap.Discovered || !strings.Contains(rel.Note, "persistent-correlation") {
		t.Fatalf("missing traced discovered relationship: %+v", rel)
	}
	h, err := m.CandidateHistory()
	if err != nil || len(h) != 1 {
		t.Fatalf("history %v: %v", h, err)
	}
	c := h[0]
	if c.Status != types.Confirmed || c.Review == nil || c.Review.PersistentWindows != 3 {
		t.Fatalf("candidate not confirmed with persistence evidence: %+v", c)
	}
	d, ok := state.Journal().Decision(c.Review.DecisionID)
	if !ok || d.Answer["model"] != "persistent-correlation" || d.Answer["applied"] != true {
		t.Fatalf("missing applied decision: %+v", d)
	}
}
