package plugin

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/collectors"
)

// The request type here is a copy of the agent's wire messages, kept separate so
// a plugin does not link the agent's internals. This keeps the copy honest: the
// hello the agent sends reaches setup with every field, the interval included to
// the millisecond, and the protocol versions agree.
func TestHelloDecodesTheAgents(t *testing.T) {
	agent := collectors.Hello{Op: "hello", Protocol: collectors.ProtocolVersion, Name: "gpu", NodeID: "rpi-2",
		IntervalMS: 2500, Config: map[string]any{"device": float64(0), "names": []any{"a"}}}
	wire, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}
	var req request
	if err := json.Unmarshal(wire, &req); err != nil {
		t.Fatal(err)
	}
	want := Hello{Name: "gpu", NodeID: "rpi-2", Interval: 2500 * time.Millisecond, Config: agent.Config}
	if got := req.hello(); !reflect.DeepEqual(got, want) {
		t.Fatalf("setup would see %+v, want %+v", got, want)
	}
	if req.Op != "hello" || req.Protocol != ProtocolVersion {
		t.Fatalf("op %q protocol %d; agent speaks %d", req.Op, req.Protocol, ProtocolVersion)
	}
	coll, _ := json.Marshal(collectors.CollectRequest{Op: "collect", Seq: 9, NowUnix: 77})
	var c request
	if err := json.Unmarshal(coll, &c); err != nil || c.Op != "collect" || c.Seq != 9 || c.NowUnix != 77 {
		t.Fatalf("collect request decoded as %+v (%v)", c, err)
	}
}
