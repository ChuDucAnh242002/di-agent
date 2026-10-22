package collectors

import (
	"testing"

	"github.com/DiyazY/di-agent/pkg/types"
)

// A plugin that leaves timestamp_unix out must not have every sample hashed at
// 1970: the same EventID each tick would make the map drop all but the first as a
// duplicate. The request's now stands in for the missing timestamp.
func TestStampReplyZeroTimestampTakesRequestNow(t *testing.T) {
	ids := map[string]bool{}
	for i, v := range []float64{0.1, 0.5, 0.9} {
		now := int64(1_700_000_000 + 10*i)
		r := &CollectReply{Samples: []*types.MetricSample{{MetricType: "load", Value: v, Unit: "load"}}}
		got, _ := stampReply("p", "n1", now, r)
		if len(got) != 1 {
			t.Fatalf("tick %d: %d samples", i, len(got))
		}
		if got[0].TimestampUnix != now {
			t.Errorf("tick %d: timestamp %d, want the request's now %d", i, got[0].TimestampUnix, now)
		}
		if ids[got[0].EventID] {
			t.Errorf("tick %d: EventID %q repeats an earlier tick's", i, got[0].EventID)
		}
		ids[got[0].EventID] = true
	}
}

// A timestamp the plugin set itself is the plugin's to state.
func TestStampReplyKeepsPluginTimestamp(t *testing.T) {
	r := &CollectReply{Samples: []*types.MetricSample{{MetricType: "load", Value: 1, Unit: "load", TimestampUnix: 42}}}
	got, _ := stampReply("p", "n1", 1_700_000_000, r)
	if got[0].TimestampUnix != 42 {
		t.Fatalf("timestamp %d, want 42", got[0].TimestampUnix)
	}
}
