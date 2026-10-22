package statemap

import (
	"fmt"
	"slices"
	"strings"

	"github.com/DiyazY/di-agent/pkg/types"
)

// AggregateSum makes a derived property the sum of its members.
const AggregateSum = "sum"

// GroupMetrics are the properties a group holds: what its members use of the
// machine, which adds up. Pressures and ratios of a window do not, so a group has none.
var GroupMetrics = []string{"cpu_utilization", "memory_utilization"}

// IsGroup reports whether subject names a group: group:<name>.
func IsGroup(subject string) bool { return strings.HasPrefix(subject, "group:") }

// DeclareGroup makes or updates the group id (group:<name>) over member subjects: for
// each of GroupMetrics a derived property metric@id summing metric@member, recomputed
// from the members and never stored. Declaring it again with other members moves the
// membership, journalled; the members' own properties are untouched. The members need
// not exist yet: a member with no such property contributes nothing until it has one.
func (m *Map) DeclareGroup(id string, members []string, labels map[string]string) error {
	if !IsGroup(id) || !types.ValidSubject(id) {
		return fmt.Errorf("group %q: want group:<name> over [A-Za-z0-9._:-]", id)
	}
	ms := slices.Clone(members)
	slices.Sort(ms)
	ms = slices.Compact(ms)
	for _, s := range ms {
		if !types.ValidSubject(s) || IsGroup(s) {
			return fmt.Errorf("group %s: member %q is not a subject that can belong to a group", id, s)
		}
	}
	if len(ms) == 0 {
		return fmt.Errorf("group %s has no members", id)
	}
	for _, metric := range GroupMetrics {
		mem := make([]string, len(ms))
		for i, s := range ms {
			mem[i] = metric + "@" + s
		}
		if err := m.DeclareProperty(Property{ID: metric + "@" + id, Kind: Derived, Subject: id, Unit: "fraction",
			Range: [2]float64{0, 1}, Aggregate: AggregateSum, Members: mem, Labels: labels, Source: "groups"}); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.recomputeDerivedLocked(m.now())
	m.mu.Unlock()
	return nil
}

// GroupMembers is the subjects group id is made of, sorted; none when it is not a group.
func (m *Map) GroupMembers(id string) []string {
	if !IsGroup(id) {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := map[string]bool{}
	for _, metric := range GroupMetrics {
		p, ok := m.properties[metric+"@"+id]
		if !ok {
			continue
		}
		for _, mem := range p.Members {
			if _, s, ok := strings.Cut(mem, "@"); ok {
				seen[s] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}
