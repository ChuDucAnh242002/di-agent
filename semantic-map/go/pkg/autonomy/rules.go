package autonomy

import (
	"fmt"

	"github.com/DiyazY/di-agent/pkg/types"
)

// Rule is a tier 0 check: something that settles a candidate without a model. Apply
// returns the action and why when the rule applies, and ok false when it does not.
// Rules run in the order configured; the first that applies decides.
type Rule interface {
	Name() string
	Apply(Evidence) (action, why string, ok bool)
}

func init() {
	RegisterRule("composition", func() Rule { return composition{} })
	RegisterRule("persistent-correlation", func() Rule { return persistentCorrelation{} })
}

// composition confirms a directed composition candidate: a part against the whole
// it is summed into — a workload's CPU against the node's, a physical disk's writes
// against the node's I/O — is true by definition, and it is what a counterfactual
// without= answers from. The other kinds (two metrics of one subject, one metric on
// two subjects) make accounting or a shared driver likelier, which is a judgement
// about this system and evidence for the model tiers, not a rule.
type composition struct{}

func (composition) Name() string { return "composition" }

func (composition) Apply(ev Evidence) (string, string, bool) {
	c := ev.Candidate
	if c.Kind == types.CandidateKindComposition && !c.Undirected {
		return types.ReviewConfirm, "composition: the source is part of the target by definition", true
	}
	return "", "", false
}

type persistentCorrelation struct{}

func (persistentCorrelation) Name() string { return "persistent-correlation" }

func (persistentCorrelation) Apply(ev Evidence) (string, string, bool) {
	c := ev.Candidate
	if c.PersistentWindows < types.PersistentWindowsRequired || len(c.Caveats) != 0 {
		return "", "", false
	}
	if c.Undirected {
		return types.ReviewAssociate, fmt.Sprintf("persistent association: %d full qualifying windows; no orientation inferred and excluded from estimates and actuation", c.PersistentWindows), true
	}
	return types.ReviewAssociate, fmt.Sprintf("persistent association: %d full qualifying windows; correlation does not establish a causal direction and is excluded from estimates and actuation", c.PersistentWindows), true
}

// rules is tier 0: the first configured rule that applies.
func (r *Reviewer) rules(ev Evidence) (outcome, bool) {
	for _, rule := range r.cfg.Rules {
		if action, why, ok := rule.Apply(ev); ok {
			association := action == types.ReviewAssociate
			if association {
				action = types.ReviewConfirm
			}
			return outcome{tier: 0, by: types.ReviewerRules, model: rule.Name(), action: action, why: why, association: association}, true
		}
	}
	return outcome{}, false
}
