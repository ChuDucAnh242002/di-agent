package agentmain

import (
	"os"
	"strings"
	"testing"
)

// TestSpecReachesConfig guards the class of defect this project has hit three
// times: a setting is declared, parsed, logged, and never passed to the thing it
// configures. Each instance produced a daemon that ran normally while ignoring the
// operator — the tuner flag wired DisabledTuner, the proposer's space-separated value
// discarded every flag after it, and a since-removed machine-class flag seeded
// class-averaged priors while logging that it had applied class-specific ones.
//
// Comparing the spec fields against the Config fields they must populate catches the
// next one without waiting for a measurement to look wrong.
func TestSpecReachesConfig(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	// Each entry is a spec path as main.go reads it and the Config field its value
	// must reach. Extend this when adding a setting that configures the profile.
	wiring := map[string]string{
		"cfg.Node.IngestScope": "AcceptForeignSamples:",
		"cfg.Map.PairWindow":   "PairWindowSeconds:",
		"cfg.Map.PairSupport":  "PairMinSupport:",
		"cfg.Map.PairHistory":  "PairWindow:",
		"cfg.Map.Tuner":        "UseRuleBasedTuner:",
		"prop.Enabled":         "UseProposer:",
		"cfg.Profile.KD":       "KD:",
		"cfg.Profile.Priors":   "PriorWeightsPath:",
		"cfg.Domain.Path":      "DomainSpec:",
	}
	for path, field := range wiring {
		if !strings.Contains(body, path) {
			t.Errorf("%s is no longer read by main.go; update this test or restore it", path)
			continue
		}
		if !strings.Contains(body, field) {
			t.Errorf("%s is read but no Config literal sets %s — the daemon will "+
				"accept the setting and ignore it", path, field)
		}
	}
}
