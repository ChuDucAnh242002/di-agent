package admission

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/DiyazY/di-agent/pkg/statemap"
)

const (
	// ArrivalWindow is how long an accept waits for its workload to appear.
	ArrivalWindow = 5 * time.Minute
	// PendingCap bounds the accepts awaiting an outcome.
	PendingCap = 32
	// outcomeCap bounds the outcomes kept for listing.
	outcomeCap = 200
)

// Outcomes of an accept.
const (
	Held       = "held"
	Breached   = "breached"
	Confounded = "confounded"
	NotArrived = "not_arrived"
)

// ErrAlreadyPending is returned for an accept whose name awaits an outcome.
var ErrAlreadyPending = errors.New("already pending")

// Outcome is what became of one accept.
type Outcome struct {
	Name      string        `json:"name"`
	AcceptID  string        `json:"accept_id"` // the accept's decision, which holds both predictions
	OutcomeID string        `json:"outcome_id"`
	Subject   string        `json:"subject"`
	Result    string        `json:"result"`
	Lines     []OutcomeLine `json:"lines"`
	At        time.Time     `json:"at"`
}

// OutcomeLine is one domain's check: the observed rise of Relieves since the
// arrival snapshot against each basis's predicted rise. Rises and errors are set
// only on scored outcomes (held, breached); an error is predicted minus observed.
type OutcomeLine struct {
	Domain       string   `json:"domain"`
	ObservedRise float64  `json:"observed_rise"`
	MapRise      *float64 `json:"map_rise,omitempty"`
	UsageRise    *float64 `json:"usage_rise,omitempty"`
	MapErr       *float64 `json:"map_err,omitempty"`
	UsageErr     *float64 `json:"usage_err,omitempty"`
	Breached     bool     `json:"breached"`
}

// TrackerConfig wires a tracker to the agent. Workloads, Shares and Moved are
// injected because the runtime and relief packages import peers, which imports
// this one.
type TrackerConfig struct {
	Map *statemap.Map
	// Workloads maps each workload's Name to its subject, as the runtime lists them.
	Workloads func(ctx context.Context) (map[string]string, error)
	// Shares is relief.Shares on Map; Moved is relief.Moved on Map at the lever's step.
	Shares func(usage string) map[string]float64
	Moved  func(usage string, at map[string]float64, except string) []string
	// Acted says whether relief acted in the action's domain since the time given.
	Acted func(action string, since time.Time) bool
	IDs   *IDs
	Now   func() time.Time
	Logf  func(string, ...any)
}

// Tracker checks each accept against what the node observes once the workload
// arrives. It changes nothing: an outcome is recorded and listed, no more.
type Tracker struct {
	c TrackerConfig

	// tick serialises Tick, which alone touches an entry's subject and snapshot.
	tick sync.Mutex

	mu       sync.Mutex
	pending  []*entry // oldest first
	outcomes []Outcome
}

type entry struct {
	name, acceptID string
	at             time.Time
	domains        []Domain
	lines          []Line // the accept's, aligned with domains

	subject string
	snap    *snapshot
}

// snapshot is the node at arrival, per domain: the baseline the outcome is
// measured from and the shares other workloads are checked against.
type snapshot struct {
	at       time.Time
	relieves []float64
	usage    []float64
	shares   []map[string]float64
}

func NewTracker(c TrackerConfig) *Tracker {
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
	return &Tracker{c: c}
}

// Accepted registers an accept for its outcome check.
func (t *Tracker) Accepted(o Offer, d Decision, domains []Domain) error {
	lines := make([]Line, len(domains))
	for i, dom := range domains {
		for _, l := range d.Lines {
			if l.Domain == dom.Relieves {
				lines[i] = l
			}
		}
	}
	e := &entry{name: o.Name, acceptID: d.ID, at: t.c.Now(), domains: slices.Clone(domains), lines: lines}

	// One critical section for the check and the insert, so two offers for one
	// name cannot both be registered.
	t.mu.Lock()
	defer t.mu.Unlock()
	if slices.ContainsFunc(t.pending, func(p *entry) bool { return p.name == o.Name }) {
		return ErrAlreadyPending
	}
	if len(t.pending) >= PendingCap {
		t.c.Logf("admission: pending cap %d reached, dropped %s", PendingCap, t.pending[0].name)
		t.pending = t.pending[1:]
	}
	t.pending = append(t.pending, e)
	return nil
}

// Pending says whether an accept for name awaits its outcome.
func (t *Tracker) Pending(name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.ContainsFunc(t.pending, func(p *entry) bool { return p.name == name })
}

// PendingAccept is an accept awaiting its outcome.
type PendingAccept struct {
	Name       string    `json:"name"`
	AcceptID   string    `json:"accept_id"`
	AcceptedAt time.Time `json:"accepted_at"`
	// Arrived says the workload has appeared and been snapshotted; its settle runs.
	Arrived bool   `json:"arrived"`
	Subject string `json:"subject,omitempty"`
}

// PendingList lists the accepts awaiting an outcome, oldest first.
func (t *Tracker) PendingList() []PendingAccept {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]PendingAccept, len(t.pending))
	for i, e := range t.pending {
		out[i] = PendingAccept{Name: e.name, AcceptID: e.acceptID, AcceptedAt: e.at, Arrived: e.snap != nil, Subject: e.subject}
	}
	return out
}

// Outcomes lists recent outcomes, newest first.
func (t *Tracker) Outcomes() []Outcome {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := slices.Clone(t.outcomes)
	slices.Reverse(out)
	return out
}

// Tick snapshots accepts whose workload has arrived, judges those whose settle
// has elapsed, and expires those whose workload never came.
func (t *Tracker) Tick(ctx context.Context) {
	t.tick.Lock()
	defer t.tick.Unlock()

	t.mu.Lock()
	work := slices.Clone(t.pending)
	t.mu.Unlock()
	if len(work) == 0 {
		return
	}

	// Without the list, arrivals cannot be seen, so nothing is expired either.
	names, err := t.c.Workloads(ctx)
	if err != nil {
		t.c.Logf("admission: listing workloads: %v", err)
	}
	now := t.c.Now()
	var done []Outcome
	var finished []*entry
	for _, e := range work {
		if e.snap == nil && err == nil {
			if s, ok := names[e.name]; ok && t.reporting(e, s) {
				snap := t.snapshot(e, now)
				// Under mu, so PendingList can read arrival while Tick runs.
				t.mu.Lock()
				e.subject, e.snap = s, snap
				t.mu.Unlock()
			} else if now.Sub(e.at) > ArrivalWindow {
				done = append(done, t.record(e, NotArrived, nil, nil, now))
				finished = append(finished, e)
			}
			continue
		}
		if e.snap != nil && now.Sub(e.snap.at) >= settle(e.domains) {
			done = append(done, t.judge(e, now))
			finished = append(finished, e)
		}
	}
	if len(finished) == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = slices.DeleteFunc(t.pending, func(p *entry) bool { return slices.Contains(finished, p) })
	t.outcomes = append(t.outcomes, done...)
	if n := len(t.outcomes) - outcomeCap; n > 0 {
		t.outcomes = slices.Delete(t.outcomes, 0, n)
	}
}

// reporting says whether subject's usage is in the map on every domain: before
// that, the snapshot would miss part of the workload.
func (t *Tracker) reporting(e *entry, subject string) bool {
	for _, d := range e.domains {
		p, ok := t.c.Map.Property(d.Usage + "@" + subject)
		if !ok || p.Status == statemap.Retired {
			return false
		}
	}
	return true
}

func (t *Tracker) snapshot(e *entry, now time.Time) *snapshot {
	s := &snapshot{at: now}
	for _, d := range e.domains {
		s.relieves = append(s.relieves, value(t.c.Map, d.Relieves))
		s.usage = append(s.usage, value(t.c.Map, d.Usage))
		s.shares = append(s.shares, t.c.Shares(d.Usage))
	}
	return s
}

// judge checks a settled accept: confounded first, since a breach another
// workload or relief caused says nothing about the prediction.
func (t *Tracker) judge(e *entry, now time.Time) Outcome {
	var moved []string
	acted := false
	for i, d := range e.domains {
		moved = append(moved, t.c.Moved(d.Usage, e.snap.shares[i], e.subject)...)
		acted = acted || t.c.Acted(d.Action, e.snap.at)
	}
	slices.Sort(moved)
	moved = slices.Compact(moved)

	lines := make([]OutcomeLine, len(e.domains))
	for i, d := range e.domains {
		lines[i].Domain = d.Relieves
	}
	if len(moved) > 0 || acted {
		return t.record(e, Confounded, lines, map[string]any{"moved": moved, "relief_acted": acted}, now)
	}

	result := Held
	settled := make([]float64, len(e.domains))
	for i, d := range e.domains {
		l, a := &lines[i], e.lines[i]
		v := value(t.c.Map, d.Relieves)
		settled[i] = v
		l.ObservedRise = v - e.snap.relieves[i]
		// Relief's trigger is strictly above Enter.
		if v > d.Enter {
			l.Breached, result = true, Breached
		}
		l.MapRise, l.MapErr = rise(a.MapPred, a.Pressure, l.ObservedRise)
		l.UsageRise, l.UsageErr = rise(a.UsagePred, a.Pressure, l.ObservedRise)
	}
	return t.record(e, result, lines, map[string]any{"relieves_at_arrival": slices.Clone(e.snap.relieves),
		"usage_at_arrival": slices.Clone(e.snap.usage), "relieves_now": settled}, now)
}

// record commits the outcome decision, linked to the accept.
func (t *Tracker) record(e *entry, result string, lines []OutcomeLine, inputs map[string]any, now time.Time) Outcome {
	o := Outcome{Name: e.name, AcceptID: e.acceptID, OutcomeID: t.c.IDs.Next(), Subject: e.subject,
		Result: result, Lines: lines, At: now}
	b := t.c.Map.Decide(o.OutcomeID, "outcome of "+e.name+"?")
	b.Input("accept", e.acceptID)
	b.Input("subject", e.subject)
	b.Input("lines", slices.Clone(lines))
	for k, v := range inputs {
		b.Input(k, v)
	}
	b.Commit(map[string]any{"outcome": result})
	return o
}

// rise is a basis's predicted rise over the pressure it was made from, and its
// error against the observed rise; nil when the basis made no prediction.
func rise(pred *float64, pressure, observed float64) (*float64, *float64) {
	if pred == nil {
		return nil, nil
	}
	r := *pred - pressure
	e := r - observed
	return &r, &e
}

// value reads a property as Decide does: absent, seeded or retired reads 0, the
// calm a node with no reporting subject is in.
func value(m *statemap.Map, id string) float64 {
	p, ok := m.Property(id)
	if !ok || p.NObservations == 0 || p.Status == statemap.Retired {
		return 0
	}
	return p.Value
}

func settle(domains []Domain) time.Duration {
	var s time.Duration
	for _, d := range domains {
		s = max(s, d.Settle)
	}
	return s
}
