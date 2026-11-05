package admission

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/DiyazY/di-agent/pkg/statemap"
)

// Domain is one relief lever this node runs, as admission reads it: the actuator
// descriptor's metric names, the lever's levels, and its loop's current phase.
type Domain struct {
	Action, Resource, Relieves, Usage, Capacity string
	Exit, Enter                                 float64
	Settle                                      time.Duration
	// Phase is the loop's relief.Phase as a string; this package does not import
	// relief, which imports peers, which carries offers.
	Phase string
}

// Self is the answering node.
type Self struct{ NodeID, Arch string }

// Line is one domain's evaluation: what was read, both predictions of Relieves
// after the workload arrives, and which one decided.
type Line struct {
	Domain, Phase, Basis, Reason string
	Share, Pressure, Usage, Exit float64
	MapPred, UsagePred, K        *float64
	KFrom                        []string
	Pass                         bool
}

// Decision is the answer to an offer and the evaluation behind it.
type Decision struct {
	ID, Verdict, Reason string
	Lines               []Line
}

// Decide answers an offer from this node's map and records the answer under id.
// It accepts only when every domain predicts its pressure at or below Exit, the
// level relief lets caps go at: admitting up to Enter would hold pressure in the
// band where caps never release.
func Decide(m *statemap.Map, o Offer, domains []Domain, self Self, id string) Decision {
	b := m.Decide(id, "admit "+o.Name+"?")
	b.Input("offer", cloneOffer(o))
	lines := make([]Line, len(domains))
	for i, d := range domains {
		lines[i] = Line{Domain: d.Relieves, Phase: d.Phase, Exit: d.Exit}
	}
	answer := func(verdict, reason string) Decision {
		b.Input("domains", slices.Clone(lines))
		b.Commit(map[string]any{"verdict": verdict, "reason": reason})
		return Decision{ID: id, Verdict: verdict, Reason: reason, Lines: lines}
	}

	if len(o.Arch) > 0 && !slices.ContainsFunc(o.Arch, func(a string) bool { return strings.EqualFold(a, self.Arch) }) {
		return answer(Decline, "cannot run on "+self.Arch)
	}
	// A node never admits blind.
	if len(domains) == 0 {
		return answer(Decline, "no admission domains")
	}
	// Headroom created by throttling is not spare capacity.
	for _, d := range domains {
		if d.Phase == "relieving" || d.Phase == "releasing" {
			return answer(Decline, "relief active on "+d.Relieves)
		}
	}
	for _, d := range domains {
		dem, ok := o.Demand[d.Resource]
		if !ok {
			return answer(Decline, "unknown "+d.Resource+" demand")
		}
		// No data relates one architecture's CPU to another's; memory carries over.
		if d.Resource == "cpu" && dem.Arch != "" && !strings.EqualFold(dem.Arch, self.Arch) {
			return answer(Decline, "cpu demand not from this architecture")
		}
	}

	// The headroom gate is Admit's: a calm node offered more than it has left reads
	// 0 on every pressure-based prediction, so the shares must fit first.
	req := statemap.AdmitRequest{ID: id + "-gate",
		CPUCores: o.Demand["cpu"].Value, MemoryBytes: o.Demand["memory"].Value}
	for _, d := range domains {
		switch d.Relieves {
		case "cpu_contention_ratio":
			req.CPUEnter = d.Enter
		case "memory_contention_ratio":
			req.MemoryEnter = d.Enter
		}
	}
	gate := m.Admit(req)
	b.Input("gate", gate.DecisionID)
	if gate.Err != "" {
		return answer(Decline, gate.Err)
	}
	if !gate.Accepted {
		return answer(Decline, gate.Reason)
	}

	facts := map[string]statemap.Fact{}
	for _, f := range b.Facts() {
		facts[f.Key] = f
	}
	subjects := m.Census().Subjects
	reason := ""
	for i, d := range domains {
		lines[i] = predict(b, m, d, o.Demand[d.Resource].Value, facts[d.Capacity], subjects)
		if !lines[i].Pass && reason == "" {
			reason = lines[i].Reason
		}
	}
	if reason != "" {
		return answer(Decline, reason)
	}
	return answer(Accept, "every domain stays at or below its exit level")
}

// predict evaluates one domain. Both predictions are made whenever their inputs
// exist, so the record shows what the basis that did not decide would have said.
func predict(b *statemap.DecisionBuilder, m *statemap.Map, d Domain, demand float64, capacity statemap.Fact, subjects int) Line {
	l := Line{Domain: d.Relieves, Phase: d.Phase, Exit: d.Exit}
	var notes []string
	fail := func(why string) Line {
		l.Reason = strings.Join(append(notes, why), "; ")
		return l
	}

	pressure, hasPressure := reading(b, d.Relieves)
	usage, _ := reading(b, d.Usage)
	l.Usage = usage
	if capacity.Number == nil || *capacity.Number <= 0 {
		return fail("no " + d.Capacity + " fact")
	}
	l.Share = demand / *capacity.Number
	if !hasPressure {
		// Node contention is the worst of the subjects', so a node running none has
		// no reading; with subjects present the gap is a gap.
		if subjects > 0 {
			return fail("no " + d.Relieves + " reading")
		}
		notes = append(notes, "no subjects")
	}
	l.Pressure = pressure

	// k is the largest slope relief measured on this node by acting on a subject's
	// usage, so the map prediction leans towards declining.
	for _, e := range m.MeasuredEffects(d.Relieves) {
		if !strings.HasPrefix(e.From, d.Usage+"@") {
			continue
		}
		switch {
		case l.K == nil || e.Effect > *l.K:
			k := e.Effect
			l.K, l.KFrom = &k, []string{e.Relationship}
		case e.Effect == *l.K:
			l.KFrom = append(l.KFrom, e.Relationship)
		}
	}
	if l.K != nil {
		p := clamp01(pressure + *l.K*l.Share)
		l.MapPred = &p
	}
	if usage > 0 {
		p := clamp01(pressure * (usage + l.Share) / usage)
		l.UsagePred = &p
	}

	var pred float64
	switch {
	case l.MapPred != nil:
		l.Basis, pred = "map", *l.MapPred
	case l.UsagePred != nil:
		l.Basis, pred = "usage", *l.UsagePred
	default:
		return fail("no basis to predict " + d.Relieves)
	}
	if pred > d.Exit {
		return fail(fmt.Sprintf("%s would reach %.4f on the %s basis, above exit %.4f", d.Relieves, pred, l.Basis, d.Exit))
	}
	l.Pass = true
	l.Reason = strings.Join(notes, "; ")
	return l
}

// reading is a property's value when it rests on an observation; a seeded or
// retired property is not a reading.
func reading(b *statemap.DecisionBuilder, id string) (float64, bool) {
	p, ok := b.Property(id)
	if !ok || p.NObservations == 0 || p.Status == statemap.Retired {
		return 0, false
	}
	return p.Value, true
}

// cloneOffer copies an offer for the record, which must not change if the caller
// later edits its offer.
func cloneOffer(o Offer) Offer {
	o.Arch = slices.Clone(o.Arch)
	o.Demand = maps.Clone(o.Demand)
	o.Chain = slices.Clone(o.Chain)
	return o
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }
