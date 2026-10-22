package agentmain

import (
	"net/http"
	"time"

	"github.com/DiyazY/di-agent/pkg/collectors"
)

// collectorsBody is GET /collectors: the collection interval and every configured
// collector's status, in the order sources.collectors lists them.
type collectorsBody struct {
	IntervalS  int64                     `json:"interval_s"`
	Collectors []collectors.MemberStatus `json:"collectors"`
}

// registerCollectorsRoute serves what each collector is doing, so a collector that
// faults, backs off or has its samples dropped is visible rather than looking like
// telemetry that has not arrived. Read-only: collectors are configured in the spec.
// A nil supervisor (no collectors configured) answers with an empty list.
func registerCollectorsRoute(mux *http.ServeMux, sup *collectors.Supervisor, interval time.Duration) {
	mux.HandleFunc("GET /collectors", func(w http.ResponseWriter, r *http.Request) {
		body := collectorsBody{IntervalS: int64(interval / time.Second), Collectors: []collectors.MemberStatus{}}
		if sup != nil {
			body.Collectors = sup.Status()
		}
		writeJSON(w, body)
	})
}
