// Package policy says what the agent may do to a workload. Rules are read in
// order: the first one that matches supplies the settings, any rule that
// matches and freezes makes the workload untouchable, and the agent itself is
// always frozen.
package policy

import (
	"fmt"
	"path"

	"github.com/DiyazY/di-agent/pkg/spec"
)

// selfFrozen is why the agent never acts on itself.
const selfFrozen = "built-in: the agent itself"

// Target is a workload as the policy sees it. Kind is the part of the subject
// before the colon (pod, unit).
type Target struct {
	Kind   string
	Name   string
	Labels map[string]string
	Self   bool
}

// Effective is what the policy permits for one target.
type Effective struct {
	Frozen   bool          `json:"frozen"`
	FrozenBy string        `json:"frozen_by,omitempty"`
	Rule     int           `json:"rule"` // first matching rule; -1 = defaults
	Settings spec.Settings `json:"settings"`
}

// Policy is a validated PolicySpec.
type Policy struct {
	spec spec.PolicySpec
}

// New refuses a rule whose name glob does not compile.
func New(p spec.PolicySpec) (*Policy, error) {
	for i, r := range p.Rules {
		if _, err := path.Match(r.Match.Name, ""); err != nil {
			return nil, fmt.Errorf("policy rule %d: name %q: %w", i, r.Match.Name, err)
		}
	}
	return &Policy{spec: p}, nil
}

// For resolves the policy for one target. The first matching rule supplies the
// settings, field by field, with defaults for what it leaves unset.
func (p *Policy) For(t Target) Effective {
	e := Effective{Rule: -1, Settings: copySettings(p.spec.Defaults)}
	if t.Self {
		e.Frozen, e.FrozenBy = true, selfFrozen
	}
	for i, r := range p.spec.Rules {
		if !matches(r.Match, t) {
			continue
		}
		if e.Rule < 0 {
			e.Rule = i
			overlay(&e.Settings, r.Settings)
		}
		if r.Freeze && !e.Frozen {
			e.Frozen, e.FrozenBy = true, fmt.Sprintf("rule %d", i)
		}
	}
	if e.Settings.Floor == nil {
		e.Settings.Floor = &spec.Floor{}
	}
	return e
}

// Allows reports whether the action may be taken: not frozen, allowed, and not
// denied. Deny beats allow.
func (e Effective) Allows(action string) bool {
	return !e.Frozen && contains(e.Settings.Allow, action) && !contains(e.Settings.Deny, action)
}

func matches(m spec.Match, t Target) bool {
	if m.Kind != "" && m.Kind != t.Kind {
		return false
	}
	if m.Name != "" {
		if ok, err := path.Match(m.Name, t.Name); err != nil || !ok {
			return false
		}
	}
	for k, want := range m.Labels {
		got, ok := t.Labels[k]
		if !ok || !contains(want, got) {
			return false
		}
	}
	return true
}

// overlay sets onto s every field r leaves set.
func overlay(s *spec.Settings, r spec.Settings) {
	if r.Allow != nil {
		s.Allow = append([]string(nil), r.Allow...)
	}
	if r.Deny != nil {
		s.Deny = append([]string(nil), r.Deny...)
	}
	if r.Floor != nil {
		f := *r.Floor
		s.Floor = &f
	}
	if r.MaxActionsPerHour != 0 {
		s.MaxActionsPerHour = r.MaxActionsPerHour
	}
	if r.Cooldown != 0 {
		s.Cooldown = r.Cooldown
	}
}

// copySettings keeps a caller from altering the policy through an Effective.
func copySettings(s spec.Settings) spec.Settings {
	var out spec.Settings
	out.MaxActionsPerHour, out.Cooldown = s.MaxActionsPerHour, s.Cooldown
	overlay(&out, spec.Settings{Allow: s.Allow, Deny: s.Deny, Floor: s.Floor})
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
