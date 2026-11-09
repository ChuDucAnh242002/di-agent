package admission

import (
	"testing"
)

func TestMeasuredSlopeAboveOneCanDriveAdmissionDecline(t *testing.T) {
	m, _ := node(t, 0.2, 0.08)
	effect(t, m, "pod:a", 3.2)

	got := Decide(m, offer(1.6), both, self, "high-slope")
	if got.Verdict != Decline || len(got.Lines) == 0 {
		t.Fatalf("decision %+v; want decline from the measured map slope", got)
	}
	line := got.Lines[0]
	if line.Basis != "map" || line.K == nil || *line.K <= 1 || line.MapPred == nil ||
		*line.MapPred <= line.Exit || !got.PredictionChanged {
		t.Fatalf("CPU prediction %+v; want a greater-than-one map slope crossing exit", line)
	}
}

func TestRetiredSourceEvidenceDoesNotDriveAdmissionDecline(t *testing.T) {
	m, _ := node(t, 0.2, 0.07)
	effect(t, m, "pod:a", 3.2)
	if err := m.RetireProperty("cpu_utilization@pod:a", "subject left", "test"); err != nil {
		t.Fatal(err)
	}

	got := Decide(m, offer(0.2), both, self, "retired-source")
	if got.Verdict != Accept || len(got.Lines) == 0 {
		t.Fatalf("decision %+v; want acceptance without retired-source evidence", got)
	}
	line := got.Lines[0]
	if line.Basis != "usage" || line.K != nil || line.MapPred != nil || got.PredictionChanged {
		t.Fatalf("CPU prediction %+v; want usage basis only, with no retired map effect", line)
	}
	if _, ok := m.MeasuredDomainEffect("cpu_utilization", "cpu_contention_ratio"); ok {
		t.Fatal("retired source still contributes a measured domain effect")
	}
}

func TestRetiringRelationshipDiscardsItsDomainEvidence(t *testing.T) {
	m, _ := node(t, 0.2, 0.02)
	effect(t, m, "pod:a", 0.8)
	if err := m.RetireRelationship("cpu_utilization@pod:a->cpu_contention_ratio:drives", "test", "test"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.MeasuredDomainEffect("cpu_utilization", "cpu_contention_ratio"); ok {
		t.Fatal("retired relationship still contributes a measured domain effect")
	}

}
