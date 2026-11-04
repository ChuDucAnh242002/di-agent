package statemap

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"time"
)

// admitHeadroom is the share of the node's CPU and of its memory admission leaves
// free. Past it, a pod owed CPU or memory waits on what others hold, which is what
// relief relieves; admitting into it would be admitting work relief must then act on.
const admitHeadroom = 0.10

// Basis says where an admission's demand came from.
type Basis string

const (
	// BasisMeasured: the map has seen the workload here and its demand is what was observed.
	BasisMeasured Basis = "measured"
	// BasisDeclared: the map has not, and its demand is what the requester declared.
	BasisDeclared Basis = "declared"
)

// AdmitRequest asks whether this node can take a workload.
type AdmitRequest struct {
	ID string
	// From is the node asking, when one is named; it is recorded, not weighed.
	From string
	// Workload optionally names the subject (pod:<uid>, group:<name>); when the map
	// holds it, its observed demand is used instead of the declared one.
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
	DecisionID  string   `json:"decision_id"`
	Revision    uint64   `json:"revision"`
	Accepted    bool     `json:"accepted"`
	Reason      string   `json:"reason"`
	Basis       Basis    `json:"basis,omitempty"`
	CPUAfter    float64  `json:"cpu_after"`
	MemoryAfter float64  `json:"memory_after"`
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
// node already past a lever's enter level declines outright.
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
		d := b.Commit(map[string]any{"accepted": accepted, "reason": reason, "basis": string(res.Basis),
			"cpu_after": res.CPUAfter, "memory_after": res.MemoryAfter,
			"cpu_enter": req.CPUEnter, "memory_enter": req.MemoryEnter, "limit": 1 - admitHeadroom})
		res.DecisionID, res.Revision, res.Rationale, res.Caveats = d.ID, d.Revision, d.Rationale, d.Caveats
		return res
	}

	// 1. The workload: what the map saw of it here, if anything. One still active is
	// already counted in the node's own shares and has nothing to admit, so it is
	// answered before anything could decline it; projecting it would price it twice.
	res.Basis = BasisDeclared
	var measured [2]float64
	if req.Workload != "" {
		cpuID, memID := "cpu_utilization@"+req.Workload, "memory_utilization@"+req.Workload
		cp, cok := m.Property(cpuID)
		mp, mok := m.Property(memID)
		if cok && mok && cp.Status != Retired && mp.Status != Retired {
			cp, _ = b.Property(cpuID)
			mp, _ = b.Property(memID)
			res.Basis = BasisMeasured
			if cp.Status == Active || mp.Status == Active {
				// The node's shares as they stand, for the record; nothing turns on them.
				if p, ok := b.Property("cpu_utilization"); ok {
					res.CPUAfter = p.Value
				}
				if p, ok := b.Property("memory_utilization"); ok {
					res.MemoryAfter = p.Value
				}
				return answer(true, req.Workload+" is already running here")
			}
			measured = [2]float64{cp.Value, mp.Value}
			last := cp.LastObserved
			if mp.LastObserved.After(last) {
				last = mp.LastObserved
			}
			if !last.IsZero() {
				b.Caveat("demand of %s was measured when it last ran here, %s ago", req.Workload, b.at.Sub(last).Round(time.Second))
			} else {
				b.Caveat("demand of %s was measured when it last ran here", req.Workload)
			}
		}
	}
	if res.Basis == BasisDeclared && req.CPUCores == 0 && req.MemoryBytes == 0 {
		return answer(false, "nothing to price: the map has not seen the workload here and no demand was declared")
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
	// observed holds an assumption, not a reading.
	node := map[string]float64{}
	for _, k := range []string{"cpu_utilization", "memory_utilization"} {
		p, ok := b.Property(k)
		if !ok || p.NObservations == 0 || p.Status == Retired {
			return answer(false, "the map has no reading of node "+k)
		}
		node[k] = p.Value
	}

	// 4. A node already contended is where relief would be acting.
	for _, c := range []struct {
		id, kind string
		enter    float64
	}{{"cpu_contention_ratio", "cpu", req.CPUEnter}, {"memory_contention_ratio", "memory", req.MemoryEnter}} {
		if c.enter <= 0 {
			b.Caveat("no relief lever for %s, so %s was not checked", c.kind, c.id)
			continue
		}
		if p, ok := b.Property(c.id); ok && p.NObservations > 0 && p.Status != Retired && p.Value >= c.enter {
			res.CPUAfter, res.MemoryAfter = node["cpu_utilization"], node["memory_utilization"]
			return answer(false, fmt.Sprintf("the node is contended: %s %.3f is at or above relief's enter %.3f", c.id, p.Value, c.enter))
		}
	}

	cpuDemand, memDemand := measured[0], measured[1]
	if res.Basis == BasisDeclared {
		cpuDemand = req.CPUCores / capacity["cpu_cores"]
		memDemand = req.MemoryBytes / capacity["memory_total"]
		b.Caveat("demand was declared by the requester, not observed")
	}
	b.Note("%s demand: %.4f of the node's CPU and %.4f of its memory", res.Basis, cpuDemand, memDemand)

	// 5. The projection, against the headroom.
	res.CPUAfter = node["cpu_utilization"] + cpuDemand
	res.MemoryAfter = node["memory_utilization"] + memDemand
	limit := 1 - admitHeadroom
	if res.CPUAfter > limit {
		return answer(false, fmt.Sprintf("cpu would reach %.3f of the node, past %.2f", res.CPUAfter, limit))
	}
	if res.MemoryAfter > limit {
		return answer(false, fmt.Sprintf("memory would reach %.3f of the node, past %.2f", res.MemoryAfter, limit))
	}
	return answer(true, fmt.Sprintf("cpu would reach %.3f and memory %.3f of the node, within %.2f", res.CPUAfter, res.MemoryAfter, limit))
}
