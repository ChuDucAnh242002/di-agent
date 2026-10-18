package main

import (
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/DiyazY/di-agent/pkg/spec"
)

// TestConfigRouteRedactsSecrets pins that GET /config shows the agent's spec and
// never a credential in it.
func TestConfigRouteRedactsSecrets(t *testing.T) {
	s := spec.Default()
	s.Explain.APIKey = "k-secret"
	s.Loops.Review.Decision.APIKey = "k-secret"

	mux := http.NewServeMux()
	registerConfigRoute(mux, s)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/config", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "k-secret") {
		t.Errorf("a secret reached the response: %s", body)
	}
	if !strings.Contains(body, spec.Redaction) {
		t.Errorf("response does not say a secret was withheld: %s", body)
	}
	var got spec.Spec
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not the spec as JSON: %v", err)
	}
	if got.Node.Addr != s.Node.Addr {
		t.Errorf("node.addr = %q, want %q", got.Node.Addr, s.Node.Addr)
	}
}

// TestOnlyConfigFlagsRemain pins that the agent is described by its file: the
// command line names the file and asks to see the spec, nothing else.
func TestOnlyConfigFlagsRemain(t *testing.T) {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	defineFlags(fs)
	var got []string
	fs.VisitAll(func(f *flag.Flag) { got = append(got, f.Name) })
	sort.Strings(got)
	if want := "config,print-config"; strings.Join(got, ",") != want {
		t.Errorf("flags = %v, want exactly %s", got, want)
	}
}
