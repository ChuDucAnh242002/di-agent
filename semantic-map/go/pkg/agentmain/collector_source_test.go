package agentmain

import (
	"strings"
	"testing"

	"github.com/DiyazY/di-agent/pkg/spec"
)

// The startup log names an http collector's URL; userinfo, a query or a fragment
// in it must not reach the log, and a URL that does not parse is not echoed.
func TestCollectorSourceRedactsHTTPURL(t *testing.T) {
	for _, raw := range []string{"http://user:s3cret@sidecar:9100/collect", "http://s3cret@sidecar:9100/collect",
		"http://sidecar:9100/collect?token=s3cret", "http://sidecar:9100/collect#s3cret"} {
		got := collectorSource(spec.CollectorEntry{HTTP: &spec.HTTPSpec{URL: raw}})
		if strings.Contains(got, "s3cret") || !strings.Contains(got, "sidecar:9100/collect") {
			t.Errorf("collectorSource(%q) = %q, want the URL with its secret redacted", raw, got)
		}
	}
	if got := collectorSource(spec.CollectorEntry{HTTP: &spec.HTTPSpec{URL: "http://%zz"}}); got != spec.Redaction {
		t.Fatalf("collectorSource = %q, want %s", got, spec.Redaction)
	}
}
