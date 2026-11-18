package agentmain

import (
	"fmt"
	"slices"

	"github.com/DiyazY/di-agent/pkg/statemap"
)

// resolveDecision finds a decision in the bounded journal, then in the journal
// log. from is "journal" or "log". A log that cannot be read or does not verify is
// an error, not a miss: the caller answers 5xx, since 404/410 say neither holds it.
func resolveDecision(j *statemap.Journal, id string) (d *statemap.Decision, from string, ok bool, err error) {
	if d, ok := j.Decision(id); ok {
		return d, "journal", true, nil
	}
	d, ok, err = j.DecisionFromLog(id)
	if err != nil {
		return nil, "", false, fmt.Errorf("journal log: reading %s: %w", id, err)
	}
	if !ok {
		return nil, "", false, nil
	}
	return d, "log", true, nil
}

// resolveChain is Journal.Chain with the log behind it: the chain is walked hop
// by hop through resolveDecision (memory first, then the log), depth-first in cite
// order, each id once; an id neither holds is marked evicted, and a log error on
// any hop is returned. CitedBy comes from the journal when the root is held in
// memory, and is nil otherwise: the log has no reverse index.
func resolveChain(j *statemap.Journal, id string) (statemap.Chain, string, bool, error) {
	d, from, ok, err := resolveDecision(j, id)
	if !ok || err != nil {
		return statemap.Chain{}, "", false, err
	}
	c := statemap.Chain{Decision: d, Cites: []statemap.ChainLink{}}
	if held, ok := j.Chain(id); ok {
		c.CitedBy = held.CitedBy
	}
	seen := map[string]bool{id: true}
	stack := reversed(d.Cites) // an explicit stack: a chain can be long
	for len(stack) > 0 {
		next := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[next] {
			continue
		}
		seen[next] = true
		a, _, held, err := resolveDecision(j, next)
		if err != nil {
			return statemap.Chain{}, "", false, err
		}
		if !held {
			c.Cites = append(c.Cites, statemap.ChainLink{ID: next, Evicted: true})
			continue
		}
		c.Cites = append(c.Cites, statemap.ChainLink{ID: next, Decision: a})
		stack = append(stack, reversed(a.Cites)...)
	}
	return c, from, true, nil
}

func reversed(ids []string) []string {
	out := slices.Clone(ids)
	slices.Reverse(out)
	return out
}
