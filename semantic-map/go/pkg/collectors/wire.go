package collectors

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/DiyazY/di-agent/pkg/ingest/client"
	"github.com/DiyazY/di-agent/pkg/types"
	"go.yaml.in/yaml/v3"
)

// ProtocolVersion is the wire protocol both transports speak. A plugin answering
// hello with any other version is refused rather than guessed at.
const ProtocolVersion = 1

// Hello is sent once when a plugin is started or reconnected, before any collect.
type Hello struct {
	Op        string         `json:"op"`
	Protocol  int            `json:"protocol"`
	Name      string         `json:"name"`
	NodeID    string         `json:"node_id"`
	IntervalS int            `json:"interval_s"`
	Config    map[string]any `json:"config"`
}

// HelloReply accepts the hello with the plugin's protocol, or refuses it with Error.
type HelloReply struct {
	Protocol int    `json:"protocol"`
	Error    string `json:"error,omitempty"`
}

// CollectRequest asks for one batch. Seq is echoed so a late reply to an earlier
// request cannot be taken for this one.
type CollectRequest struct {
	Op      string `json:"op"`
	Seq     uint64 `json:"seq"`
	NowUnix int64  `json:"now_unix"`
}

// CollectReply is one batch. Available, when present, is the full set of types the
// plugin can produce now; when absent it is the set of types in Samples. An empty
// Samples is "no data right now"; Error is a fault.
type CollectReply struct {
	Seq       uint64                `json:"seq"`
	Available []types.MetricType    `json:"available,omitempty"`
	Samples   []*types.MetricSample `json:"samples"`
	Error     string                `json:"error,omitempty"`
}

// configJSON turns an entry's YAML config block into the JSON object sent in hello.
// An absent block is an empty object, so a plugin can always index config.
func configJSON(n *yaml.Node) (map[string]any, error) {
	m := map[string]any{}
	if n == nil || n.Kind == 0 {
		return m, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config must be a mapping")
	}
	if err := n.Decode(&m); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	// A nested mapping with non-string keys decodes to a map JSON cannot encode;
	// find that now, at startup, rather than at the first hello.
	if _, err := json.Marshal(m); err != nil {
		return nil, fmt.Errorf("config is not representable as JSON: %w", err)
	}
	return m, nil
}

// stamp fills what the agent, not the plugin, is authoritative for: the source is
// the entry name, a missing node id is the agent's, and a missing event id is the
// same deterministic hash an in-process collector or the ingest client would use.
// A foreign node id is left in place so the supervisor counts it as a violation.
func stamp(name, nodeID string, s *types.MetricSample) {
	s.Source = name
	if s.NodeID == "" {
		s.NodeID = nodeID
	}
	if s.EventID == "" {
		s.EventID = client.EventID(name, nodeID, s.Subject, string(s.MetricType), time.Unix(s.TimestampUnix, 0))
	}
}

// stampReply stamps a successful reply's samples and works out the types the
// plugin declares available. Null entries are dropped. The returned slice is
// never nil, so "no data" stays distinguishable from a fault.
//
// A sample with no timestamp takes nowUnix, the now_unix of the request it
// answers, before its event id is computed: hashed at time zero it would carry
// the same event id every tick, and the map would drop every sample after the
// first as a duplicate.
func stampReply(name, nodeID string, nowUnix int64, r *CollectReply) ([]*types.MetricSample, []types.MetricType) {
	out := make([]*types.MetricSample, 0, len(r.Samples))
	for _, s := range r.Samples {
		if s == nil {
			continue
		}
		if s.TimestampUnix == 0 {
			s.TimestampUnix = nowUnix
		}
		stamp(name, nodeID, s)
		out = append(out, s)
	}
	if r.Available != nil {
		return out, r.Available
	}
	avail := []types.MetricType{}
	seen := map[types.MetricType]bool{}
	for _, s := range out {
		if !seen[s.MetricType] {
			seen[s.MetricType] = true
			avail = append(avail, s.MetricType)
		}
	}
	return out, avail
}

// resolveTimeout is the per-request timeout of an entry: the configured one, or
// min(interval/2, 5s) when it is unset, so both transports default alike.
func resolveTimeout(env Env, timeout time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}
	timeout = defaultTimeout
	if half := env.Interval / 2; half > 0 && half < timeout {
		timeout = half
	}
	return timeout
}

// checkHello decides whether a hello reply accepts the session: a refusal or a
// protocol other than ProtocolVersion is a hello failure.
func checkHello(r HelloReply) error {
	if r.Error != "" {
		return fmt.Errorf("hello refused: %s", r.Error)
	}
	if r.Protocol != ProtocolVersion {
		return fmt.Errorf("plugin speaks protocol %d, agent speaks %d", r.Protocol, ProtocolVersion)
	}
	return nil
}

// checkCollect decides whether a collect reply is a usable answer to request seq:
// an error reply is a fault, and so is a reply to another request.
func checkCollect(r *CollectReply, seq uint64) error {
	if r.Error != "" {
		return fmt.Errorf("plugin error: %s", r.Error)
	}
	if r.Seq != seq {
		return fmt.Errorf("reply seq %d, want %d", r.Seq, seq)
	}
	return nil
}
