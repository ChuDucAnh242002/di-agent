package collectors

import (
	"os"
	"path/filepath"
	"testing"
)

// newFakeCgroupRoot creates a temp directory with valid cgroups v2 files so the
// cgroup collector can be exercised without a real kernel cgroup mount. It is
// copied from internal/minimal/tests, which is not importable from here.
func newFakeCgroupRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "cpu.stat"),
		"usage_usec 1000000\n"+
			"user_usec 800000\n"+
			"system_usec 200000\n"+
			"nr_periods 1000\n"+
			"nr_throttled 50\n"+
			"throttled_usec 25000\n",
	)
	mustWrite(t, filepath.Join(root, "memory.current"), "2147483648\n") // 2 GB
	mustWrite(t, filepath.Join(root, "memory.max"), "8589934592\n")     // 8 GB
	return root
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("mustWrite %s: %v", path, err)
	}
}
