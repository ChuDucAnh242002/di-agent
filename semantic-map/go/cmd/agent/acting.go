package main

import (
	"net/http"
	"strings"

	"github.com/DiyazY/di-agent/pkg/policy"
	"github.com/DiyazY/di-agent/pkg/runtime"
)

// targetOf is the workload as the policy sees it: its kind is the subject's part
// before the colon, so a rule written for pods never matches a unit.
func targetOf(w runtime.Workload) policy.Target {
	kind, _, _ := strings.Cut(w.Subject, ":")
	return policy.Target{Kind: kind, Name: w.Name, Labels: w.Labels, Self: w.Self}
}

type resourceView struct {
	Request *float64 `json:"request"`
	Limit   *float64 `json:"limit"`
}

type workloadView struct {
	Subject string            `json:"subject"`
	Name    string            `json:"name"`
	Labels  map[string]string `json:"labels"`
	CPU     resourceView      `json:"cpu"`
	Can     map[string]string `json:"can"`
	Policy  policy.Effective  `json:"policy"`
}

// registerActingRoutes shows what the agent could change and what it is allowed
// to: every workload the runtime lists with the policy that governs it, and the
// same answer for one workload with the rule behind it.
func registerActingRoutes(mux *http.ServeMux, rt runtime.Runtime, pol *policy.Policy) {
	mux.HandleFunc("GET /workloads", func(w http.ResponseWriter, r *http.Request) {
		ws, err := rt.Workloads(r.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, "listing workloads: "+err.Error())
			return
		}
		out := make([]workloadView, 0, len(ws))
		for _, wl := range ws {
			out = append(out, workloadView{
				Subject: wl.Subject, Name: wl.Name, Labels: wl.Labels,
				CPU:    resourceView{Request: wl.CPU.Request, Limit: wl.CPU.Limit},
				Can:    wl.Can,
				Policy: pol.For(targetOf(wl)),
			})
		}
		writeJSON(w, out)
	})

	mux.HandleFunc("GET /policy", func(w http.ResponseWriter, r *http.Request) {
		subject := r.URL.Query().Get("subject")
		if subject == "" {
			writeError(w, http.StatusBadRequest, "subject is required, e.g. ?subject=pod:x")
			return
		}
		ws, err := rt.Workloads(r.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, "listing workloads: "+err.Error())
			return
		}
		for _, wl := range ws {
			if wl.Subject == subject {
				writeJSON(w, pol.For(targetOf(wl)))
				return
			}
		}
		writeError(w, http.StatusNotFound, "the runtime has no workload "+subject)
	})
}
