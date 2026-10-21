package main

import (
	"net/http"

	"go.yaml.in/yaml/v3"

	"github.com/DiyazY/di-agent/pkg/spec"
)

// shownSpec is the spec as an operator may see it: secrets withheld, rendered as
// the document the file is written in.
func shownSpec(s spec.Spec) ([]byte, error) {
	return s.Redacted().YAML()
}

// registerConfigRoute serves the spec the agent is running on, with secrets
// withheld, so "what is this agent set to" is one request rather than a read of
// a file that may have changed since the process started. The body is the spec's
// own YAML decoded and re-encoded as JSON, so its keys and durations read as they
// do in agent.yaml (`ingest_scope`, `"10s"`), not as Go field names.
func registerConfigRoute(mux *http.ServeMux, s spec.Spec) {
	var shown map[string]any
	out, err := shownSpec(s)
	if err == nil {
		err = yaml.Unmarshal(out, &shown)
	}
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
		if err != nil {
			http.Error(w, "config unavailable: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, shown)
	})
}
