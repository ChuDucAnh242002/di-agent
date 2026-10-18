package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/DiyazY/di-agent/pkg/policy"
	"github.com/DiyazY/di-agent/pkg/relief"
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
// same answer for one workload with the rule behind it. The relief routes show
// the loop and are its kill switch; loop is nil when the spec leaves relief off,
// and they then say so rather than not exist.
func registerActingRoutes(mux *http.ServeMux, rt runtime.Runtime, pol *policy.Policy, loop *relief.Loop) {
	registerReliefRoutes(mux, loop)

	mux.HandleFunc("GET /workloads", func(w http.ResponseWriter, r *http.Request) {
		ws, err := rt.Workloads(r.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, "listing workloads: "+err.Error())
			return
		}
		out := make([]workloadView, 0, len(ws))
		for _, wl := range ws {
			if wl.Labels == nil {
				wl.Labels = map[string]string{}
			}
			if wl.Can == nil {
				wl.Can = map[string]string{}
			}
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

// registerReliefRoutes wires GET /relief, GET /relief/actions and the three
// operator controls: pause, resume and revert one action. Pause stops new
// changes and releases; it does not abandon a change in force.
func registerReliefRoutes(mux *http.ServeMux, loop *relief.Loop) {
	handle := func(pattern string, mutates bool, h func(w http.ResponseWriter, r *http.Request)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if loop == nil {
				writeError(w, http.StatusNotFound, "relief is not enabled in the spec (loops.relief.enabled)")
				return
			}
			if mutates {
				if err := requireJSON(r); err != nil {
					writeError(w, http.StatusBadRequest, err.Error())
					return
				}
			}
			h(w, r)
		})
	}
	handle("GET /relief", false, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, loop.Status()) })
	handle("GET /relief/actions", false, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, loop.Actions()) })
	handle("POST /relief/pause", true, func(w http.ResponseWriter, r *http.Request) {
		loop.Pause()
		writeJSON(w, loop.Status())
	})
	handle("POST /relief/resume", true, func(w http.ResponseWriter, r *http.Request) {
		loop.Resume()
		writeJSON(w, loop.Status())
	})
	handle("POST /relief/actions/{id}/revert", true, func(w http.ResponseWriter, r *http.Request) {
		err := loop.Revert(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, relief.ErrNoAction):
			writeError(w, http.StatusNotFound, err.Error())
		case err != nil:
			writeError(w, http.StatusBadGateway, err.Error())
		default:
			writeJSON(w, loop.Status())
		}
	})
}
