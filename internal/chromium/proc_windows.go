//go:build windows

package chromium

import (
	"os/exec"
	"strconv"
)

func setProcessGroup(cmd *exec.Cmd) {}

// terminate ends Chromium's process tree; taskkill /T reaches the helper
// processes the browser started.
func terminate(cmd *exec.Cmd) {
	exec.Command("taskkill", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}

func kill(cmd *exec.Cmd) {
	exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}
