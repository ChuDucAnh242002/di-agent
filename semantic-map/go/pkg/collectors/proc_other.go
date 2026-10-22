//go:build !unix

package collectors

import (
	"os/exec"
	"syscall"
)

// ownGroup is a no-op where process groups are not available; the agent targets
// Linux and macOS, and this keeps the package building elsewhere.
func ownGroup(*exec.Cmd) {}

// signalGroup can reach only the plugin itself here; any signal other than
// SIGKILL is delivered as a kill, since that is all the platform offers.
func signalGroup(cmd *exec.Cmd, _ syscall.Signal) {
	cmd.Process.Kill()
}
