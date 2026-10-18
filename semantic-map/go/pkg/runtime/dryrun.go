package runtime

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

func init() {
	Register("dry-run", func(env Env) (Runtime, error) { return NewDryRun(env), nil })
}

// DryRun is a runtime that changes nothing outside the process: workloads and
// limits live in memory. It is what the agent runs against until an orchestrator
// is configured, and what the loops' tests run against.
type DryRun struct {
	env Env

	mu        sync.Mutex
	workloads map[string]Workload
	seen      map[string]bool // subjects ever admitted, so a removed one is not re-admitted
	undos     map[string]Undo // unreverted changes by subject
}

// NewDryRun builds a dry-run runtime.
func NewDryRun(env Env) *DryRun {
	return &DryRun{
		env:       env,
		workloads: map[string]Workload{},
		seen:      map[string]bool{},
		undos:     map[string]Undo{},
	}
}

// Name is "dry-run".
func (d *DryRun) Name() string { return "dry-run" }

// Workloads lists the workloads, first admitting any subject in the map it has not
// seen: named for the subject, with no labels and no limit, able to take a CPU cap.
func (d *DryRun) Workloads(ctx context.Context) ([]Workload, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.env.Subjects != nil {
		for _, s := range d.env.Subjects() {
			if d.seen[s] {
				continue
			}
			d.seen[s] = true
			d.workloads[s] = Workload{Subject: s, Name: s, Can: map[string]string{ActionCPULimit: ""}}
		}
	}
	out := make([]Workload, 0, len(d.workloads))
	for _, w := range d.workloads {
		out = append(out, cloneWorkload(w))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Subject < out[j].Subject })
	return out, nil
}

// Apply sets the workload's CPU limit and records how to undo it. Any action other
// than ActionCPULimit is an error, and an unknown subject is ErrGone.
func (d *DryRun) Apply(ctx context.Context, subject string, c Change) (Undo, error) {
	if c.Action != ActionCPULimit {
		return Undo{}, fmt.Errorf("dry-run cannot apply action %q", c.Action)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workloads[subject]
	if !ok {
		return Undo{}, fmt.Errorf("%s: %w", subject, ErrGone)
	}
	u := Undo{
		Runtime:    "dry-run",
		NodeID:     d.env.NodeID,
		Subject:    subject,
		Action:     c.Action,
		Original:   cloneResource(w.CPU),
		Applied:    c.CPULimit,
		Expires:    c.Expires,
		DecisionID: c.DecisionID,
	}
	limit := c.CPULimit
	w.CPU.Limit = &limit
	d.workloads[subject] = w
	d.undos[subject] = u
	return u, nil
}

// Revert restores the resource the undo recorded and forgets the change. A removed
// workload is ErrGone, and its change is forgotten too: there is nothing left to
// give back.
func (d *DryRun) Revert(ctx context.Context, u Undo) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.undos, u.Subject)
	w, ok := d.workloads[u.Subject]
	if !ok {
		return fmt.Errorf("%s: %w", u.Subject, ErrGone)
	}
	w.CPU = cloneResource(u.Original)
	d.workloads[u.Subject] = w
	return nil
}

// Changed lists nodeID's unreverted changes, soonest-expiring first, then by subject.
func (d *DryRun) Changed(ctx context.Context, nodeID string) ([]Undo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Undo
	for _, u := range d.undos {
		if u.NodeID == nodeID {
			out = append(out, cloneUndo(u))
		}
	}
	sortUndos(out)
	return out, nil
}

// Put adds or replaces a workload. For tests.
func (d *DryRun) Put(w Workload) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen[w.Subject] = true
	d.workloads[w.Subject] = cloneWorkload(w)
}

// PutUndo seeds an outstanding change, such as one another node made. For tests.
func (d *DryRun) PutUndo(u Undo) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.undos[u.Subject] = cloneUndo(u)
}

// Remove deletes a workload, as when its pod is gone. It is not admitted again.
func (d *DryRun) Remove(subject string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen[subject] = true
	delete(d.workloads, subject)
}

// Applied lists every outstanding change on any node, in Changed's order.
func (d *DryRun) Applied() []Undo {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Undo, 0, len(d.undos))
	for _, u := range d.undos {
		out = append(out, cloneUndo(u))
	}
	sortUndos(out)
	return out
}

func sortUndos(us []Undo) {
	sort.Slice(us, func(i, j int) bool {
		if !us[i].Expires.Equal(us[j].Expires) {
			return us[i].Expires.Before(us[j].Expires)
		}
		return us[i].Subject < us[j].Subject
	})
}

func cloneFloat(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneResource(r Resource) Resource {
	return Resource{Request: cloneFloat(r.Request), Limit: cloneFloat(r.Limit)}
}

func cloneUndo(u Undo) Undo {
	u.Original = cloneResource(u.Original)
	return u
}

func cloneWorkload(w Workload) Workload {
	w.CPU = cloneResource(w.CPU)
	w.Labels = cloneMap(w.Labels)
	w.Can = cloneMap(w.Can)
	return w
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
