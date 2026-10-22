package plugin

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/types"
)

// drive runs serve over the given request lines and returns its reply lines and
// result. serve reads until EOF, so the input ends where the agent would close stdin.
func drive(t *testing.T, setup func(Hello) (CollectFunc, error), lines ...string) ([]string, error) {
	t.Helper()
	var out strings.Builder
	err := serve(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out, setup)
	got := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if out.Len() == 0 {
		got = nil
	}
	return got, err
}

const hello = `{"op":"hello","protocol":1,"name":"fx","node_id":"n1","interval_s":10,"config":{"device":0}}`

func TestServeHelloError(t *testing.T) {
	got, err := drive(t, func(Hello) (CollectFunc, error) {
		return nil, errors.New("no device")
	}, hello, `{"op":"collect","seq":1,"now_unix":5}`)
	if len(got) != 1 || got[0] != `{"error":"no device"}` {
		t.Fatalf("replies = %q, want one hello refusal", got)
	}
	if err == nil || !strings.Contains(err.Error(), "no device") {
		t.Fatalf("serve returned %v, want the setup error", err)
	}
}

func TestServeHelloPassesFields(t *testing.T) {
	var seen Hello
	got, err := drive(t, func(h Hello) (CollectFunc, error) {
		seen = h
		return func(uint64, time.Time) ([]*types.MetricSample, []types.MetricType, error) { return nil, nil, nil }, nil
	}, hello)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	if len(got) != 1 || got[0] != `{"protocol":1}` {
		t.Fatalf("replies = %q", got)
	}
	if seen.Name != "fx" || seen.NodeID != "n1" || seen.IntervalS != 10 || seen.Config["device"] != float64(0) {
		t.Fatalf("setup saw %+v", seen)
	}
}

func TestServeCollect(t *testing.T) {
	var gotSeq uint64
	var gotNow time.Time
	collect := func(seq uint64, now time.Time) ([]*types.MetricSample, []types.MetricType, error) {
		gotSeq, gotNow = seq, now
		return []*types.MetricSample{{MetricType: "fx", Value: 1, TimestampUnix: now.Unix(), Unit: "u"}},
			[]types.MetricType{"fx", "fy"}, nil
	}
	got, err := drive(t, func(Hello) (CollectFunc, error) { return collect, nil },
		hello, `{"op":"collect","seq":42,"now_unix":1761160000}`)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("replies = %q", got)
	}
	var r struct {
		Seq       uint64                `json:"seq"`
		Available []types.MetricType    `json:"available"`
		Samples   []*types.MetricSample `json:"samples"`
		Error     string                `json:"error"`
	}
	if err := json.Unmarshal([]byte(got[1]), &r); err != nil {
		t.Fatalf("reply %q: %v", got[1], err)
	}
	if r.Seq != 42 || len(r.Samples) != 1 || len(r.Available) != 2 || r.Error != "" {
		t.Fatalf("reply = %+v", r)
	}
	if gotSeq != 42 || gotNow.Unix() != 1761160000 {
		t.Fatalf("collect saw seq %d now %v", gotSeq, gotNow)
	}
}

// "No data right now" is an empty list on the wire, never null and never an error.
func TestServeNoDataIsEmptySamples(t *testing.T) {
	got, _ := drive(t, func(Hello) (CollectFunc, error) {
		return func(uint64, time.Time) ([]*types.MetricSample, []types.MetricType, error) { return nil, nil, nil }, nil
	}, hello, `{"op":"collect","seq":3,"now_unix":1}`)
	if len(got) != 2 || got[1] != `{"seq":3,"samples":[]}` {
		t.Fatalf("replies = %q", got)
	}
}

func TestServeCollectError(t *testing.T) {
	got, err := drive(t, func(Hello) (CollectFunc, error) {
		return func(uint64, time.Time) ([]*types.MetricSample, []types.MetricType, error) {
			return nil, nil, errors.New("device gone")
		}, nil
	}, hello, `{"op":"collect","seq":9,"now_unix":1}`, `{"op":"collect","seq":10,"now_unix":2}`)
	if err != nil {
		t.Fatalf("a collect error is a reply, not the end of serving: %v", err)
	}
	if len(got) != 3 || got[1] != `{"seq":9,"error":"device gone"}` {
		t.Fatalf("replies = %q", got)
	}
}

func TestServeCollectBeforeHello(t *testing.T) {
	got, _ := drive(t, func(Hello) (CollectFunc, error) { return nil, nil }, `{"op":"collect","seq":1,"now_unix":1}`)
	if len(got) != 1 || !strings.Contains(got[0], `"seq":1`) || !strings.Contains(got[0], `"error"`) {
		t.Fatalf("replies = %q", got)
	}
}

func TestServeReturnsOnEOF(t *testing.T) {
	err := serve(strings.NewReader(""), io.Discard, func(Hello) (CollectFunc, error) { return nil, nil })
	if err != nil {
		t.Fatalf("serve on EOF = %v, want nil", err)
	}
}

func TestServeRejectsMalformedLine(t *testing.T) {
	_, err := drive(t, func(Hello) (CollectFunc, error) { return nil, nil }, "not json")
	if err == nil {
		t.Fatalf("serve accepted a line that is not JSON")
	}
}
