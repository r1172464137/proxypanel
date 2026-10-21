package telemetry

import (
	"math"
	"testing"
)

func TestCollectReturnsSaneMetrics(t *testing.T) {
	dir := t.TempDir()
	// The CPU metric needs two samples.
	first := Collect(dir)
	second := Collect(dir)
	if len(first) == 0 && len(second) == 0 {
		t.Skip("no host metrics are available on this platform")
	}
	for key, value := range second {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			t.Fatalf("%s is not a finite number: %v", key, value)
		}
		if value < 0 {
			t.Fatalf("%s must not be negative: %v", key, value)
		}
	}
	if v, ok := second["disk_used_percent"]; ok && (v < 0 || v > 100) {
		t.Fatalf("disk_used_percent out of range: %v", v)
	}
	if v, ok := second["memory_used_percent"]; ok && (v < 0 || v > 100) {
		t.Fatalf("memory_used_percent out of range: %v", v)
	}
	if v, ok := second["cpu_percent"]; ok && (v < 0 || v > 100) {
		t.Fatalf("cpu_percent out of range: %v", v)
	}
}
