package agentmain

import (
	"net/http"
	"time"

	"github.com/DiyazY/di-agent/pkg/collectors"
)

// collectorsBody is GET /collectors: the collection interval in milliseconds, so a
// sub-second interval is not reported as 0, and every configured
// collector's status, in the order sources.collectors lists them.
type collectorsBody struct {
	IntervalMS int64                     `json:"interval_ms"`
	Collectors []collectors.MemberStatus `json:"collectors"`
}

// registerCollectorsRoute serves what each collector is doing, so a collector that
// faults, backs off or has its samples dropped is visible rather than looking like
// telemetry that has not arrived. Read-only: collectors are configured in the spec.
// A nil supervisor (no collectors configured) answers with an empty list.
func registerCollectorsRoute(mux *http.ServeMux, sup *collectors.Supervisor, interval time.Duration) {
	mux.HandleFunc("GET /collectors", func(w http.ResponseWriter, r *http.Request) {
		body := collectorsBody{IntervalMS: interval.Milliseconds(), Collectors: []collectors.MemberStatus{}}
		if sup != nil {
			body.Collectors = sup.Status()
		}
		writeJSON(w, body)
	})
}
