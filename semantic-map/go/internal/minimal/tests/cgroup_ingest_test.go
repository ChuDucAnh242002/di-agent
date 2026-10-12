package minimal_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/internal/minimal"
	"github.com/DiyazY/di-agent/pkg/semmap"
	"github.com/DiyazY/di-agent/pkg/types"
)

// TestCgroupNodeReadingsAgreeWithTheSpec drives the cgroup collector into a map
// seeded from the committed domain specification, which is what the daemon does
// on a real node. The spec declares the routed node-level properties' unit, and
// the map refuses an observation that contradicts a declaration, so a collector
// labelling the node's CPU or memory differently has every reading refused:
// the properties stay at their seed while the journal fills with conflicts.
func TestCgroupNodeReadingsAgreeWithTheSpec(t *testing.T) {
	root := newFakeCgroupRoot(t)
	state := stateFor(t)
	sm := semmap.New(minimal.NewOntologyFromSpec(mustSpec()), reasonerWithState(t), minimal.NewDisabledProposer(), minimal.NewDisabledTuner())
	sm.AttachState(state)

	c := minimal.NewCgroupCollectorWithOptions("scenario-node", root, minimal.CgroupOptions{MemTotalBytes: 16 << 30})
	if _, err := c.Collect(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	mustWrite(t, filepath.Join(root, "cpu.stat"), "usage_usec 1100000\nnr_periods 1010\nnr_throttled 50\n")
	samples, err := c.Collect()
	if err != nil {
		t.Fatal(err)
	}

	ingested := map[types.MetricType]bool{}
	for _, s := range samples {
		if s.Subject != "" {
			continue
		}
		if err := sm.IngestSample(s); err != nil {
			t.Errorf("node-level %s (unit %q) refused: %v", s.MetricType, s.Unit, err)
			continue
		}
		ingested[s.MetricType] = true
	}
	for _, mt := range []types.MetricType{types.CPUUtilization, types.MemoryUtilization} {
		if !ingested[mt] {
			t.Errorf("no node-level %s reached the map", mt)
		}
	}
}
