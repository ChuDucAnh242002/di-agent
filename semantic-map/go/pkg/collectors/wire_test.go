package collectors

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DiyazY/di-agent/pkg/contracts"
	"github.com/DiyazY/di-agent/pkg/types"
)

// rawOf puts r through the wire, as a plugin's reply reaches the agent.
func rawOf(t *testing.T, r CollectReply) *rawReply {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return rawLine(t, string(b))
}

func rawLine(t *testing.T, line string) *rawReply {
	t.Helper()
	r, err := decodeReply([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A plugin that leaves timestamp_unix out must not have every sample hashed at
// 1970: the same EventID each tick would make the map drop all but the first as a
// duplicate. The request's now stands in for the missing timestamp.
func TestStampReplyZeroTimestampTakesRequestNow(t *testing.T) {
	ids := map[string]bool{}
	for i, v := range []float64{0.1, 0.5, 0.9} {
		now := int64(1_700_000_000 + 10*i)
		r := rawOf(t, CollectReply{Samples: []*types.MetricSample{{MetricType: "load", Value: v, Unit: "load"}}})
		got, _, _ := stampReply("p", "n1", now, r)
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
	r := rawOf(t, CollectReply{Samples: []*types.MetricSample{{MetricType: "load", Value: 1, Unit: "load", TimestampUnix: 42}}})
	got, _, _ := stampReply("p", "n1", 1_700_000_000, r)
	if got[0].TimestampUnix != 42 {
		t.Fatalf("timestamp %d, want 42", got[0].TimestampUnix)
	}
}

// A misspelt or missing value must not become a reading of zero, and a bad sample
// costs only itself: the rest of the reply is kept.
func TestStampReplyDropsMalformedSamples(t *testing.T) {
	r := rawLine(t, `{"seq":1,"samples":[
		{"metric_type":"ok","value":0,"unit":"u"},
		{"metric_type":"typo","valu":0.5,"unit":"u"},
		{"metric_type":"novalue","unit":"u"},
		{"metric_type":"nullvalue","value":null,"unit":"u"},
		{"metric_type":"wrongtype","value":"0.5","unit":"u"},
		null,
		7]}`)
	got, avail, drops := stampReply("p", "n1", 100, r)
	if len(got) != 1 || got[0].MetricType != "ok" || got[0].Value != 0 {
		t.Fatalf("kept %+v, want only the well-formed zero", got)
	}
	if len(avail) != 1 || avail[0] != "ok" {
		t.Fatalf("available %v, want only the kept type", avail)
	}
	want := []types.MetricType{"typo", "novalue", "nullvalue", "wrongtype", "", ""}
	if len(drops) != len(want) {
		t.Fatalf("drops %+v, want %d", drops, len(want))
	}
	for i, d := range drops {
		if d.Kind != contracts.DropMalformed || d.MetricType != want[i] {
			t.Errorf("drop %d = %+v, want malformed %q", i, d, want[i])
		}
	}
}

// Two samples with one event id are one observation to the map; the first is
// kept and the second is dropped here, so the agent counts only what lands.
func TestStampReplyDropsDuplicateEventIDs(t *testing.T) {
	r := rawOf(t, CollectReply{Samples: []*types.MetricSample{
		{MetricType: "load", Value: 1, Unit: "u", TimestampUnix: 50},
		{MetricType: "load", Value: 2, Unit: "u", TimestampUnix: 50, Labels: map[string]string{"k": "v"}},
		{MetricType: "load", Value: 3, Unit: "u", TimestampUnix: 50, Subject: "disk:sda"},
	}})
	got, _, drops := stampReply("p", "n1", 100, r)
	if len(got) != 2 || got[0].Value != 1 || got[1].Subject != "disk:sda" {
		t.Fatalf("kept %+v, want the first node-level and the scoped one", got)
	}
	if len(drops) != 1 || drops[0] != (contracts.SampleDrop{Kind: contracts.DropDuplicate, MetricType: "load"}) {
		t.Fatalf("drops %+v", drops)
	}
}

// An available list is filtered: an invalid type is dropped and reported, a
// repeat is folded.
func TestStampReplyFiltersAvailable(t *testing.T) {
	r := rawOf(t, CollectReply{Available: []types.MetricType{"a", "bad type", "a", "b"}})
	_, avail, drops := stampReply("p", "n1", 100, r)
	if strings.Join(asStrings(avail), ",") != "a,b" {
		t.Fatalf("available %v, want [a b]", avail)
	}
	if len(drops) != 1 || drops[0].Kind != contracts.DropInvalidAvailable || drops[0].MetricType != "bad type" {
		t.Fatalf("drops %+v", drops)
	}
}

// available absent is "take the types in samples"; available [] is "nothing now",
// even when samples carry types. Both survive a round trip through CollectReply.
func TestAvailableAbsentVersusEmpty(t *testing.T) {
	samples := []*types.MetricSample{{MetricType: "x", Value: 1, Unit: "u"}}
	absent, err := json.Marshal(CollectReply{Seq: 1, Samples: samples})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(absent), "available") {
		t.Fatalf("nil Available encoded as %s", absent)
	}
	empty, err := json.Marshal(CollectReply{Seq: 1, Available: []types.MetricType{}, Samples: samples})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(empty), `"available":[]`) {
		t.Fatalf("empty Available encoded as %s", empty)
	}
	_, a, _ := stampReply("p", "n1", 1, rawLine(t, string(absent)))
	if len(a) != 1 || a[0] != "x" {
		t.Errorf("absent available = %v, want the sample types", a)
	}
	_, a, _ = stampReply("p", "n1", 1, rawLine(t, string(empty)))
	if a == nil || len(a) != 0 {
		t.Errorf("empty available = %#v, want present and empty", a)
	}
	_, a, _ = stampReply("p", "n1", 1, rawLine(t, `{"seq":1,"available":null,"samples":[{"metric_type":"x","value":1,"unit":"u"}]}`))
	if len(a) != 1 {
		t.Errorf("null available = %v, want it read as absent", a)
	}
}

func asStrings(mts []types.MetricType) []string {
	out := make([]string, len(mts))
	for i, m := range mts {
		out[i] = string(m)
	}
	return out
}
