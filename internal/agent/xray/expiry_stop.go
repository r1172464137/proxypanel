package xray

import (
	"context"
	"net"
	"strconv"
	"time"
)

// StopExpired bypasses the technical lifecycle lock using its immutable process
// view. It only stops the published, expired process, never a replacement. The
// lifecycle owner will observe Exited; its last-moment deadline guard prevents
// starting the now expired target after a slow validation completes.
func (m *Manager) StopExpired(ctx context.Context, id string, now time.Time) (Result, bool) {
	inst := m.lookup(id)
	if inst == nil {
		return Result{}, false
	}
	v := inst.view()
	if !v.running || v.nextExpiry.IsZero() || now.Before(v.nextExpiry) {
		return Result{InstanceID: id, State: StateStopped, OldConnectionsTerminated: !v.running}, true
	}
	if m.beforeStop != nil && v.apiPort > 0 {
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		m.beforeStop(hctx, v.nodeID, v.coreGeneration, net.JoinHostPort(APIListen, strconv.Itoa(v.apiPort)), copyMap(v.emailToMember))
		cancel()
	}
	if !m.stopProc(inst, v.proc) {
		return Result{InstanceID: id, State: StateFailed, Error: "expired core exit unconfirmed"}, true
	}
	return Result{InstanceID: id, State: StateStopped, OldConnectionsTerminated: true}, true
}
