package xray

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ExitSignal exposes the process exit event without acquiring the lifecycle
// lock. The returned signal belongs to exactly the currently published core.
func (m *Manager) ExitSignal(id string) <-chan struct{} {
	if inst := m.lookup(id); inst != nil {
		if v := inst.view(); v.proc != nil {
			return v.proc.Exited()
		}
	}
	return nil
}

// UnconfirmedStops counts failed live processes, not crashes already observed.
func (m *Manager) UnconfirmedStops() int {
	n := 0
	for _, id := range m.runningIDs() {
		if inst := m.lookup(id); inst != nil {
			v := inst.view()
			if v.running && (v.state == StateFailed || v.state == StateStopping) {
				n++
			}
		}
	}
	return n
}

func processExited(p Proc) bool {
	if p == nil {
		return true
	}
	select {
	case <-p.Exited():
		return true
	default:
		return false
	}
}
func authorizationTightened(inst *instance, t Target) bool {
	for email, member := range inst.emailToMember {
		if t.EmailToMember[email] != member {
			return true
		}
	}
	return false
}

// SetSampleHook wires final accounting for ALL stop paths, including shutdown
// and rejected configurations. Must be called before concurrent use.
func (m *Manager) SetSampleHook(h SampleHook) { m.beforeStop = h }

// NeedsRecovery does not acquire a lifecycle lock or perform I/O.
func (m *Manager) NeedsRecovery(id string) bool {
	inst := m.lookup(id)
	if inst == nil {
		return true
	}
	v := inst.view()
	return !v.running || v.state == StateFailed
}
func (m *Manager) RunningSince(id string) time.Time {
	inst := m.lookup(id)
	if inst == nil || !inst.view().running {
		return time.Time{}
	}
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.startedAt
}

// ReapUntracked also covers removed nodes after a crash: their only recovery
// record may be a pid file. Ownership checks are identical to normal reclaim.
// A failure keeps that record; callers must not announce cleanup success.
func (m *Manager) ReapUntracked(ctx context.Context) error {
	dirs, err := os.ReadDir(filepath.Join(m.root, "instances"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.IsDir() || m.isTracked(d.Name()) {
			continue
		}
		dir, err := m.instanceDir(d.Name())
		if err != nil {
			continue
		}
		if err := m.reapStale(dir); err != nil {
			return fmt.Errorf("cleanup %s: %w", d.Name(), err)
		}
	}
	return nil
}
