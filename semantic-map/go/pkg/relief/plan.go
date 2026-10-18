package relief

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/DiyazY/di-agent/pkg/policy"
	"github.com/DiyazY/di-agent/pkg/runtime"
	"github.com/DiyazY/di-agent/pkg/spec"
	"github.com/DiyazY/di-agent/pkg/statemap"
)

// The node-level properties relief reads. Pressure is what triggers it, because
// a node at full use with nobody waiting is busy rather than in trouble; usage
// is a share of node capacity in [0,1], and so is each subject's
// cpu_utilization@<subject>.
const (
	NodePressure = "cpu_pressure_ratio"
	NodeUsage    = "cpu_utilization"
)

// Basis says what a candidate's ranking or a plan's prediction rests on: the
// map's relationships, or only the workload's share of node CPU.
const (
	BasisMap   = "map"
	BasisUsage = "usage"
)

// noCores is why nothing can be planned on a machine whose core count is unknown.
const noCores = "no cpu_cores fact: shares cannot become cores"

// usageCaveat is carried by a decision whose prediction the map could not make.
const usageCaveat = "predicted from usage share: the map has no relationship for this workload"

// Candidate is a workload that might be throttled, with what removing it is
// predicted to do to node pressure. Drop is zero for a Basis "usage" candidate:
// the map says nothing about it, and its rank comes from Share.
type Candidate struct {
	Subject string  `json:"subject"`
	Share   float64 `json:"share"`
	Drop    float64 `json:"drop"`
	Basis   string  `json:"basis"`
}

// Plan is one step on one workload: the limit it gets and the drop in node
// pressure that step is predicted to bring. EstimateID is empty for a Basis
// "usage" plan, which no estimate produced. Effect is what the prediction was
// scaled by: what acting on the workload has done before against what was
// predicted, 1 with no history.
type Plan struct {
	Subject    string  `json:"subject"`
	FromCores  float64 `json:"from_cores"`
	ToCores    float64 `json:"to_cores"`
	Predicted  float64 `json:"predicted"`
	EstimateID string  `json:"estimate_id,omitempty"`
	Basis      string  `json:"basis"`
	Rule       int     `json:"rule"`
	Effect     float64 `json:"effect"`
}

// targetOf is the workload as the policy sees it: its kind is the subject's part
// before the colon, so a rule written for pods never matches a unit.
func targetOf(w runtime.Workload) policy.Target {
	kind, _, _ := strings.Cut(w.Subject, ":")
	return policy.Target{Kind: kind, Name: w.Name, Labels: w.Labels, Self: w.Self}
}

// actionable reports whether the runtime can cap this workload's CPU and the
// policy lets it. A Can entry with a reason is the runtime saying why it cannot.
func actionable(w runtime.Workload, pol *policy.Policy) bool {
	reason, listed := w.Can[runtime.ActionCPULimit]
	return listed && reason == "" && pol.For(targetOf(w)).Allows(runtime.ActionCPULimit)
}

// share is the subject's current share of node CPU. A property that is not
// active says nothing about what the workload is doing now.
func share(m *statemap.Map, subject string) (float64, bool) {
	p, ok := m.Property(NodeUsage + "@" + subject)
	if !ok || p.Status != statemap.Active {
		return 0, false
	}
	return p.Value, true
}

// Attribute ranks actionable, unfrozen, non-victim workloads by Drop (Basis
// "map"), falling back to Share (Basis "usage") for those the estimate has no
// influence for. Every map-ranked candidate comes before every usage-ranked one,
// because a ranking the map supports is evidence and a share is only a guess.
// A workload using less than VictimShare is left out whatever its own pressure:
// one that waits a lot but uses little is suffering the contention, not causing
// it.
func Attribute(m *statemap.Map, ws []runtime.Workload, pol *policy.Policy, c spec.CPURelief) []Candidate {
	var byMap, byUsage []Candidate
	for _, w := range ws {
		if !actionable(w, pol) {
			continue
		}
		s, ok := share(m, w.Subject)
		if !ok || s < c.VictimShare {
			continue
		}
		cand := Candidate{Subject: w.Subject, Share: s, Basis: BasisUsage}
		res := m.Estimate(statemap.EstimateRequest{Target: NodePressure, Without: []string{w.Subject}})
		if res.Err == "" && res.Hypothetical != nil && influencedBy(res, w.Subject) {
			cand.Basis, cand.Drop = BasisMap, -res.Hypothetical.Delta
			byMap = append(byMap, cand)
			continue
		}
		byUsage = append(byUsage, cand)
	}
	sort.SliceStable(byMap, func(i, j int) bool {
		if byMap[i].Drop != byMap[j].Drop {
			return byMap[i].Drop > byMap[j].Drop
		}
		return byMap[i].Subject < byMap[j].Subject
	})
	sort.SliceStable(byUsage, func(i, j int) bool {
		if byUsage[i].Share != byUsage[j].Share {
			return byUsage[i].Share > byUsage[j].Share
		}
		return byUsage[i].Subject < byUsage[j].Subject
	})
	return append(byMap, byUsage...)
}

// influencedBy reports whether the subject's own CPU usage reaches the target
// through a relationship with a known strength. It is the same source the plan's
// prediction assumes a new value for, so a "map" rank and a "map" prediction
// rest on one relationship; one with no strength yet contributes nothing to the
// projection, so it is no basis for a rank.
func influencedBy(res statemap.EstimateResult, subject string) bool {
	for _, inf := range res.Influences {
		if inf.Known && inf.Source == NodeUsage+"@"+subject {
			return true
		}
	}
	return false
}

// MakePlan picks the first candidate whose step clears floors and min_effect.
// ok=false with why when none does; why lists each candidate's reason, so a
// refusal says what was considered.
//
// The step is Step below current usage, bounded by the policy floor: never
// below Floor.CPUCores, never below Floor.OfOriginal of the original limit (of
// current usage when there is none), and never below the workload's CPU request. A Basis "map" candidate is predicted by
// assuming its usage scaled to the new limit; a Basis "usage" candidate, which
// the map cannot predict, by its share of the node's pressure per unit of
// usage. effect scales a prediction by what acting on the subject has done before
// against predictions of the same basis; 1 means no history.
func MakePlan(m *statemap.Map, cands []Candidate, ws []runtime.Workload, pol *policy.Policy, c spec.CPURelief, effect func(subject, basis string) float64) (p Plan, ok bool, why string) {
	return makePlan(m, cands, ws, pol, c, effect, nil)
}

// makePlan is MakePlan for a loop that has already capped some workloads:
// originals holds, for each, the limit it had before the agent first touched it,
// which is what the of_original floor is a share of. A workload's current limit
// is then the agent's own cap, and a step that would not go below it is no step.
func makePlan(m *statemap.Map, cands []Candidate, ws []runtime.Workload, pol *policy.Policy, c spec.CPURelief, effect func(subject, basis string) float64, originals map[string]float64) (p Plan, ok bool, why string) {
	n, ok := coreCount(m)
	if !ok {
		return Plan{}, false, noCores
	}
	if len(cands) == 0 {
		return Plan{}, false, "no candidate: no actionable workload uses victim_share or more of node CPU"
	}
	byID := make(map[string]runtime.Workload, len(ws))
	for _, w := range ws {
		byID[w.Subject] = w
	}
	var reasons []string
	for _, cand := range cands {
		w, ok := byID[cand.Subject]
		if !ok {
			reasons = append(reasons, cand.Subject+": not a workload the runtime lists")
			continue
		}
		eff := pol.For(targetOf(w))
		from := cand.Share * n
		original := from
		if o, ok := originals[cand.Subject]; ok {
			original = o
		} else if w.CPU.Limit != nil {
			original = *w.CPU.Limit
		}
		floorCores, floorShare := eff.Settings.Floor.Values()
		to := math.Max(from*(1-c.Step), math.Max(floorCores, floorShare*original))
		if w.CPU.Request != nil {
			// A limit below the request is refused by the orchestrator, so the request is a floor.
			to = math.Max(to, *w.CPU.Request)
		}
		if to >= from {
			reasons = append(reasons, fmt.Sprintf("%s: floor reached (%.3f cores in use, floor %.3f)", cand.Subject, from, to))
			continue
		}
		if w.CPU.Limit != nil && to >= *w.CPU.Limit {
			reasons = append(reasons, fmt.Sprintf("%s: already capped at %.3f cores, at or below the step's %.3f", cand.Subject, *w.CPU.Limit, to))
			continue
		}
		plan := Plan{Subject: cand.Subject, FromCores: from, ToCores: to, Basis: cand.Basis, Rule: eff.Rule,
			Effect: effect(cand.Subject, cand.Basis)}
		switch cand.Basis {
		case BasisMap:
			res := m.Estimate(statemap.EstimateRequest{
				Target: NodePressure,
				Assume: map[string]float64{NodeUsage + "@" + cand.Subject: cand.Share * to / from},
			})
			if res.Err != "" || res.Hypothetical == nil {
				reasons = append(reasons, fmt.Sprintf("%s: no prediction (%s)", cand.Subject, res.Err))
				continue
			}
			plan.Predicted, plan.EstimateID = -res.Hypothetical.Delta*plan.Effect, res.DecisionID
		default:
			pressure, okP := m.Property(NodePressure)
			usage, okU := m.Property(NodeUsage)
			if !okP || !okU || usage.Value <= 0 {
				reasons = append(reasons, cand.Subject+": no node pressure or usage to predict from")
				continue
			}
			plan.Predicted = cand.Share * c.Step * (pressure.Value / usage.Value) * plan.Effect
		}
		if plan.Predicted < c.MinEffect {
			reasons = append(reasons, fmt.Sprintf("%s: predicted drop %.4f is below min_effect %g", cand.Subject, plan.Predicted, c.MinEffect))
			continue
		}
		return plan, true, ""
	}
	return Plan{}, false, strings.Join(reasons, "; ")
}

// coreCount is the machine's core count, which turns a share into cores.
func coreCount(m *statemap.Map) (float64, bool) {
	f, ok := m.Fact("cpu_cores")
	if !ok || f.Number == nil || *f.Number <= 0 {
		return 0, false
	}
	return *f.Number, true
}
