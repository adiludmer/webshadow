//go:build !windows

package chromium

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts Chromium and its helpers in their own process
// group, so stopping the session reaches all of them and nothing else.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminate(cmd *exec.Cmd) { syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }

func kill(cmd *exec.Cmd) { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
