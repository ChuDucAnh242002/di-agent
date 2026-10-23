package agentmain_test

import (
	"fmt"

	"go.yaml.in/yaml/v3"

	"github.com/DiyazY/di-agent/pkg/agentmain"
	"github.com/DiyazY/di-agent/pkg/collectors"
	"github.com/DiyazY/di-agent/pkg/contracts"
)

// Example_customCollector shows a custom agent binary. The registration belongs in an
// init() of the collector's own package, which the binary blank-imports; it is inlined
// here so the example compiles as one unit. There is no Output comment, so the test
// runner compiles the example but never runs it, and Main never starts a daemon.
func Example_customCollector() {
	// In the collector package:
	register := func() {
		collectors.Register("mine", func(env collectors.Env, config *yaml.Node) (contracts.CollectorContract, error) {
			return nil, fmt.Errorf("mine: not implemented")
		})
	}
	_ = register

	// In the custom binary's main.go, after blank-importing that package:
	agentmain.Main()
}
