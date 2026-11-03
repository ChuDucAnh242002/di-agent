package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DiyazY/di-agent/cmd/mapctl/client"
)

func TestAssociationRenderingPreservesUndirectedMeaning(t *testing.T) {
	var edge client.EdgeDTO
	if err := json.Unmarshal([]byte(`{"from":"a","to":"b","proposition_id":"association","direction":"+","association":true,"basis":"association"}`), &edge); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	writeDot(&out, &client.GraphSnapshot{Edges: []client.EdgeDTO{edge}})
	if !strings.Contains(out.String(), "dir=none") || !strings.Contains(out.String(), "association only") {
		t.Fatalf("association rendered directionally: %s", out.String())
	}
}
