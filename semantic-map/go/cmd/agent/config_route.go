package main

import (
	"net/http"

	"github.com/DiyazY/di-agent/pkg/spec"
)

// registerConfigRoute serves the spec the agent is running on, with secrets
// withheld, so "what is this agent set to" is one request rather than a read of
// a file that may have changed since the process started.
func registerConfigRoute(mux *http.ServeMux, s spec.Spec) {
	shown := s.Redacted()
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, shown)
	})
}
