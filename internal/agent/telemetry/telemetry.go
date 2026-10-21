// Package telemetry collects the host metrics the agent can actually measure
// without extra dependencies.
package telemetry

import (
	"math"
)

// Collect returns the metric map for a heartbeat. Only metrics that could be
// measured are present; missing keys mean "not available on this platform".
func Collect(dir string) map[string]float64 {
	out := map[string]float64{}
	if v, ok := cpuPercent(); ok {
		out["cpu_percent"] = round2(v)
	}
	if v, ok := memoryUsedPercent(); ok {
		out["memory_used_percent"] = round2(v)
	}
	if v, ok := diskUsedPercent(dir); ok {
		out["disk_used_percent"] = round2(v)
	}
	if v, ok := loadAverage1(); ok {
		out["load1"] = round2(v)
	}
	rx, tx, ok := netBytes()
	if ok {
		out["net_rx_bytes"] = float64(rx)
		out["net_tx_bytes"] = float64(tx)
	}
	return out
}

func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*100) / 100
}
