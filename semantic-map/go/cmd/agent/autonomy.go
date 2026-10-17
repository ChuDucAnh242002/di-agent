package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/DiyazY/di-agent/pkg/autonomy"
	"github.com/DiyazY/di-agent/pkg/semmap"
)

// autonomyOptions bundles the -autonomy-* flags.
type autonomyOptions struct {
	Level          string // "off" runs no reviewer
	Interval       time.Duration
	PerCycle       int
	URL            string // Ollama's base URL, without /v1
	DecisionModel  string // "none" disables the model tiers
	ReasoningModel string // "none" disables tier 2
	KeepAlive      string
	Thresholds     autonomy.Thresholds
}

// buildReviewer constructs the candidate reviewer, or nil when -autonomy=off.
func buildReviewer(sm *semmap.SemanticMap, opt autonomyOptions) (*autonomy.Reviewer, error) {
	if opt.Level == "" || opt.Level == "off" {
		return nil, nil
	}
	level, err := autonomy.ParseLevel(opt.Level)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(opt.URL, "/")
	cfg := autonomy.Config{Level: level, Interval: opt.Interval, MaxPer: opt.PerCycle, Thresholds: opt.Thresholds}
	if opt.DecisionModel != "" && opt.DecisionModel != "none" {
		cfg.Decider = autonomy.NewSystemOne(base, opt.DecisionModel, 0)
	}
	if opt.ReasoningModel != "" && opt.ReasoningModel != "none" {
		if cfg.Decider == nil {
			return nil, fmt.Errorf("-autonomy-reasoning-model %s needs a decision model to score its analysis", opt.ReasoningModel)
		}
		chat := autonomy.NewChat(sm, base+"/v1", opt.ReasoningModel, 0)
		chat.KeepAlive = opt.KeepAlive
		cfg.Reasoner = chat
	}
	return autonomy.New(sm, cfg)
}

// registerAutonomyRoutes exposes the reviewer's configuration and counts. With no
// reviewer it answers {"level":"off"}, so a reader can tell "off" from "unknown".
func registerAutonomyRoutes(mux *http.ServeMux, r *autonomy.Reviewer) {
	mux.HandleFunc("GET /autonomy", func(w http.ResponseWriter, _ *http.Request) {
		if r == nil {
			writeJSON(w, map[string]string{"level": "off"})
			return
		}
		writeJSON(w, r.Status())
	})
}

// autonomyStatus is what GET /state reports of the reviewer: the level the map is
// being shaped under.
func autonomyStatus(r *autonomy.Reviewer) func() *autonomy.Status {
	return func() *autonomy.Status {
		if r == nil {
			return &autonomy.Status{Level: "off"}
		}
		s := r.Status()
		return &s
	}
}
