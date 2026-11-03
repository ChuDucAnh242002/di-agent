package minimal

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/DiyazY/di-agent/pkg/stats"
	"github.com/DiyazY/di-agent/pkg/types"
)

type discoveryPolicy struct {
	Threshold, MinSpread, FDR      float64
	MinPairs, BufferSize, MaxPairs int
	PairWindow                     time.Duration
	MaxGap                         time.Duration
}

type runCheckpoint struct {
	Pairs, Windows, Sign int
	Kind                 string
}

func checkpointRun(r persistenceRun) runCheckpoint {
	return runCheckpoint{r.pairs, r.windows, r.sign, r.kind}
}

func (s runCheckpoint) restore(size int) (persistenceRun, error) {
	if s.Pairs < 0 || s.Pairs >= size || s.Windows < 0 || s.Sign < -1 || s.Sign > 1 {
		return persistenceRun{}, fmt.Errorf("invalid persistence checkpoint")
	}
	return persistenceRun{pairs: s.Pairs, windows: s.Windows, sign: s.Sign, kind: s.Kind}, nil
}

type discoveryPair struct {
	From, To, Class string
	Window          stats.WindowSnapshot
	LastPair        [2]int64
	Run, BeforeLast runCheckpoint
	PValue          *float64
	N               int
	R               float64
}

type discoveryRecent struct {
	Values []float64
	Next   int
}

type discoveryCheckpoint struct {
	Version    int
	SavedAt    time.Time
	Policy     discoveryPolicy
	Properties []types.PropertyReading
	Recent     map[string]discoveryRecent
	Pairs      []discoveryPair
	Candidates []*types.CandidateEdge
	Links      []string
}

func (p *MICorrelationProposer) policy() discoveryPolicy {
	return discoveryPolicy{p.threshold, p.minSpread, p.fdr, p.minPairs, p.bufSize, p.maxPairs, p.pairWindow, p.evidenceMaxGap}
}

// Checkpoint keeps raw paired evidence and its persistence counters in one record.
// Explicit Observe pairs have no timestamps and cannot be safely resumed.
func (p *MICorrelationProposer) Checkpoint() (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := discoveryCheckpoint{Version: 1, SavedAt: time.Now(), Policy: p.policy(),
		Recent: map[string]discoveryRecent{}}
	for id, v := range p.latestProps {
		s.Properties = append(s.Properties, types.PropertyReading{ID: id, Value: v.value, At: v.at,
			Subject: v.subject, Chart: v.chart, PartOf: v.partOf, Range: v.rng})
	}
	for id, r := range p.recent {
		s.Recent[id] = discoveryRecent{slices.Clone(r.vs), r.next}
	}
	for key, sl := range p.slots {
		w, ok := p.buffers[key]
		if !ok {
			continue
		}
		pr := p.persistence[key]
		var pv *float64
		if value, ok := p.pvals[key]; ok {
			pv = &value
		}
		s.Pairs = append(s.Pairs, discoveryPair{From: sl.from, To: sl.to, Class: sl.class,
			Window: w.Snapshot(), LastPair: p.lastPair[key], Run: checkpointRun(pr.run),
			BeforeLast: checkpointRun(pr.beforeLast), PValue: pv, N: sl.n, R: sl.r})
	}
	for _, id := range p.order {
		if c := p.candidates[id]; c != nil {
			s.Candidates = append(s.Candidates, copyCandidate(c))
		}
	}
	for key := range p.links {
		s.Links = append(s.Links, key)
	}
	return json.Marshal(s)
}

// RestoreCheckpoint is called before collection/review starts. Stale or changed-
// policy evidence is discarded explicitly; malformed snapshots fail startup.
func (p *MICorrelationProposer) RestoreCheckpoint(raw json.RawMessage, now time.Time, maxGap time.Duration) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if maxGap <= 0 {
		maxGap = 2 * time.Minute
	}
	p.evidenceMaxGap = maxGap
	if len(raw) == 0 {
		return "no saved discovery evidence; starting fresh", nil
	}
	var s discoveryCheckpoint
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("decoding discovery checkpoint: %w", err)
	}
	if s.Version != 1 {
		return "", fmt.Errorf("unsupported discovery checkpoint version %d", s.Version)
	}
	if s.Policy != p.policy() {
		return "discovery policy changed; discarded saved discovery evidence", nil
	}
	if s.SavedAt.IsZero() || s.SavedAt.After(now) || now.Sub(s.SavedAt) > maxGap {
		return "discovery checkpoint expired or clock moved backwards; discarded saved evidence", nil
	}
	if len(p.buffers) != 0 || len(p.candidates) != 0 {
		return "", fmt.Errorf("cannot restore discovery into a running proposer")
	}
	for _, v := range s.Properties {
		if v.ID == "" || v.At.After(now) {
			return "", fmt.Errorf("invalid discovery property %q", v.ID)
		}
		if now.Sub(v.At) > maxGap {
			continue
		}
		p.latestProps[v.ID] = latestValue{value: v.Value, at: v.At, subject: v.Subject, chart: v.Chart, partOf: v.PartOf, rng: v.Range}
		p.latestAt = maxTime(p.latestAt, v.At)
		if r, ok := s.Recent[v.ID]; ok {
			if len(r.Values) > p.bufSize || r.Next < 0 || r.Next >= p.bufSize ||
				len(r.Values) < p.bufSize && r.Next != 0 {
				return "", fmt.Errorf("invalid recent discovery window for %q", v.ID)
			}
			p.recent[v.ID] = &valueRing{vs: slices.Clone(r.Values), next: r.Next}
		}
	}
	for _, pair := range s.Pairs {
		if _, ok := p.latestProps[pair.From]; !ok {
			continue
		}
		if _, ok := p.latestProps[pair.To]; !ok {
			continue
		}
		key := pair.From + "→" + pair.To
		from, to, _, class := p.orient(pair.From, pair.To)
		if from == to || from != pair.From || to != pair.To || class != pair.Class || p.buffers[key] != nil ||
			pair.N < 0 || pair.N > p.bufSize || pair.R < -1 || pair.R > 1 ||
			pair.PValue != nil && (*pair.PValue < 0 || *pair.PValue > 1) {
			return "", fmt.Errorf("invalid discovery pair %q", key)
		}
		last := time.Unix(0, min(pair.LastPair[0], pair.LastPair[1]))
		if last.After(now) || now.Sub(last) > maxGap {
			continue
		}
		w, err := stats.RestoreWindow(pair.Window)
		if err != nil {
			return "", fmt.Errorf("invalid discovery window %q: %w", key, err)
		}
		if pair.Window.Capacity != p.bufSize {
			return "", fmt.Errorf("discovery window %q has capacity %d, want %d", key, pair.Window.Capacity, p.bufSize)
		}
		run, err := pair.Run.restore(p.bufSize)
		if err != nil {
			return "", err
		}
		before, err := pair.BeforeLast.restore(p.bufSize)
		if err != nil {
			return "", err
		}
		p.buffers[key], p.lastPair[key] = w, pair.LastPair
		p.persistence[key] = &pairPersistence{run: run, beforeLast: before}
		p.slots[key] = &pairSlot{from: pair.From, to: pair.To, class: pair.Class, n: pair.N, r: pair.R}
		if pair.PValue != nil {
			p.pvals[key] = *pair.PValue
		}
		for _, ends := range [][2]string{{pair.From, pair.To}, {pair.To, pair.From}} {
			if p.partners[ends[0]] == nil {
				p.partners[ends[0]] = map[string]struct{}{}
			}
			p.partners[ends[0]][ends[1]] = struct{}{}
		}
	}
	for _, c := range s.Candidates {
		if c == nil || c.CandidateID != candidateID(c.FromID, c.ToID, c.Undirected) {
			return "", fmt.Errorf("invalid saved discovery candidate")
		}
		c = copyCandidate(c)
		key := c.FromID + "→" + c.ToID
		if pr := p.persistence[key]; pr != nil {
			c.PersistentWindows, c.PersistencePairs, c.PersistenceWindowSize = pr.run.windows, pr.run.pairs, p.bufSize
		} else if !settled(c) {
			c.PersistentWindows, c.PersistencePairs = 0, 0
		}
		if c.Status == types.Confirmed {
			sign := 1
			if c.Direction == types.Negative {
				sign = -1
			}
			covered := p.lookup != nil && (p.lookup.Covered(c.FromID, c.ToID, sign) ||
				c.Undirected && p.lookup.Covered(c.ToID, c.FromID, sign))
			if !covered && !p.associated(c.FromID, c.ToID, sign) {
				c.Status, c.Review = types.Pending, nil
				if p.persistence[key] == nil {
					c.PersistentWindows, c.PersistencePairs = 0, 0
				}
			}
		}
		if c.Status == types.Pending {
			// A new process may use a different review policy, while the journal
			// still retains every earlier review.
			c.Review = nil
		}
		p.candidates[c.CandidateID] = c
		p.order = append(p.order, c.CandidateID)
	}
	for _, key := range s.Links {
		if p.buffers[key] != nil {
			p.links[key] = struct{}{}
			delete(p.pvals, key)
		}
	}
	p.regroupLocked()
	p.barValid = false
	p.enforceCapLocked()
	return fmt.Sprintf("restored %d discovery windows and %d candidates", len(p.buffers), len(p.candidates)), nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
