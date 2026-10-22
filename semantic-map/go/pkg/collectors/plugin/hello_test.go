package plugin_test

import (
	"encoding/json"
	"testing"

	"github.com/DiyazY/di-agent/pkg/collectors"
	"github.com/DiyazY/di-agent/pkg/collectors/plugin"
)

// plugin.Hello is a copy of collectors.Hello, kept separate so a plugin does not
// link the agent's internals. This keeps the copy honest: both decode one wire
// message to the same fields and encode back to the same bytes.
func TestHelloMarshalsLikeTheAgents(t *testing.T) {
	agent := collectors.Hello{Op: "hello", Protocol: collectors.ProtocolVersion, Name: "gpu", NodeID: "rpi-2",
		IntervalS: 10, Config: map[string]any{"device": float64(0), "names": []any{"a"}}}
	wire, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}
	var p plugin.Hello
	if err := json.Unmarshal(wire, &p); err != nil {
		t.Fatal(err)
	}
	back, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != string(wire) {
		t.Fatalf("plugin.Hello re-encodes the agent's hello differently:\n agent:  %s\n plugin: %s", wire, back)
	}
	if p.Protocol != plugin.ProtocolVersion {
		t.Fatalf("plugin.ProtocolVersion %d, agent speaks %d", plugin.ProtocolVersion, p.Protocol)
	}
}
