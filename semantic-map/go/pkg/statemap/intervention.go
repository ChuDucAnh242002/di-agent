package statemap

import (
	"fmt"
	"time"
)

// Interventions is what the agent did to a relationship's From end and saw happen at
// its To end. It is evidence of a different kind from correlation: the agent moved
// one side itself, so the other side's response is not a shared cause.
type Interventions struct {
	Agreed     int `json:"agreed"`     // observed at least half of what was predicted
	Null       int `json:"null"`       // observed less
	Confounded int `json:"confounded"` // something else moved too; says nothing either way
	// AgreedRun and NullRun count unconfounded outcomes since the last structural
	// change or the opposite outcome; Agreed and Null are lifetime totals. Structure
	// is decided by the runs: evidence since the map last changed its mind.
	AgreedRun    int       `json:"agreed_run"`
	NullRun      int       `json:"null_run"`
	Last         time.Time `json:"last,omitzero"`
	LastDecision string    `json:"last_decision,omitempty"`
}

// InterventionOutcome is the verified result of one action the agent took.
type InterventionOutcome struct {
	From, To   string
	Agreed     bool // observed >= half of predicted
	Confounded bool
	DecisionID string
}

func interventionKey(from, to string) string { return from + "->" + to }

// pairRelationshipLocked finds the relationship an intervention pair maps to: sign
// +1, preferring the unlabelled id, then the first by id. A retired one is returned
// only when includeRetired is set and no live one exists.
func (m *Map) pairRelationshipLocked(from, to string, includeRetired bool) *Relationship {
	pick := func(live bool) *Relationship {
		var best *Relationship
		for _, r := range m.relationships {
			if r.From != from || r.To != to || r.Sign != 1 || (r.Status == Retired) == live {
				continue
			}
			if r.ID == RelationshipID(from, to, "") {
				return r
			}
			if best == nil || r.ID < best.ID {
				best = r
			}
		}
		return best
	}
	if r := pick(true); r != nil || !includeRetired {
		return r
	}
	return pick(false)
}

// relationshipViewLocked copies a relationship, attaching its pair's counters when it
// is the relationship that pair maps to. Caller holds the lock.
func (m *Map) relationshipViewLocked(r *Relationship) Relationship {
	out := *r
	out.Interventions = nil
	if r.Sign != 1 {
		return out
	}
	iv, ok := m.interventions[interventionKey(r.From, r.To)]
	if !ok || m.pairRelationshipLocked(r.From, r.To, true) != r {
		return out
	}
	c := *iv
	out.Interventions = &c
	return out
}

// RecordIntervention journals an outcome and changes structure only on unconfounded
// evidence. It returns the relationship id and what changed: "", "created",
// "promoted" or "retired". An Asserted relationship is never promoted or retired.
func (m *Map) RecordIntervention(o InterventionOutcome) (id, changed string, err error) {
	if o.From == "" || o.To == "" || o.From == o.To {
		return "", "", fmt.Errorf("intervention needs two distinct endpoints, got %q -> %q", o.From, o.To)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	key := interventionKey(o.From, o.To)
	iv := m.interventions[key]
	if iv == nil {
		iv = &Interventions{}
		m.interventions[key] = iv
	}
	now := m.now()
	iv.Last, iv.LastDecision = now, o.DecisionID

	live := m.pairRelationshipLocked(o.From, o.To, false)
	target := RelationshipID(o.From, o.To, "")
	if live != nil {
		target = live.ID
	}

	outcome := "null"
	switch {
	case o.Confounded:
		iv.Confounded++
		outcome = "confounded"
	case o.Agreed:
		iv.Agreed++
		iv.AgreedRun++
		iv.NullRun = 0
		outcome = "agreed"
	default:
		iv.Null++
		iv.NullRun++
		iv.AgreedRun = 0
	}
	m.bump(EventIntervention, target, "intervention", map[string]any{
		"outcome": outcome, "decision": o.DecisionID, "agreed": iv.Agreed, "null": iv.Null,
		"confounded": iv.Confounded, "agreed_run": iv.AgreedRun, "null_run": iv.NullRun,
	}, now)

	id = target
	if live == nil {
		id = ""
	}
	switch {
	case outcome == "agreed" && iv.AgreedRun >= 2 && live == nil:
		// Declaring over a retired relationship revives it, so created means live.
		decl := Relationship{From: o.From, To: o.To, Sign: 1, Provenance: Intervened}
		old := m.pairRelationshipLocked(o.From, o.To, true)
		if old != nil {
			decl.Label = old.Label
		}
		if err := m.declareRelationshipLocked(decl, true); err != nil {
			return "", "", err
		}
		iv.AgreedRun, iv.NullRun = 0, 0
		if old != nil {
			m.promoteLocked(old, now)
		}
		return RelationshipID(decl.From, decl.To, decl.Label), "created", nil
	case outcome == "agreed" && iv.AgreedRun >= 2 && live.Provenance != Intervened:
		if !m.promoteLocked(live, now) {
			return id, "", nil
		}
		iv.AgreedRun, iv.NullRun = 0, 0
		return id, "promoted", nil
	case outcome == "null" && iv.NullRun >= 2 && live != nil && live.Provenance == Asserted:
		// An operator's assertion outranks the agent's evidence: the nulls are
		// counted, and the run starts again rather than retiring it.
		iv.AgreedRun, iv.NullRun = 0, 0
		return id, "", nil
	case outcome == "null" && iv.NullRun >= 2 && live != nil:
		m.retireRelationshipLocked(live, "two interventions had no effect", "intervention", now)
		iv.AgreedRun, iv.NullRun = 0, 0
		return id, "retired", nil
	}
	return id, "", nil
}

// promoteLocked marks a seeded, learned or discovered relationship as confirmed by
// intervention, journalled, and reports whether it did. An operator's assertion is
// never overwritten.
func (m *Map) promoteLocked(r *Relationship, at time.Time) bool {
	switch r.Provenance {
	case Seeded, Learned, Discovered:
	default:
		return false
	}
	was := r.Provenance
	r.Provenance = Intervened
	m.bump(EventIntervention, r.ID, "intervention", map[string]any{
		"promoted": true, "provenance": string(Intervened), "was": string(was),
	}, at)
	return true
}
