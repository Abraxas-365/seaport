//go:build windows

package connector

import (
	"os/exec"
	"time"
)

// The managed browser runs in hosted slots (Linux containers); the Windows
// build of `seaport connect` only needs to compile. Without process groups
// the engine itself is killed and its renderers are left to exit on their
// own.

func ownProcessGroup(*exec.Cmd) {}

func endProcessGroup(cmd *exec.Cmd, done <-chan struct{}, grace time.Duration) {
	_ = cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(grace):
	}
}
