//go:build !windows && !linux

package xray

import "fmt"

// processIdentity cannot inspect other processes on this platform, so leftover
// processes can never be proven to be ours.
func processIdentity(pid int) (ProcessIdentity, bool, error) {
	return ProcessIdentity{PID: pid}, true, fmt.Errorf("process inspection is not supported on this platform")
}

func terminateProcess(pid int) error {
	return fmt.Errorf("terminating recorded processes is not supported on this platform")
}
