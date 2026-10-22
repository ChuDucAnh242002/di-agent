// Command mapctl is the command-line control surface for the di-agent
// semantic-map daemon. It mirrors the daemon's HTTP API as cobra
// subcommands and renders responses either as ASCII tables (default) or as
// pretty-printed JSON (with --json).
//
// Build:
//
//	go build ./cmd/mapctl
//
// Examples:
//
//	mapctl health
//	mapctl graph
//	mapctl --json edges --from RC --to PS
//	mapctl strength P3 0.77
//	mapctl deprecate P1 "stale; superseded by P4"
//	mapctl reset RC PS
//	mapctl watch graph --interval 2s
//	mapctl dot > graph.dot && dot -Tpdf graph.dot -o graph.pdf
//	mapctl collector check --exec ./loadavg.py
package main

import (
	"errors"
	"fmt"
	"os"

	mapctlcmd "github.com/DiyazY/di-agent/cmd/mapctl/cmd"
)

func main() {
	if err := mapctlcmd.Execute(); err != nil {
		// A failed certification has already printed every check and its verdict.
		if !errors.Is(err, mapctlcmd.ErrNotCertified) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
