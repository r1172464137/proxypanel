//go:build !windows && !linux

package telemetry

// cpuPercent is not implemented on this platform.
func cpuPercent() (float64, bool) { return 0, false }

// memoryUsedPercent is not implemented on this platform.
func memoryUsedPercent() (float64, bool) { return 0, false }

// diskUsedPercent is not implemented on this platform.
func diskUsedPercent(dir string) (float64, bool) { return 0, false }

// loadAverage1 is not implemented on this platform.
func loadAverage1() (float64, bool) { return 0, false }

// netBytes is not implemented on this platform.
func netBytes() (int64, int64, bool) { return 0, 0, false }
