package minimal

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/DiyazY/di-agent/pkg/stats"
	"github.com/DiyazY/di-agent/pkg/types"
)

// DiscoveryEvidenceMaxGap bounds how long an evidence gap or saved checkpoint
// remains usable. It is intentionally independent of telemetry stale_after.
const DiscoveryEvidenceMaxGap = 24 * time.Hour

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
	return persistenceRun{pairs: s.Pairs, windows: min(s.Windows, types.PersistentWindowsRequired),
		sign: s.Sign, kind: s.Kind}, nil
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
// policy evidence is discarded explicitly; malformed snapshots leave the proposer
// empty so the caller can log and continue with fresh discovery.
func (p *MICorrelationProposer) RestoreCheckpoint(raw json.RawMessage, now time.Time, maxGap time.Duration) (note string, restoreErr error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if maxGap <= 0 {
		maxGap = DiscoveryEvidenceMaxGap
	}
	p.evidenceMaxGap = maxGap
	if len(p.buffers) != 0 || len(p.candidates) != 0 {
		return "", fmt.Errorf("cannot restore discovery into a running proposer")
	}
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
	restoreStarted := false
	defer func() {
		if restoreErr != nil && restoreStarted {
			p.clearDiscoveryLocked()
		}
	}()
	propertyIDs := make(map[string]bool, len(s.Properties))
	for _, v := range s.Properties {
		if v.ID == "" || v.At.IsZero() || v.At.After(now) || !finite(v.Value) ||
			v.Range != nil && (!finite(v.Range[0]) || !finite(v.Range[1]) || v.Range[0] > v.Range[1]) ||
			propertyIDs[v.ID] {
			return "", fmt.Errorf("invalid discovery property %q", v.ID)
		}
		propertyIDs[v.ID] = true
	}
	for id, r := range s.Recent {
		if !propertyIDs[id] || len(r.Values) > p.bufSize || r.Next < 0 || r.Next >= p.bufSize ||
			len(r.Values) < p.bufSize && r.Next != 0 || len(r.Values) == 0 && r.Next != 0 {
			return "", fmt.Errorf("invalid recent discovery window for %q", id)
		}
		for _, value := range r.Values {
			if !finite(value) {
				return "", fmt.Errorf("invalid recent discovery value for %q", id)
			}
		}
	}
	restoreStarted = true
	for _, v := range s.Properties {
		p.latestProps[v.ID] = latestValue{value: v.Value, at: v.At, subject: v.Subject, chart: v.Chart, partOf: v.PartOf, rng: v.Range}
		p.latestAt = maxTime(p.latestAt, v.At)
		if now.Sub(v.At) > maxGap {
			continue
		}
		if r, ok := s.Recent[v.ID]; ok {
			p.recent[v.ID] = &valueRing{vs: slices.Clone(r.Values), next: r.Next}
		}
	}
	pairKeys := make(map[string]bool, len(s.Pairs))
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
			pairKeys[key] || pair.N < 0 || pair.N > p.bufSize || !finite(pair.R) || pair.R < -1 || pair.R > 1 ||
			pair.PValue != nil && (!finite(*pair.PValue) || *pair.PValue < 0 || *pair.PValue > 1) {
			return "", fmt.Errorf("invalid discovery pair %q", key)
		}
		pairKeys[key] = true
		if pair.LastPair[0] == 0 || pair.LastPair[1] == 0 {
			return "", fmt.Errorf("invalid discovery pair timestamps %q", key)
		}
		last := time.Unix(0, min(pair.LastPair[0], pair.LastPair[1]))
		first, second := time.Unix(0, pair.LastPair[0]), time.Unix(0, pair.LastPair[1])
		if first.After(now) || second.After(now) || now.Sub(last) > maxGap {
			continue
		}
		w, err := stats.RestoreWindow(pair.Window)
		if err != nil {
			return "", fmt.Errorf("invalid discovery window %q: %w", key, err)
		}
		if pair.Window.Capacity != p.bufSize {
			return "", fmt.Errorf("discovery window %q has capacity %d, want %d", key, pair.Window.Capacity, p.bufSize)
		}
		if w.Len() != pair.N && !(pair.N == 0 && w.Len() < p.minPairs) {
			return "", fmt.Errorf("discovery window %q has %d pairs, checkpoint says %d", key, w.Len(), pair.N)
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
	candidateIDs := make(map[string]bool, len(s.Candidates))
	for _, c := range s.Candidates {
		if c == nil || c.FromID == "" || c.ToID == "" ||
			c.CandidateID != candidateID(c.FromID, c.ToID, c.Undirected) ||
			candidateIDs[c.CandidateID] || !finite(c.MIScore) || c.MIScore < 0 ||
			c.MIScore > 1 || !finite(c.PValue) || c.PValue < 0 || c.PValue > 1 ||
			!finite(c.PBar) || c.PBar < 0 || c.PBar > 1 ||
			c.Direction != types.Positive && c.Direction != types.Negative ||
			c.PersistentWindows < 0 || c.PersistentWindows > types.PersistentWindowsRequired ||
			c.PersistencePairs < 0 || c.PersistencePairs >= p.bufSize ||
			c.PersistenceWindowSize < 0 && c.PersistenceWindowSize != 0 ||
			c.PersistenceWindowSize > 0 && c.PersistenceWindowSize != p.bufSize {
			return "", fmt.Errorf("invalid saved discovery candidate")
		}
		candidateIDs[c.CandidateID] = true
		from, to, undirected, _ := p.orient(c.FromID, c.ToID)
		if from != c.FromID || to != c.ToID || undirected != c.Undirected {
			return "", fmt.Errorf("saved discovery candidate %q has an invalid orientation", c.CandidateID)
		}
		c = copyCandidate(c)
		key := c.FromID + "→" + c.ToID
		if pr := p.persistence[key]; pr != nil {
			c.PersistentWindows, c.PersistencePairs, c.PersistenceWindowSize = pr.run.windows, pr.run.pairs, p.bufSize
		} else if !settled(c) {
			c.PersistentWindows, c.PersistencePairs = 0, 0
		}
		sign := 1
		if c.Direction == types.Negative {
			sign = -1
		}
		if p.associated(c.FromID, c.ToID, sign) {
			// The map snapshot and proposer checkpoint are captured separately. If
			// the map has the association but the checkpoint predates its candidate
			// update, the persisted relationship is authoritative.
			c.Status = types.Confirmed
		} else if c.Status == types.Confirmed {
			covered := p.lookup != nil && (p.lookup.Covered(c.FromID, c.ToID, sign) ||
				c.Undirected && p.lookup.Covered(c.ToID, c.FromID, sign))
			if !covered {
				c.Status, c.Review = types.Pending, nil
				if p.persistence[key] == nil {
					c.PersistentWindows, c.PersistencePairs = 0, 0
				}
			}
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

func (p *MICorrelationProposer) clearDiscoveryLocked() {
	p.buffers = make(map[string]*stats.PairWindow)
	p.persistence = make(map[string]*pairPersistence)
	p.lastPair = make(map[string][2]int64)
	p.candidates = make(map[string]*types.CandidateEdge)
	p.beforeConfirm = make(map[string]types.CandidateEdge)
	p.order = nil
	p.latestConstructs = make(map[string]float64)
	p.latestProps = make(map[string]latestValue)
	p.recent = make(map[string]*valueRing)
	p.slots = make(map[string]*pairSlot)
	p.partners = make(map[string]map[string]struct{})
	p.displaced = 0
	p.lastPass, p.latestAt = time.Time{}, time.Time{}
	p.links = make(map[string]struct{})
	p.repOf = make(map[string]string)
	p.pvals = make(map[string]float64)
	p.bar, p.barTests = 0, 0
	p.barValid = false
	p.folds, p.barFolds = 0, 0
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
