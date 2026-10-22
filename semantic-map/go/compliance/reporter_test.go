package compliance

import (
	"fmt"
	"testing"

	"github.com/DiyazY/di-agent/pkg/contracts"
	"github.com/DiyazY/di-agent/pkg/types"
)

type emptyCollector struct{}

func (emptyCollector) SourceID() string                        { return "empty" }
func (emptyCollector) AvailableMetrics() []types.MetricType    { return nil }
func (emptyCollector) Collect() ([]*types.MetricSample, error) { return nil, nil }

// A failing check must be reported by name, and Fatalf must end only the check that
// raised it: the checks after it still run.
func TestRunCollectorChecksOverRecorder(t *testing.T) {
	rec := NewRecorder()
	RunCollectorChecks(rec, func(r Reporter) contracts.CollectorContract { return emptyCollector{} })
	res := rec.Results()
	byName := map[string]Result{}
	for _, x := range res {
		byName[x.Name] = x
	}
	if !byName["SourceIDNonEmpty"].Passed() {
		t.Errorf("SourceIDNonEmpty should pass: %+v", byName["SourceIDNonEmpty"])
	}
	if byName["CollectProducesSamples"].Passed() {
		t.Error("CollectProducesSamples must fail for a collector that emits nothing")
	}
	if _, ran := byName["EventIDDeterministic"]; !ran {
		t.Errorf("checks after a Fatalf must still run; got %v", fmt.Sprint(res))
	}
}
