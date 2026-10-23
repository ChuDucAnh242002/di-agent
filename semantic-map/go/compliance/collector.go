package compliance

import (
	"testing"

	"github.com/DiyazY/di-agent/pkg/contracts"
	"github.com/DiyazY/di-agent/pkg/types"
)

// CollectorFactory builds a fresh collector for one check, given that check's
// *testing.T for t.TempDir, t.Cleanup and t.Fatalf.
type CollectorFactory func(t *testing.T) contracts.CollectorContract

// CheckFactory is CollectorFactory for a caller with no *testing.T.
type CheckFactory func(r Reporter) contracts.CollectorContract

// RunCollectorCompliance certifies the collector factory builds, as subtests of t.
// Each subtest's factory call receives that subtest's own *testing.T.
func RunCollectorCompliance(t *testing.T, factory CollectorFactory) {
	t.Helper()
	RunCollectorChecks(TReporter(t), func(r Reporter) contracts.CollectorContract {
		return factory(testingT(r))
	})
}

// DefaultBatchTicks is how many collects RunCollectorChecks makes in the checks
// that hold for every batch, not only the first.
const DefaultBatchTicks = 3

// RunCollectorChecks is the assertion set behind RunCollectorCompliance, written
// against Reporter so a command line can run it over any collector.
func RunCollectorChecks(t Reporter, factory CheckFactory) {
	t.Helper()
	RunCollectorChecksTicks(t, factory, DefaultBatchTicks)
}

// RunCollectorChecksTicks is RunCollectorChecks with the number of collects the
// every-batch checks (NoDuplicateSamplesInBatch, NoMalformedSamples) make, so a
// source that goes bad on a later tick is caught; ticks below 1 count as 1.
func RunCollectorChecksTicks(t Reporter, factory CheckFactory, ticks int) {
	t.Helper()
	ticks = max(ticks, 1)

	// ── SourceID ──────────────────────────────────────────────────────────────

	t.Run("SourceIDNonEmpty", func(t Reporter) {
		c := factory(t)
		if c.SourceID() == "" {
			t.Errorf("SourceID() must return a non-empty string")
		}
	})

	t.Run("SourceIDStable", func(t Reporter) {
		c := factory(t)
		if c.SourceID() != c.SourceID() {
			t.Errorf("SourceID() must return the same value on every call")
		}
	})

	// ── AvailableMetrics ──────────────────────────────────────────────────────

	// A pattern-driven collector knows its types only once it has asked its source,
	// so these are asserted after a Collect.
	t.Run("AvailableMetricsNonEmpty", func(t Reporter) {
		c := factory(t)
		_, _ = c.Collect()
		if len(c.AvailableMetrics()) == 0 {
			t.Errorf("AvailableMetrics() must list at least one MetricType once Collect has produced samples")
		}
	})

	t.Run("AvailableMetricsStableWithoutACollect", func(t Reporter) {
		c := factory(t)
		_, _ = c.Collect()
		first := metricSet(c.AvailableMetrics())
		second := metricSet(c.AvailableMetrics())
		for mt := range first {
			if !second[mt] {
				t.Errorf("AvailableMetrics() changed with no Collect between: %q present first call but not second", mt)
			}
		}
		for mt := range second {
			if !first[mt] {
				t.Errorf("AvailableMetrics() changed with no Collect between: %q present second call but not first", mt)
			}
		}
	})

	// ── Collect ───────────────────────────────────────────────────────────────

	t.Run("CollectDoesNotError", func(t Reporter) {
		c := factory(t)
		_, err := c.Collect()
		if err != nil {
			t.Fatalf("Collect() returned unexpected error: %v", err)
		}
	})

	t.Run("CollectTwiceNoError", func(t Reporter) {
		c := factory(t)
		if _, err := c.Collect(); err != nil {
			t.Fatalf("first Collect() error: %v", err)
		}
		if _, err := c.Collect(); err != nil {
			t.Fatalf("second Collect() error: %v", err)
		}
	})

	t.Run("SamplesHaveNonEmptyNodeID", func(t Reporter) {
		c := factory(t)
		samples, _ := c.Collect()
		for _, s := range samples {
			if s.NodeID == "" {
				t.Errorf("MetricSample.NodeID must be non-empty; got %+v", s)
			}
		}
	})

	t.Run("SamplesHaveNonEmptyEventID", func(t Reporter) {
		c := factory(t)
		samples, _ := c.Collect()
		for _, s := range samples {
			if s.EventID == "" {
				t.Errorf("MetricSample.EventID must be non-empty; got %+v", s)
			}
		}
	})

	// The map keys a property by metric_type@subject and serves its id as one path
	// segment, so the agent drops a sample with a malformed type or subject. A
	// collector that would lose samples that way at runtime is not certified.
	t.Run("SamplesHaveValidTypeAndSubject", func(t Reporter) {
		c := factory(t)
		samples, _ := c.Collect()
		for _, s := range samples {
			if !types.ValidMetricType(string(s.MetricType)) {
				t.Errorf("MetricSample.MetricType %q must be a single segment over [A-Za-z0-9._-]; got %+v", s.MetricType, s)
			}
			if !types.ValidSubject(s.Subject) {
				t.Errorf("MetricSample.Subject %q must be empty or <kind>:<identity> over [A-Za-z0-9._:-]; got %+v", s.Subject, s)
			}
			if !types.ValidPartOf(s.PartOf, s.Subject) {
				t.Errorf("MetricSample.PartOf %q must be empty, or a valid node-level metric type on a sample with a subject; got %+v", s.PartOf, s)
			}
		}
	})

	// Two samples of one batch with one EventID are one observation to the map,
	// which keeps the first and drops the rest. A collector that sends them loses
	// readings without knowing it — typically two samples of one type and subject
	// that differ only in labels, which are not part of a sample's identity. An
	// adapter that drops such duplicates itself reports them through
	// contracts.DropReporter, and they fail this check too.
	t.Run("NoDuplicateSamplesInBatch", func(t Reporter) {
		c := factory(t)
		for i := 1; i <= ticks; i++ {
			samples, _ := c.Collect()
			seen := map[string]bool{}
			for _, s := range samples {
				if seen[s.EventID] {
					t.Errorf("collect %d: EventID %q appears twice in one batch (%s, subject %q); the map keeps only the first", i, s.EventID, s.MetricType, s.Subject)
				}
				seen[s.EventID] = true
			}
			for _, d := range takeDrops(c) {
				if d.Kind == contracts.DropDuplicate {
					t.Errorf("collect %d: the source sent a duplicate sample of %q (same type, subject and timestamp as another in its batch); the agent drops it", i, d.MetricType)
				}
			}
		}
	})

	// A collector that validates what its source sends (the exec and http
	// adapters) drops a sample it cannot read rather than guessing at it; the batch
	// it returns is clean, so the drop is visible only through DropReporter. A
	// source whose samples the agent would drop at run time is not certified.
	t.Run("NoMalformedSamples", func(t Reporter) {
		c := factory(t)
		for i := 1; i <= ticks; i++ {
			_, _ = c.Collect()
			for _, d := range takeDrops(c) {
				switch d.Kind {
				case contracts.DropMalformed:
					if d.MetricType != "" {
						t.Errorf("collect %d: the source sent a malformed sample of %q (an unknown field, a field of the wrong type, or no value); the agent drops it", i, d.MetricType)
					} else {
						t.Errorf("collect %d: the source sent a malformed sample (not an object, or no readable metric_type); the agent drops it", i)
					}
				case contracts.DropInvalidAvailable:
					t.Errorf("collect %d: the source declared %q available, which is not a valid metric type; the agent drops it", i, d.MetricType)
				}
			}
		}
	})

	// A sample whose unit and range are not declared cannot be reasoned about: the
	// map normalises a contribution by the property's range, so an undeclared range
	// means the map assumes [0,1] and every answer that reads the property carries a
	// caveat saying so. The contract makes declaring them a MUST; this asserts it.
	// Every sample-level subtest in this suite ranges over one Collect() batch and
	// passes on an empty one. A factory whose collector emits nothing therefore
	// certifies nothing; this makes that visible instead of vacuous.
	t.Run("CollectProducesSamples", func(t Reporter) {
		c := factory(t)
		samples, err := c.Collect()
		if err != nil {
			t.Fatalf("Collect: %v", err)
		}
		if len(samples) == 0 {
			t.Fatalf("Collect returned no samples: the sample-level guarantees are asserted over this batch, so the factory must build a collector with data behind it")
		}
	})

	t.Run("SamplesDeclareUnitAndRange", func(t Reporter) {
		c := factory(t)
		samples, _ := c.Collect()
		for _, s := range samples {
			if s.Unit == "" {
				t.Errorf("MetricSample.Unit must be non-empty; got %+v", s)
			}
			if s.Range != nil && s.Range[1] <= s.Range[0] {
				t.Errorf("MetricSample.Range %v is empty; hi must exceed lo", *s.Range)
			}
		}
	})

	// "keep them stable per type": the map stamps unit and range on the property at
	// admission and treats a later disagreement as a conflict, so a collector whose
	// declaration moves between calls, or between subjects of one type, poisons its
	// own properties.
	t.Run("UnitAndRangeStablePerType", func(t Reporter) {
		c := factory(t)
		type decl struct {
			unit     string
			declared bool
			rng      [2]float64
		}
		seen := map[types.MetricType]decl{}
		for call := 0; call < 2; call++ {
			samples, _ := c.Collect()
			for _, s := range samples {
				d := decl{unit: s.Unit, declared: s.Range != nil}
				if s.Range != nil {
					d.rng = *s.Range
				}
				if prev, ok := seen[s.MetricType]; ok && prev != d {
					t.Errorf("%s declared %q %v and then %q %v; unit and range must be stable per type",
						s.MetricType, prev.unit, prev.rng, d.unit, d.rng)
				}
				seen[s.MetricType] = d
			}
		}
	})

	t.Run("SamplesMetricTypeInAvailable", func(t Reporter) {
		c := factory(t)
		samples, _ := c.Collect()
		available := metricSet(c.AvailableMetrics())
		for _, s := range samples {
			if !available[s.MetricType] {
				t.Errorf("sample MetricType %q not declared in the AvailableMetrics() read after the Collect that emitted it", s.MetricType)
			}
		}
	})

	t.Run("EventIDDeterministic", func(t Reporter) {
		// Collect twice; any overlapping EventIDs must refer to identical samples.
		// Implementations with advancing windows produce disjoint batches —
		// in that case the test trivially passes.
		c := factory(t)
		first := sampleMap(mustCollect(t, c))
		second := sampleMap(mustCollect(t, c))
		for eid, s1 := range first {
			s2, ok := second[eid]
			if !ok {
				continue
			}
			if s1.NodeID != s2.NodeID || s1.MetricType != s2.MetricType ||
				s1.Value != s2.Value || s1.TimestampUnix != s2.TimestampUnix {
				t.Errorf("EventID %q maps to different samples:\n  first:  %+v\n  second: %+v", eid, s1, s2)
			}
		}
	})
}

// ── helpers ───────────────────────────────────────────────────────────────────

func metricSet(ms []types.MetricType) map[types.MetricType]bool {
	out := make(map[types.MetricType]bool, len(ms))
	for _, m := range ms {
		out[m] = true
	}
	return out
}

// takeDrops is what c dropped from its last Collect, when it reports drops.
func takeDrops(c contracts.CollectorContract) []contracts.SampleDrop {
	if d, ok := c.(contracts.DropReporter); ok {
		return d.TakeDrops()
	}
	return nil
}

func mustCollect(t Reporter, c contracts.CollectorContract) []*types.MetricSample {
	t.Helper()
	samples, err := c.Collect()
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}
	return samples
}

func sampleMap(samples []*types.MetricSample) map[string]*types.MetricSample {
	out := make(map[string]*types.MetricSample, len(samples))
	for _, s := range samples {
		out[s.EventID] = s
	}
	return out
}
