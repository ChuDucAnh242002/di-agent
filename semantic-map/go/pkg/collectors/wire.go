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
func stampReply(name, nodeID string, r *CollectReply) ([]*types.MetricSample, []types.MetricType) {
	out := make([]*types.MetricSample, 0, len(r.Samples))
	for _, s := range r.Samples {
		if s == nil {
			continue
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
