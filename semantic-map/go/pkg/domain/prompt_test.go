package domain

import (
	"strings"
	"testing"
)

// TestPromptSummaryIsWhatWasLoaded: a language model is told the constructs and
// propositions this agent loaded, and nothing it did not.
func TestPromptSummaryIsWhatWasLoaded(t *testing.T) {
	s := &Spec{
		Constructs:    []Construct{{ConstructID: "RC", Name: "Resource", Description: "cost of running work"}},
		MetricRouting: []MetricRoute{{MetricType: "cpu_utilization", ConstructID: "RC"}},
		Propositions:  []Proposition{{PropositionID: "P3", FromConstruct: "RC", ToConstruct: "RC", Direction: "positive", Description: "a claim"}},
	}
	got := s.PromptSummary()
	for _, want := range []string{"RC", "Resource", "cpu_utilization", "P3", "positive"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "SC") || strings.Contains(got, "Security") {
		t.Errorf("summary names a construct that was not loaded:\n%s", got)
	}
	var none *Spec
	if got := none.PromptSummary(); !strings.Contains(got, "No domain model is loaded") {
		t.Errorf("nil spec summary %q; want it to say none is loaded", got)
	}
}
