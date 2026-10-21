//go:build !windows

package xray

import (
	"os"
	"os/exec"
	"syscall"
)

// hideWindow is a no-op outside Windows.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// gracefulStop sends SIGTERM so the core can flush state before dying.
func gracefulStop(p *os.Process) {
	_ = p.Signal(syscall.SIGTERM)
}
