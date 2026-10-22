// Command agent is the Semantic Map daemon. Its body lives in pkg/agentmain so that a
// custom binary can register extra collectors before running the same daemon.
package main

import "github.com/DiyazY/di-agent/pkg/agentmain"

func main() { agentmain.Main() }
