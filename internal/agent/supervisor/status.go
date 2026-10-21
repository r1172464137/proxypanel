package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"proxypanel/internal/contracts"
)

// statusFileName is the sanitized state report the Agent publishes next to its
// SQLite database. It exists so an operator (and the isolated recovery
// acceptance) can observe what this Agent has DURABLY committed without
// stopping it, copying a live WAL database or touching the token: the report
// holds only state codes and counters - no agent/member id, email, config or
// credential is ever written to it.
const statusFileName = "status.json"

// statusReport is the published document. Schema 1 is the only version.
type statusReport struct {
	Schema    int            `json:"schema"`
	UpdatedAt string         `json:"updated_at"`
	Instances []statusRecord `json:"instances"`
}

// statusRecord is one persisted instance record, reduced to non-sensitive
// counters. Users is the persisted authorization size, AppliedUsers the last
// reported applied count, ExpiryCleared states that no user expiry remains.
type statusRecord struct {
	State         string `json:"state"`
	Users         int    `json:"users"`
	AppliedUsers  int    `json:"applied_users"`
	ExpiryCleared bool   `json:"expiry_cleared"`
	HasError      bool   `json:"error"`
}

// writeStatus publishes the sanitized state report. It is best effort on
// purpose: publishing evidence must never block or fail an apply, and a missing
// report only means the operator has to look at the SQLite export instead.
func (s *Supervisor) writeStatus() {
	if s.store == nil || s.cfg == nil || strings.TrimSpace(s.cfg.StateDir) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	records, err := s.store.Instances(ctx)
	if err != nil {
		return
	}
	report := statusReport{
		Schema:    1,
		UpdatedAt: s.now().UTC().Format(time.RFC3339Nano),
		Instances: make([]statusRecord, 0, len(records)),
	}
	for _, rec := range records {
		report.Instances = append(report.Instances, statusRecord{
			State:         rec.State,
			Users:         len(rec.Users),
			AppliedUsers:  rec.AppliedUserCount,
			ExpiryCleared: rec.NextExpiry.IsZero(),
			HasError:      strings.TrimSpace(rec.Error) != "",
		})
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return
	}
	path := filepath.Join(s.cfg.StateDir, statusFileName)
	tmp := path + ".tmp"
	// The state directory is 0700 and the file 0600: it never contains a secret,
	// but it still describes this node's authorization.
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return
	}
}

// filterExpiredUsers drops every user whose delivered expiry already passed and
// returns the earliest expiry that remains. It is exactly the rule the snapshot
// and local expiry paths apply through xray.Prepare, so a persisted instance
// record can never be wider than what this node's clock already proves.
func filterExpiredUsers(users []contracts.AuthorizedUser, now time.Time) ([]contracts.AuthorizedUser, time.Time) {
	var kept []contracts.AuthorizedUser
	var next time.Time
	for _, u := range users {
		if u.ExpiresAt != nil && !u.ExpiresAt.After(now) {
			continue
		}
		kept = append(kept, u)
		if u.ExpiresAt != nil {
			exp := u.ExpiresAt.UTC()
			if next.IsZero() || exp.Before(next) {
				next = exp
			}
		}
	}
	return kept, next
}
