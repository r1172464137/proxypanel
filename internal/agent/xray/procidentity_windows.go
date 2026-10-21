//go:build windows

package xray

import (
	"errors"
	"fmt"
	"strconv"

	"golang.org/x/sys/windows"
)

// processIdentity reports the full image path and creation time of a process.
// found is false when the pid does not exist; an error means the process exists
// but cannot be inspected (and therefore cannot be proven to be ours).
func processIdentity(pid int) (ProcessIdentity, bool, error) {
	if pid <= 0 {
		return ProcessIdentity{}, false, nil
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_FOUND) {
			return ProcessIdentity{}, false, nil
		}
		return ProcessIdentity{PID: pid}, true, fmt.Errorf("open process %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return ProcessIdentity{PID: pid}, true, fmt.Errorf("process times of %d: %w", pid, err)
	}
	image, imageErr := windowsProcessImage(h)
	identity := ProcessIdentity{PID: pid, Executable: image, Created: strconv.FormatInt(creation.Nanoseconds(), 10)}
	if imageErr != nil {
		return identity, true, imageErr
	}
	return identity, true, nil
}

func windowsProcessImage(h windows.Handle) (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", fmt.Errorf("query image name: %w", err)
	}
	return windows.UTF16ToString(buf[:size]), nil
}

func terminateProcess(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("open process %d for termination: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, 1); err != nil {
		return fmt.Errorf("terminate process %d: %w", pid, err)
	}
	return nil
}
