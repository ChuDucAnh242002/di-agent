package collectors

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/compliance"
	"github.com/DiyazY/di-agent/pkg/contracts"
	"github.com/DiyazY/di-agent/pkg/spec"
)

// The exec and http adapters are collectors like any other: the suite that
// certifies a Go collector must pass over a well-behaved plugin behind each.

func TestExecCompliance(t *testing.T) {
	compliance.RunCollectorCompliance(t, func(t *testing.T) contracts.CollectorContract {
		e, _ := fixture(t, "good", 5*time.Second, nil)
		if err := e.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		return e
	})
}

func TestHTTPCompliance(t *testing.T) {
	compliance.RunCollectorCompliance(t, func(t *testing.T) contracts.CollectorContract {
		srv := httptest.NewServer(&httpPlugin{reply: complianceReply})
		t.Cleanup(srv.Close)
		c := NewHTTPCollector(Env{NodeID: "n1", Interval: 10 * time.Second}, "gpu",
			spec.HTTPSpec{URL: srv.URL}, 5*time.Second, map[string]any{"device": 0})
		t.Cleanup(func() { c.Close() })
		if err := c.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		return c
	})
}
