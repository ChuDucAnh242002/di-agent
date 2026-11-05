package statemap

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// admitHeadroom is the share of the node's CPU and of its memory admission leaves
// free. Past it, a pod owed CPU or memory waits on what others hold, which is what
// relief relieves; admitting into it would be admitting work relief must then act on.
const admitHeadroom = 0.10

// Basis says where an admission's demand came from.
type Basis string

const (
	// BasisMeasured: the workload is a pod the map has seen here, and its demand is
	// what was observed of it.
	BasisMeasured Basis = "measured"
	// BasisDeclared: anything else, and its demand is what the requester declared.
	BasisDeclared Basis = "declared"
)

// AdmitRequest asks whether this node can take a workload.
type AdmitRequest struct {
	ID string
	// From is the node asking, when one is named; it is recorded, not weighed.
	From string
	// Workload optionally names the subject. Only a pod (pod:<uid>) is measured here:
	// its uid is the cluster's, so pod:<uid> in this map is that workload. A unit or a
	// group (group:<name>, from cluster-wide labels; every node has group:system) names
	// something each node has its own of, so it is recorded and priced on the declared
	// demand.
	Workload string
	// CPUCores and MemoryBytes are the declared demand, >= 0.
	CPUCores    float64
	MemoryBytes float64
	// CPUEnter and MemoryEnter are the relief levers' enter levels for
	// cpu_contention_ratio and memory_contention_ratio; 0 means no such lever, and
	// that contention is not checked.
	CPUEnter    float64
	MemoryEnter float64
}

// AdmitResult is the answer with its trace.
type AdmitResult struct {
	DecisionID string `json:"decision_id"`
	Revision   uint64 `json:"revision"`
	Accepted   bool   `json:"accepted"`
	Reason     string `json:"reason"`
	Basis      Basis  `json:"basis,omitempty"`
	// CPUAfter and MemoryAfter are the node shares projected with the workload; nil
	// when the answer came before a projection was made.
	CPUAfter    *float64 `json:"cpu_after,omitempty"`
	MemoryAfter *float64 `json:"memory_after,omitempty"`
	Rationale   string   `json:"rationale"`
	Caveats     []string `json:"caveats,omitempty"`
	// Err is set when the request was refused before anything was decided.
	Err string `json:"error,omitempty"`
}

// Admit answers whether this node can take a workload, from its own map, and records
// the answer with what it read. It decides; nothing is scheduled.
//
// It does not go through Estimate: the contention relief acts on is a measured
// stall, not a function of demand, so it cannot be projected for a workload that is
// not here yet, and Estimate's assume substitutes a property the map already holds.
// Admission instead asks whether the node keeps its headroom: the node's CPU and
// memory shares plus the workload's must stay at or below 1 - admitHeadroom, and a
// node at or above a lever's enter level declines outright.
func (m *Map) Admit(req AdmitRequest) AdmitResult {
	for name, v := range map[string]float64{"cpu_cores": req.CPUCores, "memory_bytes": req.MemoryBytes} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return AdmitResult{Err: name + " must be a finite number >= 0"}
		}
	}
	if req.ID != "" {
		if _, used := m.journal.Decision(req.ID); used {
			return AdmitResult{Err: "decision id " + req.ID + " is already used"}
		}
	}
	id := req.ID
	if id == "" {
		// The request is the question, so it is part of the name of the answer: two
		// different requests at one revision must not share a record.
		h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%g|%g|%g|%g", req.From, req.Workload, req.CPUCores, req.MemoryBytes, req.CPUEnter, req.MemoryEnter)))
		id = "adm-" + strconv.FormatUint(m.Revision(), 10) + "-" + hex.EncodeToString(h[:])[:8]
	}
	what := req.Workload
	if what == "" {
		what = "a workload"
	}
	question := fmt.Sprintf("admit %s declaring %g CPU cores and %g bytes of memory", what, req.CPUCores, req.MemoryBytes)
	if req.From != "" {
		question += " for " + req.From
	}
	b := m.Decide(id, question)
	b.Input("request", map[string]any{"from": req.From, "workload": req.Workload, "cpu_cores": req.CPUCores, "memory_bytes": req.MemoryBytes})
	b.Input("thresholds", map[string]float64{"cpu_enter": req.CPUEnter, "memory_enter": req.MemoryEnter, "headroom": admitHeadroom})

	res := AdmitResult{}
	answer := func(accepted bool, reason string) AdmitResult {
		res.Accepted, res.Reason = accepted, reason
		b.Note("%s", reason)
		a := map[string]any{"accepted": accepted, "reason": reason, "basis": string(res.Basis),
			"cpu_enter": req.CPUEnter, "memory_enter": req.MemoryEnter, "limit": 1 - admitHeadroom}
		if res.CPUAfter != nil {
			a["cpu_after"], a["memory_after"] = *res.CPUAfter, *res.MemoryAfter
		}
		d := b.Commit(a)
		res.DecisionID, res.Revision, res.Rationale, res.Caveats = d.ID, d.Revision, d.Rationale, d.Caveats
		return res
	}

	// 1. The workload: what the map saw of it here, if anything. A pod with any share
	// still active is already counted in the node's own shares and has nothing to
	// admit, so it is answered before any capacity or contention check; projecting it
	// would price it twice. A pod seen with both shares is priced on them; with one
	// (the cgroup collector reports memory from its first tick, CPU only from its
	// second), on the declared demand, saying which share is missing.
	res.Basis = BasisDeclared
	var measured [2]float64
	var declaredWhy string
	switch {
	case req.Workload == "":
		declaredWhy = "no workload was named"
	case !strings.HasPrefix(req.Workload, "pod:"):
		declaredWhy = req.Workload + " is not a pod, so what this map holds under that name is this node's own, not the workload's"
	default:
		ids := [2]string{"cpu_utilization@" + req.Workload, "memory_utilization@" + req.Workload}
		var seen, missing []string
		var last time.Time
		for i, pid := range ids {
			if p, ok := m.Property(pid); !ok || p.Status == Retired {
				missing = append(missing, pid)
				continue
			}
			p, _ := b.Property(pid)
			if p.Status == Active {
				return answer(true, req.Workload+" is already running here: "+pid+" is active")
			}
			measured[i] = p.Value
			seen = append(seen, pid)
			if p.LastObserved.After(last) {
				last = p.LastObserved
			}
		}
		switch len(seen) {
		case 2:
			res.Basis = BasisMeasured
			b.Caveat("demand of %s was measured when it last ran here, %s ago", req.Workload, b.at.Sub(last).Round(time.Second))
		case 1:
			declaredWhy = "the map holds " + seen[0] + " but not " + missing[0]
		default:
			declaredWhy = "the map has not seen " + req.Workload + " here"
		}
	}
	if res.Basis == BasisDeclared {
		if req.CPUCores == 0 && req.MemoryBytes == 0 {
			return answer(false, "nothing to price: "+declaredWhy+", and no demand was declared")
		}
		b.Caveat("demand was declared by the requester, not observed: %s", declaredWhy)
	}

	// 2. The machine: how much of it there is turns a declared demand into shares.
	capacity := map[string]float64{}
	for _, f := range b.Facts() {
		if f.Number != nil && *f.Number > 0 {
			capacity[f.Key] = *f.Number
		}
	}
	for _, k := range []string{"cpu_cores", "memory_total"} {
		if capacity[k] == 0 {
			return answer(false, "the map has no fact "+k+", so a demand cannot be read against this machine")
		}
	}

	// 3. What the node uses now. A property seeded from the specification and never
	// observed holds an assumption, not a reading; a stale one, a reading of the past.
	node := map[string]float64{}
	for _, k := range []string{"cpu_utilization", "memory_utilization"} {
		p, ok := b.Property(k)
		if !ok || p.NObservations == 0 || p.Status == Retired {
			return answer(false, "the map has no reading of node "+k)
		}
		if p.Status == Stale {
			return answer(false, "the map's reading of node "+k+" is stale")
		}
		node[k] = p.Value
	}

	// 4. A node already contended is where relief would be acting. One whose contention
	// reading has gone stale cannot say it is not.
	for _, c := range []struct {
		id, kind string
		enter    float64
	}{{"cpu_contention_ratio", "cpu", req.CPUEnter}, {"memory_contention_ratio", "memory", req.MemoryEnter}} {
		if c.enter <= 0 {
			b.Caveat("no relief lever for %s, so %s was not checked", c.kind, c.id)
			continue
		}
		p, ok := b.Property(c.id)
		if !ok || p.NObservations == 0 || p.Status == Retired {
			continue // not a reading; b.Property has said so in a caveat
		}
		if p.Status == Stale {
			return answer(false, "the map's reading of "+c.id+" is stale, so the node's contention is unknown")
		}
		if p.Value >= c.enter {
			return answer(false, fmt.Sprintf("the node is contended: %s %.3f is at or above relief's enter %.3f", c.id, p.Value, c.enter))
		}
	}

	cpuDemand, memDemand := measured[0], measured[1]
	if res.Basis == BasisDeclared {
		cpuDemand = req.CPUCores / capacity["cpu_cores"]
		memDemand = req.MemoryBytes / capacity["memory_total"]
	}
	b.Note("%s demand: %.4f of the node's CPU and %.4f of its memory", res.Basis, cpuDemand, memDemand)

	// 5. The projection, against the headroom.
	cpuAfter, memAfter := node["cpu_utilization"]+cpuDemand, node["memory_utilization"]+memDemand
	res.CPUAfter, res.MemoryAfter = &cpuAfter, &memAfter
	limit := 1 - admitHeadroom
	if cpuAfter > limit {
		return answer(false, fmt.Sprintf("cpu would reach %.3f of the node, past %.2f", cpuAfter, limit))
	}
	if memAfter > limit {
		return answer(false, fmt.Sprintf("memory would reach %.3f of the node, past %.2f", memAfter, limit))
	}
	return answer(true, fmt.Sprintf("cpu would reach %.3f and memory %.3f of the node, within %.2f", cpuAfter, memAfter, limit))
}
