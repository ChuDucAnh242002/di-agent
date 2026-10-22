package agentmain

import (
	"strings"
	"testing"

	"github.com/DiyazY/di-agent/pkg/spec"
)

// The startup log names an http collector's URL; a password in it must not reach
// the log, and a URL that does not parse is named as such rather than echoed.
func TestCollectorSourceRedactsHTTPURL(t *testing.T) {
	got := collectorSource(spec.CollectorEntry{HTTP: &spec.HTTPSpec{URL: "http://user:s3cret@sidecar:9100/collect"}})
	if strings.Contains(got, "s3cret") || !strings.Contains(got, "sidecar:9100/collect") {
		t.Fatalf("collectorSource = %q, want the URL with its password redacted", got)
	}
	if got := collectorSource(spec.CollectorEntry{HTTP: &spec.HTTPSpec{URL: "http://%zz"}}); got != "<invalid url>" {
		t.Fatalf("collectorSource = %q, want <invalid url>", got)
	}
}
