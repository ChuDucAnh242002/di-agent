package agentmain

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"maps"
	"net/http"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/DiyazY/di-agent/pkg/admission"
	"github.com/DiyazY/di-agent/pkg/peers"
	"github.com/DiyazY/di-agent/pkg/relief"
	"github.com/DiyazY/di-agent/pkg/runtime"
	"github.com/DiyazY/di-agent/pkg/spec"
	"github.com/DiyazY/di-agent/pkg/statemap"
)

// submissionCap bounds the submissions GET /tasks lists.
const submissionCap = 200

// admitter answers offers from this node's map and walks a submission through
// its peers until one accepts.
type admitter struct {
	m       *statemap.Map
	self    admission.Self
	ids     *admission.IDs
	tracker *admission.Tracker
	domains func() []admission.Domain // one per relief loop
	peers   func() ([]*peers.Descriptor, error)
	client  *peers.Client

	mu     sync.Mutex   // guards recent and answered
	recent []submission // newest first, at most submissionCap
	// answered counts this node's own verdicts, local and for peers: only false
	// accepts are observable, so the decline rate is reported beside the outcomes.
	answered map[string]int
}

// submission is one POST /tasks and where it ended. Each hop's URL and decision
// id resolve on the node that decided (<url>/state/decisions/<id>); an empty URL
// is this node.
type submission struct {
	At         time.Time       `json:"at"`
	Name       string          `json:"name"`
	AcceptedBy *acceptor       `json:"accepted_by"`
	DecisionID string          `json:"decision_id,omitempty"`
	Chain      []admission.Hop `json:"chain"`
}

type acceptor struct {
	Node string `json:"node"`
	URL  string `json:"url"`
}

// buildAdmission wires admission to relief: its loops are the domains, its
// shares and moved-workload test confound outcomes, and its actions are relief
// acting in an outcome window.
func buildAdmission(cfg spec.Spec, m *statemap.Map, rt runtime.Runtime, loops []*relief.Loop, reg *peers.Registry) *admitter {
	ids := admission.NewIDs(time.Now())
	tr := admission.NewTracker(admission.TrackerConfig{
		Map: m,
		Workloads: func(ctx context.Context) (map[string]string, error) {
			ws, err := rt.Workloads(ctx)
			if err != nil {
				return nil, err
			}
			out := make(map[string]string, len(ws))
			for _, w := range ws {
				out[w.Name] = w.Subject
			}
			return out, nil
		},
		Shares: func(usage string) map[string]float64 { return relief.Shares(m, usage) },
		// 0.25 is the default levers' step, the move relief itself counts as a change.
		Moved: func(usage string, at map[string]float64, except string) []string {
			return relief.Moved(m, usage, at, except, 0.25)
		},
		Acted: reliefActed(loops),
		IDs:   ids,
		Now:   time.Now,
		Logf:  log.Printf,
	})
	return &admitter{m: m, self: admission.Self{NodeID: cfg.Node.ID, Arch: goruntime.GOARCH}, ids: ids, tracker: tr,
		domains: reliefDomains(loops), peers: reg.List, client: peers.NewClient(5 * time.Second)}
}

// startAdmissionLoop checks pending accepts on relief's interval, so an outcome
// is judged no later than relief would see the same pressure.
func startAdmissionLoop(ctx context.Context, t *admission.Tracker, every time.Duration) {
	if every <= 0 {
		every = 10 * time.Second
	}
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				t.Tick(ctx)
			}
		}
	}()
}

// reliefDomains reads each relief loop as an admission domain, afresh on every
// call because the phase moves. With relief off there are none, and every offer
// declines: a node never admits blind.
func reliefDomains(loops []*relief.Loop) func() []admission.Domain {
	return func() []admission.Domain {
		out := make([]admission.Domain, 0, len(loops))
		for _, l := range loops {
			d, c := l.Descriptor(), l.Lever()
			out = append(out, admission.Domain{Action: d.Action, Resource: d.Resource, Relieves: d.Relieves,
				Usage: d.Usage, Capacity: d.Capacity, Exit: c.Exit, Enter: c.Enter, Settle: c.Settle,
				Phase: string(l.Status().Phase)})
		}
		return out
	}
}

// reliefActed says whether the loop for action changed anything after since: an
// outcome window relief acted in cannot be credited to the admitted workload.
func reliefActed(loops []*relief.Loop) func(string, time.Time) bool {
	return func(action string, since time.Time) bool {
		for _, l := range loops {
			if l.Descriptor().Action != action {
				continue
			}
			if slices.ContainsFunc(l.Actions(), func(a relief.Action) bool { return a.ActedAt.After(since) }) {
				return true
			}
		}
		return false
	}
}

func registerAdmissionRoutes(mux *http.ServeMux, a *admitter) {
	mux.HandleFunc("POST /tasks", func(w http.ResponseWriter, r *http.Request) {
		o, ok := readOffer(w, r)
		if !ok {
			return
		}
		writeJSON(w, a.submit(r.Context(), o))
	})
	mux.HandleFunc("POST /offers", func(w http.ResponseWriter, r *http.Request) {
		o, ok := readOffer(w, r)
		if !ok {
			return
		}
		writeJSON(w, a.answer(o))
	})
	mux.HandleFunc("GET /tasks", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		subs := slices.Clone(a.recent)
		answered := maps.Clone(a.answered)
		a.mu.Unlock()
		if answered == nil {
			answered = map[string]int{}
		}
		writeJSON(w, map[string]any{"submissions": subs, "answered": answered,
			"pending": a.tracker.PendingList(), "outcomes": a.tracker.Outcomes()})
	})
}

// readOffer decodes and validates an offer; a malformed one is refused before
// anything is decided or recorded.
func readOffer(w http.ResponseWriter, r *http.Request) (admission.Offer, bool) {
	var o admission.Offer
	if err := requireJSON(r); err != nil {
		writeError(w, http.StatusUnsupportedMediaType, err.Error())
		return o, false
	}
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
		writeError(w, http.StatusBadRequest, "decode offer: "+err.Error())
		return o, false
	}
	if err := o.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return o, false
	}
	return o, true
}

// submit decides locally, then puts the offer to each peer in id order until one
// accepts. Only the node that took the submission walks the chain, so the chain
// starts here whatever the request carried.
func (a *admitter) submit(ctx context.Context, o admission.Offer) submission {
	o.Chain = nil
	s := submission{At: time.Now(), Name: o.Name}
	hop := a.answer(o)
	o.Chain = append(o.Chain, hop)
	if hop.Verdict != admission.Accept {
		list, err := a.peers()
		if err != nil {
			o.Chain = append(o.Chain, admission.Hop{Verdict: admission.Unreachable, Reason: "listing peers: " + err.Error()})
		}
		slices.SortFunc(list, func(x, y *peers.Descriptor) int { return strings.Compare(x.ID, y.ID) })
		for _, p := range list {
			// Peer ids are derived from URLs, not node ids, so the URL is what can be
			// checked here; a node already in the chain answers already_asked itself.
			if slices.ContainsFunc(o.Chain, func(h admission.Hop) bool { return h.URL == p.URL }) {
				continue
			}
			hop, err = a.client.Offer(ctx, p.URL, o)
			if err != nil {
				hop = admission.Hop{Verdict: admission.Unreachable, Reason: err.Error()}
			}
			hop.URL = p.URL
			o.Chain = append(o.Chain, hop)
			if hop.Verdict == admission.Accept {
				break
			}
		}
	}
	s.Chain = o.Chain
	if last := o.Chain[len(o.Chain)-1]; last.Verdict == admission.Accept {
		s.AcceptedBy, s.DecisionID = &acceptor{Node: last.Node, URL: last.URL}, last.DecisionID
	}

	a.mu.Lock()
	a.recent = append([]submission{s}, a.recent...)
	if len(a.recent) > submissionCap {
		a.recent = a.recent[:submissionCap]
	}
	a.mu.Unlock()
	return s
}

// answer is this node's hop for an offer, counted by verdict.
func (a *admitter) answer(o admission.Offer) admission.Hop {
	h := a.decide(o)
	a.mu.Lock()
	if a.answered == nil {
		a.answered = map[string]int{}
	}
	a.answered[h.Verdict]++
	a.mu.Unlock()
	return h
}

// decide is the hop, the same for a local submission and a peer's: a node already
// in the chain is not asked again, and a name awaiting its outcome is not accepted
// twice.
func (a *admitter) decide(o admission.Offer) admission.Hop {
	if o.Asked(a.self.NodeID, "") {
		return admission.Hop{Node: a.self.NodeID, Verdict: admission.AlreadyAsked, Reason: "already in the chain"}
	}
	if a.tracker.Pending(o.Name) {
		return a.declinePending(o, "")
	}
	doms := a.domains()
	d := admission.Decide(a.m, o, doms, a.self, a.ids.Next())
	if d.Verdict == admission.Accept {
		if err := a.tracker.Accepted(o, d, doms); errors.Is(err, admission.ErrAlreadyPending) {
			// Another offer for the name was registered between the check and
			// here. The accept stays in the journal; the answer is a decline of
			// its own, so the hop's decision says what the hop says.
			return a.declinePending(o, d.ID)
		}
	}
	return admission.Hop{Node: a.self.NodeID, DecisionID: d.ID, Verdict: d.Verdict, Reason: d.Reason}
}

// declinePending records a decline for a name already pending; superseded is the
// accept it replaces when it lost a race, empty otherwise.
func (a *admitter) declinePending(o admission.Offer, superseded string) admission.Hop {
	id := a.ids.Next()
	b := a.m.Decide(id, "admit "+o.Name+"?")
	b.Input("offer", o)
	if superseded != "" {
		b.Input("superseded", superseded)
	}
	reason := admission.ErrAlreadyPending.Error()
	b.Commit(map[string]any{"verdict": admission.Decline, "reason": reason})
	return admission.Hop{Node: a.self.NodeID, DecisionID: id, Verdict: admission.Decline, Reason: reason}
}
