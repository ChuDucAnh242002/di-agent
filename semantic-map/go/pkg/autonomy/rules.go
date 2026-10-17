package autonomy

import "github.com/DiyazY/di-agent/pkg/types"

// rules is tier 0: what can be settled without a model.
//
// There is one rule. A composition candidate is a part against the whole it is
// summed into — a workload's CPU against the node's, a physical disk's writes
// against the node's I/O — so the relationship is true by definition, and it is
// what a counterfactual without= answers from. It is confirmed. Everything else,
// including two metrics of one subject and one metric on two subjects, needs a
// judgement about this system, which is what the model tiers are for: the kind is
// in the evidence they are given.
func rules(ev Evidence) (outcome, bool) {
	c := ev.Candidate
	if c.Kind == types.CandidateKindComposition && !c.Undirected {
		return outcome{tier: 0, by: types.ReviewerRules, action: types.ReviewConfirm,
			why: "composition: the source is part of the target by definition"}, true
	}
	return outcome{}, false
}
