// Command agent is the Semantic Map daemon. Its body lives in pkg/agentmain so that a
// custom binary can register extra collectors before running the same daemon.
//
// Usage:
//
//	agent -config agent.yaml
//	agent -print-config                      # the defaults, as a file to copy and edit
//	agent -config agent.yaml -print-config   # the effective spec
//
// The agent is described by one document, agent.yaml (see pkg/spec): the file
// overlays the defaults, so it names only what it changes, and a key the agent does
// not know is refused. These are the only flags.
package main

import "github.com/DiyazY/di-agent/pkg/agentmain"

func main() { agentmain.Main() }
