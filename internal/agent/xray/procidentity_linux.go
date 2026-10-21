//go:build linux

package xray

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// processIdentity reports the executable path, boot relative start time and
// command line of a process. found is false when the pid does not exist.
func processIdentity(pid int) (ProcessIdentity, bool, error) {
	if pid <= 0 {
		return ProcessIdentity{}, false, nil
	}
	base := "/proc/" + strconv.Itoa(pid)
	stat, err := os.ReadFile(base + "/stat")
	if err != nil {
		if os.IsNotExist(err) {
			return ProcessIdentity{}, false, nil
		}
		return ProcessIdentity{PID: pid}, true, fmt.Errorf("read %s/stat: %w", base, err)
	}
	start, err := procStartTime(string(stat))
	if err != nil {
		return ProcessIdentity{PID: pid}, true, err
	}
	boot := bootTime()
	exe, err := os.Readlink(base + "/exe")
	if err != nil {
		return ProcessIdentity{PID: pid, Created: boot + "/" + start}, true, fmt.Errorf("read %s/exe: %w", base, err)
	}
	cmdline := ""
	if raw, err := os.ReadFile(base + "/cmdline"); err == nil {
		cmdline = strings.TrimSpace(strings.ReplaceAll(string(raw), "\x00", " "))
	}
	return ProcessIdentity{PID: pid, Executable: exe, Created: boot + "/" + start, CommandLine: cmdline}, true, nil
}

// procStartTime extracts field 22 (starttime) from /proc/<pid>/stat. The
// command name may contain spaces and parentheses, so parsing starts after the
// last ')'.
func procStartTime(stat string) (string, error) {
	idx := strings.LastIndex(stat, ")")
	if idx < 0 {
		return "", fmt.Errorf("unexpected /proc stat format")
	}
	fields := strings.Fields(stat[idx+1:])
	// fields[0] is the process state (field 3 of stat); starttime is field 22.
	if len(fields) < 20 {
		return "", fmt.Errorf("unexpected /proc stat format")
	}
	return fields[19], nil
}

// bootTime returns the kernel boot time so identities stay unique across
// reboots.
func bootTime() string {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return "?"
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "btime ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "btime "))
		}
	}
	return "?"
}

func terminateProcess(pid int) error {
	if pid <= 1 {
		return fmt.Errorf("refusing to terminate pid %d", pid)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		return fmt.Errorf("kill process %d: %w", pid, err)
	}
	return nil
}
