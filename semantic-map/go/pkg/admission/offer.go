// Package admission holds the wire types and decision rules for map-grounded
// task admission: a submitter offers a workload, a node answers from its own map.
package admission

import (
	"fmt"
	"math"
)

// Hop verdicts. The first two are a node's answer; the others are recorded by
// the submitter when it could not get one.
const (
	Accept       = "accept"
	Decline      = "decline"
	Unreachable  = "unreachable"
	AlreadyAsked = "already_asked"
)

// Offer is a workload a submitter wants some node to run.
type Offer struct {
	// Name is what the submitter will start the workload under; the node
	// resolves it to a subject when the workload arrives.
	Name  string `json:"name"`
	Image string `json:"image,omitempty"` // informational
	// Arch lists the GOARCH values the image runs on; empty means unchecked.
	Arch []string `json:"arch,omitempty"`
	// Demand maps a resource ("cpu" | "memory") to what the workload needs.
	Demand map[string]Demand `json:"demand,omitempty"`
	// Chain is the nodes already asked, in order.
	Chain []Hop `json:"chain,omitempty"`
}

// Demand is submitted, never verified: the receiver cannot check a
// measurement taken elsewhere, so it records the stated Source with it.
type Demand struct {
	Value  float64 `json:"value"`            // cores for "cpu", bytes for "memory"
	Source string  `json:"source"`           // "declared", or "measured on <node>"
	Arch   string  `json:"arch,omitempty"`   // GOARCH measured on; empty when declared
	Capped bool    `json:"capped,omitempty"` // measured under a relief cap, so CPU is understated
}

// Hop is one node's answer in the chain of nodes an offer has been through.
type Hop struct {
	Node       string `json:"node"` // the answering agent's -node-id
	URL        string `json:"url"`  // where it was asked
	DecisionID string `json:"decision_id,omitempty"`
	Verdict    string `json:"verdict"` // accept | decline | unreachable | already_asked
	Reason     string `json:"reason"`
}

// Validate rejects an offer no node could price: no name, a resource the map
// does not model, or a value that is not a finite non-negative number.
func (o Offer) Validate() error {
	if o.Name == "" {
		return fmt.Errorf("offer has no name")
	}
	for k, d := range o.Demand {
		if k != "cpu" && k != "memory" {
			return fmt.Errorf("unknown demand resource %q", k)
		}
		if math.IsNaN(d.Value) || math.IsInf(d.Value, 0) || d.Value < 0 {
			return fmt.Errorf("%s demand %v is not a finite non-negative number", k, d.Value)
		}
	}
	return nil
}

// Asked reports whether the chain already holds a hop for this node or URL, so
// an offer is never put to the same node twice. An empty node id or URL matches
// nothing: unreachable hops carry no node id.
func (o Offer) Asked(nodeID, url string) bool {
	for _, h := range o.Chain {
		if (nodeID != "" && h.Node == nodeID) || (url != "" && h.URL == url) {
			return true
		}
	}
	return false
}
