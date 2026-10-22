package collectors

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DiyazY/di-agent/pkg/spec"
	"github.com/DiyazY/di-agent/pkg/types"
)

// httpPlugin is a sidecar double: it records the ops it was sent and answers each
// through reply, which may write any response it likes.
type httpPlugin struct {
	mu    sync.Mutex
	ops   []string
	reply func(op string, req map[string]any, w http.ResponseWriter)
}

func (p *httpPlugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	body, _ := io.ReadAll(r.Body)
	json.Unmarshal(body, &req)
	op, _ := req["op"].(string)
	p.mu.Lock()
	p.ops = append(p.ops, op)
	p.mu.Unlock()
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "bad request", 400)
		return
	}
	p.reply(op, req, w)
}

func (p *httpPlugin) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.ops...)
}

func okReply(op string, req map[string]any, w http.ResponseWriter) {
	if op == "hello" {
		w.Write([]byte(`{"protocol":1}`))
		return
	}
	json.NewEncoder(w).Encode(CollectReply{Seq: uint64(req["seq"].(float64)),
		Samples: []*types.MetricSample{{MetricType: "gpu_util", Value: 0.5, TimestampUnix: 100}}})
}

func newHTTP(t *testing.T, p *httpPlugin, timeout time.Duration) *HTTPCollector {
	t.Helper()
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	c := NewHTTPCollector(Env{NodeID: "n1", Interval: 10 * time.Second}, "gpu", spec.HTTPSpec{URL: srv.URL}, timeout, map[string]any{"device": 0})
	t.Cleanup(func() { c.Close() })
	return c
}

func eq(a, b []string) bool { return strings.Join(a, ",") == strings.Join(b, ",") }

func TestHTTPHelloThenCollect(t *testing.T) {
	p := &httpPlugin{reply: okReply}
	c := newHTTP(t, p, time.Second)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	got, err := c.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if !eq(p.seen(), []string{"hello", "collect"}) {
		t.Fatalf("ops %v", p.seen())
	}
	if len(got) != 1 || got[0].Source != "gpu" || got[0].NodeID != "n1" || got[0].EventID == "" {
		t.Fatalf("not stamped: %+v", got)
	}
	if c.SourceID() != "gpu" {
		t.Fatal(c.SourceID())
	}
	if a := c.AvailableMetrics(); len(a) != 1 || a[0] != "gpu_util" {
		t.Fatalf("available %v", a)
	}
}

func TestHTTPNon2xxIsFaultAndReHellos(t *testing.T) {
	n := 0
	p := &httpPlugin{}
	p.reply = func(op string, req map[string]any, w http.ResponseWriter) {
		if op == "collect" {
			n++
			if n == 2 {
				http.Error(w, "down", 503)
				return
			}
		}
		okReply(op, req, w)
	}
	c := newHTTP(t, p, time.Second)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Collect(); err == nil {
		t.Fatal("503 must be a fault")
	}
	if _, err := c.Collect(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"hello", "collect", "collect", "hello", "collect"}; !eq(p.seen(), want) {
		t.Fatalf("ops %v want %v", p.seen(), want)
	}
}

func TestHTTPTimeout(t *testing.T) {
	p := &httpPlugin{}
	p.reply = func(op string, req map[string]any, w http.ResponseWriter) {
		if op == "collect" {
			time.Sleep(time.Second)
		}
		okReply(op, req, w)
	}
	c := newHTTP(t, p, 100*time.Millisecond)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := c.Collect(); err == nil {
		t.Fatal("want timeout")
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("took %s", d)
	}
}

func TestHTTPHelloErrorRefused(t *testing.T) {
	for name, body := range map[string]string{
		"error":    `{"error":"no gpu"}`,
		"protocol": `{"protocol":2}`,
		"notjson":  `nope`,
	} {
		p := &httpPlugin{reply: func(string, map[string]any, http.ResponseWriter) {}}
		p.reply = func(op string, req map[string]any, w http.ResponseWriter) { w.Write([]byte(body)) }
		c := newHTTP(t, p, time.Second)
		if err := c.Start(); err == nil {
			t.Fatalf("%s: Start must fail", name)
		}
	}
}

func TestHTTPReplyFaults(t *testing.T) {
	cases := map[string]func(req map[string]any) string{
		"error reply": func(map[string]any) string { return `{"seq":1,"error":"boom"}` },
		"wrong seq":   func(map[string]any) string { return `{"seq":99,"samples":[]}` },
		"not json":    func(map[string]any) string { return `<html>` },
	}
	for name, mk := range cases {
		p := &httpPlugin{}
		mk := mk
		p.reply = func(op string, req map[string]any, w http.ResponseWriter) {
			if op == "hello" {
				w.Write([]byte(`{"protocol":1}`))
				return
			}
			w.Write([]byte(mk(req)))
		}
		c := newHTTP(t, p, time.Second)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Collect(); err == nil {
			t.Fatalf("%s: want fault", name)
		}
	}
}

func TestHTTPOversizedBodyIsFault(t *testing.T) {
	p := &httpPlugin{}
	p.reply = func(op string, req map[string]any, w http.ResponseWriter) {
		if op == "hello" {
			w.Write([]byte(`{"protocol":1}`))
			return
		}
		w.Write([]byte(`{"seq":1,"samples":[],"pad":"` + strings.Repeat("x", maxHTTPBody) + `"}`))
	}
	c := newHTTP(t, p, 5*time.Second)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Collect(); err == nil {
		t.Fatal("oversized body must be a fault")
	}
}

func TestHTTPEmptySamplesIsNoData(t *testing.T) {
	p := &httpPlugin{}
	p.reply = func(op string, req map[string]any, w http.ResponseWriter) {
		if op == "hello" {
			w.Write([]byte(`{"protocol":1}`))
			return
		}
		w.Write([]byte(`{"seq":1,"samples":[],"available":["gpu_util"]}`))
	}
	c := newHTTP(t, p, time.Second)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	got, err := c.Collect()
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %v err %v", got, err)
	}
	if a := c.AvailableMetrics(); len(a) != 1 {
		t.Fatalf("available %v", a)
	}
}

func TestHTTPHelloFailureOnRetryIsThatTicksFault(t *testing.T) {
	fail := false
	p := &httpPlugin{}
	p.reply = func(op string, req map[string]any, w http.ResponseWriter) {
		if op == "hello" && fail {
			w.Write([]byte(`{"error":"not yet"}`))
			return
		}
		if op == "collect" && fail {
			http.Error(w, "x", 500)
			return
		}
		okReply(op, req, w)
	}
	c := newHTTP(t, p, time.Second)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	fail = true
	if _, err := c.Collect(); err == nil {
		t.Fatal("want fault")
	}
	if _, err := c.Collect(); err == nil {
		t.Fatal("hello refusal must fault the tick")
	}
	fail = false
	if _, err := c.Collect(); err != nil {
		t.Fatalf("recovers: %v", err)
	}
}
