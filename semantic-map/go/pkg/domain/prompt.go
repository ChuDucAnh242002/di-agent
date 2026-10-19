package domain

import (
	"fmt"
	"strings"
)

// PromptSummary renders what this spec declares — constructs, the metrics routed to
// each, and the propositions between them — as text for a language model's system
// prompt. It is the only place a prompt learns a construct's name, so the model is
// told about the domain this agent loaded and no other. A nil spec says that none is
// loaded, which is a state the agent runs in.
func (s *Spec) PromptSummary() string {
	if s == nil || len(s.Constructs) == 0 {
		return "No domain model is loaded: there are no constructs or propositions. Reason from " +
			"properties and from relationships the map discovered, learned or measured."
	}
	routed := map[string][]string{}
	for _, r := range s.MetricRouting {
		routed[r.ConstructID] = append(routed[r.ConstructID], r.MetricType)
	}
	var b strings.Builder
	b.WriteString("Constructs (derived properties summarising the metrics routed to them):\n")
	for _, c := range s.Constructs {
		fmt.Fprintf(&b, "  - %s (%s): %s", c.ConstructID, c.Name, c.Description)
		if ms := routed[c.ConstructID]; len(ms) > 0 {
			fmt.Fprintf(&b, "; fed by %s", strings.Join(ms, ", "))
		}
		b.WriteString("\n")
	}
	if len(s.Propositions) > 0 {
		b.WriteString("Propositions (declared structure; every strength is measured on this machine or asserted):\n")
		for _, p := range s.Propositions {
			fmt.Fprintf(&b, "  - %s: %s -> %s, %s. %s\n", p.PropositionID, p.FromConstruct, p.ToConstruct, p.Direction, p.Description)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
