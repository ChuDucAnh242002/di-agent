//go:build unix

package collectors

import (
	"os/exec"
	"syscall"
)

// ownGroup puts the plugin in a process group of its own, so a fault can signal
// everything it started: a wrapper script's subprocess is usually what hung, and
// killing only the wrapper would orphan it on every fault.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup signals the plugin's whole process group. The group outlives its
// leader while any member lives, so this also reaches what a dead plugin left
// behind; ESRCH (nothing left) is not an error worth reporting.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	syscall.Kill(-cmd.Process.Pid, sig)
}
