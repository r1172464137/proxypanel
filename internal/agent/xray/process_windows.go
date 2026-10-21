//go:build windows

package xray

import (
	"os"
	"os/exec"
	"syscall"
)

// hideWindow keeps the core process windowless.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// gracefulStop asks the process to terminate. Windows has no portable SIGTERM
// for child processes, so the hard kill is the only guaranteed stop.
func gracefulStop(p *os.Process) {
	_ = p.Kill()
}
