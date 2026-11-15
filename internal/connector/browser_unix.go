//go:build !windows

package connector

import (
	"os/exec"
	"syscall"
	"time"
)

// ownProcessGroup starts the engine in a process group of its own, so
// stopping it reaches every renderer it forked.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// endProcessGroup asks the engine's process group to end and kills it when
// it is still there after grace. done closes when the engine exited.
func endProcessGroup(cmd *exec.Cmd, done <-chan struct{}, grace time.Duration) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(grace):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
	}
}
