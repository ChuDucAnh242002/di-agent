package main

import (
	"context"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/autonomy"
	"github.com/DiyazY/di-agent/pkg/profiles"
	"github.com/DiyazY/di-agent/pkg/spec"
)

type timeoutDecider struct{}

func (timeoutDecider) Name() string { return "timeout-capture" }
func (timeoutDecider) Decide(context.Context, autonomy.Evidence, string) (*autonomy.Verdict, error) {
	return nil, context.Canceled
}

type timeoutReasoner struct{}

func (timeoutReasoner) Name() string { return "timeout-capture" }
func (timeoutReasoner) Reason(context.Context, autonomy.Evidence) (*autonomy.Analysis, error) {
	return nil, context.Canceled
}

// TestAReviewModelsTimeoutIsConfigured: a model on the node's own CPUs scoring a
// candidate with a reasoning model's analysis took longer than a fixed two minutes, and
// every such review failed. How long a model may take is the operator's setting, per
// tier, and reaches the backend.
func TestAReviewModelsTimeoutIsConfigured(t *testing.T) {
	var decision, reasoning time.Duration
	autonomy.RegisterDecider("timeout-capture", func(s autonomy.Spec, _ autonomy.Renderer) (autonomy.Decider, error) {
		decision = s.Timeout
		return timeoutDecider{}, nil
	})
	autonomy.RegisterReasoner("timeout-capture", func(s autonomy.Spec, _ autonomy.Renderer, _ []autonomy.Tool) (autonomy.Reasoner, error) {
		reasoning = s.Timeout
		return timeoutReasoner{}, nil
	})
	sm, err := profiles.Build("edge-minimal", profiles.Config{StateMap: newTestState(t)})
	if err != nil {
		t.Fatal(err)
	}
	opt := spec.Default().Loops.Review
	opt.Decision.Backend, opt.Decision.Timeout = "timeout-capture", 7*time.Minute
	opt.Reasoning.Backend, opt.Reasoning.Timeout = "timeout-capture", 13*time.Minute
	if _, err := buildReviewer(sm, opt); err != nil {
		t.Fatal(err)
	}
	if decision != 7*time.Minute || reasoning != 13*time.Minute {
		t.Fatalf("decision timeout %v, reasoning %v; want the configured 7m and 13m", decision, reasoning)
	}
	d := spec.Default().Loops.Review
	if d.Decision.Timeout < 5*time.Minute || d.Reasoning.Timeout < d.Decision.Timeout {
		t.Fatalf("default timeouts decision %v, reasoning %v; want room for a model on the node's CPUs", d.Decision.Timeout, d.Reasoning.Timeout)
	}
}
