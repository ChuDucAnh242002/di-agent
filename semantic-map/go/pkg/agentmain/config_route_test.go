package agentmain

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

// TestConfigRouteRedactsSecrets pins that GET /config shows the agent's spec in the
// spec's own words (snake_case keys, human durations) and never a credential in it.
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
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if _, ok := got["node"].(map[string]any)["ingest_scope"]; !ok {
		t.Errorf("node.ingest_scope missing; keys are not the spec's: %s", body)
	}
	if v := got["sources"].(map[string]any)["interval"]; v != "10s" {
		t.Errorf("sources.interval = %v, want the string \"10s\"", v)
	}
	if v := got["explain"].(map[string]any)["api_key"]; v != spec.Redaction {
		t.Errorf("explain.api_key = %v, want %q", v, spec.Redaction)
	}
}

// TestPrintedSpecWithholdsSecrets pins that -print-config, which prints after ${VAR}
// expansion, does not echo a credential to stdout.
func TestPrintedSpecWithholdsSecrets(t *testing.T) {
	s := spec.Default()
	s.Explain.APIKey = "k-secret"
	out, err := shownSpec(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "k-secret") || !strings.Contains(string(out), spec.Redaction) {
		t.Errorf("printed spec does not withhold the key:\n%s", out)
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
