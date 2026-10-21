package xray

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// pidFileName is the per instance file that records which core process the
// agent started, so a crash cannot leave an unmanaged process behind.
const pidFileName = "xray.pid"

// ErrUnverifiedProcess is returned when a recorded leftover process cannot be
// proven to belong to this agent. The process is then left untouched and the
// instance reports a clear error instead of guessing.
var ErrUnverifiedProcess = errors.New("cannot prove ownership of a leftover core process")

// ProcessIdentity is what the operating system can prove about a live process.
type ProcessIdentity struct {
	PID         int
	Executable  string // full image path
	Created     string // opaque creation token; compared exactly
	CommandLine string // empty when the platform does not expose it
}

// pidRecord is persisted in the instance directory.
type pidRecord struct {
	PID            int    `json:"pid"`
	Created        string `json:"created"`
	Executable     string `json:"executable"`
	ConfigPath     string `json:"config_path"`
	InstanceID     string `json:"instance_id"`
	CoreGeneration string `json:"core_generation"`
	RecordedAt     int64  `json:"recorded_at"`
}

func writePidFile(dir string, rec pidRecord) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, pidFileName), raw, 0o600)
}

func readPidFile(dir string) (pidRecord, error) {
	raw, err := os.ReadFile(filepath.Join(dir, pidFileName))
	if err != nil {
		return pidRecord{}, err
	}
	var rec pidRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return pidRecord{}, fmt.Errorf("decode pid record: %w", err)
	}
	return rec, nil
}

func removePidFile(dir string) {
	_ = os.Remove(filepath.Join(dir, pidFileName))
}

// samePath compares two paths, case-insensitively on Windows.
func samePath(a, b string) bool {
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ca, cb)
	}
	return ca == cb
}
