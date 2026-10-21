//go:build linux

package telemetry

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

var (
	cpuMu         sync.Mutex
	lastCPUValues []uint64
)

// cpuPercent reads /proc/stat twice.
func cpuPercent() (float64, bool) {
	busy, total, ok := readCPUTimes()
	if !ok {
		return 0, false
	}
	cpuMu.Lock()
	defer cpuMu.Unlock()
	if lastCPUValues == nil {
		lastCPUValues = []uint64{busy, total}
		return 0, false
	}
	prevBusy, prevTotal := lastCPUValues[0], lastCPUValues[1]
	lastCPUValues = []uint64{busy, total}
	if total <= prevTotal {
		return 0, false
	}
	return float64(busy-prevBusy) / float64(total-prevTotal) * 100, true
}

func readCPUTimes() (uint64, uint64, bool) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	line, _, _ := strings.Cut(string(raw), "\n")
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	var total, idle uint64
	for i, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		total += v
		if i == 3 || i == 4 { // idle + iowait
			idle += v
		}
	}
	if total == 0 {
		return 0, 0, false
	}
	return total - idle, total, true
}

// memoryUsedPercent reads /proc/meminfo.
func memoryUsedPercent() (float64, bool) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	var total, available float64
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			available = v
		}
	}
	if total <= 0 {
		return 0, false
	}
	return (total - available) / total * 100, true
}

// diskUsedPercent uses statfs on the volume that holds dir.
func diskUsedPercent(dir string) (float64, bool) {
	if dir == "" {
		return 0, false
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	total := float64(st.Blocks) * float64(st.Bsize)
	free := float64(st.Bavail) * float64(st.Bsize)
	if total <= 0 {
		return 0, false
	}
	return (total - free) / total * 100, true
}

// loadAverage1 reads /proc/loadavg.
func loadAverage1() (float64, bool) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// netBytes sums the interface counters from /proc/net/dev.
func netBytes() (int64, int64, bool) {
	raw, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, false
	}
	var rx, tx int64
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		if name == "lo" || name == "" {
			continue
		}
		fields := strings.Fields(line[idx+1:])
		if len(fields) < 9 {
			continue
		}
		r, err1 := strconv.ParseInt(fields[0], 10, 64)
		t, err2 := strconv.ParseInt(fields[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rx += r
		tx += t
		found = true
	}
	return rx, tx, found
}
