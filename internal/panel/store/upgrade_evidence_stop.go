package store

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const UpgradeStopReviewFormat = "proxypanel.administrator-stop-review/v1"

// UpgradeStopReview is an administrator attestation, NOT an independent OS probe.
// Authority is the existing maintenance administrator's §6.4.3/6.4.4 review.
// The reviewer must retain external service, process, inventory and accounting
// records. Hashes bind those records; they cannot establish their truth.
type UpgradeStopReview struct {
	FormatVersion               string               `json:"format_version"`
	Administrator               string               `json:"administrator"`
	Confirmation                string               `json:"confirmation"` // exactly "administrator-reviewed"
	AgentID                     string               `json:"agent_id"`
	ServerID                    string               `json:"server_id"`
	AgentVersion                string               `json:"agent_version"`
	PostgreSQLSHA256            string               `json:"postgresql_sha256"`
	AgentExportSHA256           string               `json:"agent_export_sha256"`
	StateDatabaseSHA256         string               `json:"state_database_sha256"`
	ControlGeneration           int64                `json:"control_generation"`
	PostgreSQLControlGeneration int64                `json:"postgresql_control_generation"`
	PostgreSQLRevision          int64                `json:"postgresql_revision"`
	AppliedRevision             int64                `json:"applied_revision"`
	DesiredRevision             int64                `json:"desired_revision"`
	InventoryComplete           bool                 `json:"inventory_complete"`        // includes untracked/orphan processes
	ExecutionReconciled         bool                 `json:"execution_reconciled"`      // all operations, not just journal counts
	FinalAccountingVerified     bool                 `json:"final_accounting_verified"` // no unresolved final-sampling failure
	ReportsAcknowledged         bool                 `json:"reports_acknowledged"`
	TrafficStoppedAt            time.Time            `json:"traffic_stopped_at"`
	FinalAccountingAt           time.Time            `json:"final_accounting_at"`
	Fence                       RestartFenceReview   `json:"restart_fence"`
	AgentProcess                ProcessStopReview    `json:"agent_process"`
	Instances                   []InstanceStopReview `json:"instances"`
	ReviewedAt                  time.Time            `json:"reviewed_at"`
	Records                     []StopReviewRecord   `json:"records"`
}

type StopReviewRecord struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}
type RestartFenceReview struct {
	Mechanism       string    `json:"mechanism"` // administrator explains all restart paths disabled
	ServiceIdentity string    `json:"service_identity"`
	RecordIndex     int       `json:"record_index"`
	EstablishedAt   time.Time `json:"established_at"`
	ValidUntil      time.Time `json:"valid_until"` // must still cover check and import
}
type ProcessStopReview struct {
	PID              int       `json:"pid"`
	BootID           string    `json:"boot_id"`
	StartIdentity    string    `json:"start_identity"` // OS start ticks / equivalent, prevents PID reuse
	ExecutableSHA256 string    `json:"executable_sha256"`
	Observation      string    `json:"observation"` // observed-exit or os-identity-absent
	ObservedAt       time.Time `json:"observed_at"`
	RecordIndex      int       `json:"record_index"`
}
type InstanceStopReview struct {
	InstanceID       string            `json:"instance_id"`
	NodeID           string            `json:"node_id"`
	Generation       int64             `json:"generation"`
	ConfigRevision   int64             `json:"config_revision"`
	CoreGeneration   string            `json:"core_generation"`
	ValidFrom        time.Time         `json:"valid_from"`
	ValidTo          *time.Time        `json:"valid_to"`
	ReleasedRevision *int64            `json:"released_revision"`
	Process          ProcessStopReview `json:"process"`
}

func stopDigest(s string) bool { b, e := hex.DecodeString(s); return e == nil && len(b) == 32 }

// verifyAdministratorStop consumes a strict record, not arbitrary attachment text
// or two zero counters. Completeness of the local inventory and external facts
// remains explicitly attributed to the administrator, never to this tool.
func verifyAdministratorStop(a upgradeAgent, in *AgentEvidenceInput, data upgradeSnapshot, r *UpgradeReadiness) error {
	v, x := in.StopReview, in.Export
	if v == nil {
		return fmt.Errorf("administrator stop review missing")
	}
	now := time.Now().UTC()
	if v.FormatVersion != UpgradeStopReviewFormat || strings.TrimSpace(v.Administrator) == "" || v.Confirmation != "administrator-reviewed" ||
		v.AgentID != a.ID || v.ServerID != a.ServerID || v.AgentVersion != a.Version || v.PostgreSQLSHA256 != r.DatabaseSHA256 ||
		v.AgentExportSHA256 != in.SHA256 || !stopDigest(v.StateDatabaseSHA256) || v.StateDatabaseSHA256 != x.DatabaseSHA256 ||
		v.ControlGeneration != a.Generation || v.AppliedRevision != a.Applied || v.DesiredRevision != a.Desired {
		return fmt.Errorf("review identity, version, fingerprints or control watermarks mismatch")
	}
	controls, err := decodeUpgradeRows[struct {
		Generation int64 `json:"control_generation"`
		Revision   int64 `json:"revision_counter"`
	}](data, "control_state")
	if err != nil || len(controls) != 1 || controls[0].Generation != v.PostgreSQLControlGeneration || controls[0].Revision != v.PostgreSQLRevision || a.Generation != controls[0].Generation || a.Applied != a.Desired {
		return fmt.Errorf("PG control generation/revision mismatch")
	}
	if !v.InventoryComplete || !v.ExecutionReconciled || !v.FinalAccountingVerified || !v.ReportsAcknowledged || x.PendingReportCount != 0 || x.PendingApplyIntentCount != 0 {
		return fmt.Errorf("inventory, execution, final accounting or reports unresolved")
	}
	if v.Fence.Mechanism == "" || v.Fence.ServiceIdentity == "" || v.Fence.EstablishedAt.IsZero() || !v.Fence.ValidUntil.After(now) ||
		v.TrafficStoppedAt.IsZero() || v.FinalAccountingAt.Before(v.TrafficStoppedAt) || v.Fence.EstablishedAt.After(v.TrafficStoppedAt) ||
		x.CapturedAt.Before(v.AgentProcess.ObservedAt) || v.ReviewedAt.Before(x.CapturedAt) || v.ReviewedAt.After(now) || len(v.Records) == 0 || v.Fence.RecordIndex < 0 || v.Fence.RecordIndex >= len(v.Records) {
		return fmt.Errorf("restart fence or stop/capture/review ordering invalid")
	}
	process := func(p ProcessStopReview) bool {
		return p.PID > 0 && p.BootID != "" && p.StartIdentity != "" && stopDigest(p.ExecutableSHA256) &&
			(p.Observation == "observed-exit" || p.Observation == "os-identity-absent") && !p.ObservedAt.IsZero() &&
			!p.ObservedAt.Before(v.FinalAccountingAt) && !p.ObservedAt.After(v.AgentProcess.ObservedAt) &&
			p.RecordIndex >= 0 && p.RecordIndex < len(v.Records)
	}
	if !process(v.AgentProcess) {
		return fmt.Errorf("Agent termination observation invalid")
	}
	cores, err := decodeUpgradeRows[struct {
		ID         string
		NodeID     string `json:"node_id"`
		ServerID   string `json:"server_id"`
		AgentID    string `json:"agent_id"`
		Generation int64
		Revision   int64 `json:"config_revision"`
	}](data, "core_instances")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	processes := map[string]bool{}
	processes[fmt.Sprintf("%s/%d/%s", v.AgentProcess.BootID, v.AgentProcess.PID, v.AgentProcess.StartIdentity)] = true
	for _, i := range v.Instances {
		key := fmt.Sprintf("%s/%d/%s", i.InstanceID, i.Generation, i.ValidFrom)
		pidKey := fmt.Sprintf("%s/%d/%s", i.Process.BootID, i.Process.PID, i.Process.StartIdentity)
		if i.InstanceID == "" || i.NodeID == "" || i.CoreGeneration == "" || seen[key] || processes[pidKey] || !process(i.Process) || i.ValidFrom.IsZero() || i.ValidFrom.After(i.Process.ObservedAt) || i.ConfigRevision > a.Applied || i.Generation < 0 || (i.ValidTo != nil && (!i.ValidFrom.Before(*i.ValidTo) || i.ValidTo.After(i.Process.ObservedAt))) || (i.ReleasedRevision != nil && (*i.ReleasedRevision < 0 || *i.ReleasedRevision > a.Desired)) {
			return fmt.Errorf("invalid/duplicate instance or termination observation")
		}
		seen[key] = true
		processes[pidKey] = true
		owner := false
		for _, o := range r.owners {
			if o.AgentID == a.ID && o.ServerID == a.ServerID && o.NodeID == i.NodeID && o.ValidFrom.Equal(i.ValidFrom) && sameStopTime(o.ValidTo, i.ValidTo) && sameStopRevision(o.ReleasedRevision, i.ReleasedRevision) {
				owner = true
			}
		}
		if !owner {
			return fmt.Errorf("instance execution window is not reconciled")
		}
	}
	nodes, err := decodeUpgradeRows[upgradeNode](data, "nodes")
	if err != nil {
		return err
	}
	for _, n := range nodes {
		if n.ServerID != a.ServerID {
			continue
		}
		found := false
		for _, i := range v.Instances {
			if i.NodeID == n.ID {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("server node lacks instance reconciliation")
		}
	}
	for _, c := range cores {
		if c.AgentID != a.ID && !(c.AgentID == "" && c.ServerID == a.ServerID) {
			continue
		}
		match := false
		for _, i := range v.Instances {
			if c.ID == i.InstanceID && c.NodeID == i.NodeID && c.ServerID == a.ServerID && c.Generation == i.Generation && c.Revision == i.ConfigRevision {
				match = true
			}
		}
		if !match {
			return fmt.Errorf("PG instance absent or generation/revision differs")
		}
	}
	// Every asserted owner (including historical executions) needs its own stop
	// reconciliation; duplicate accepted batches do not bypass this prerequisite.
	for _, o := range r.owners {
		if o.AgentID == a.ID {
			match := false
			for _, i := range v.Instances {
				if i.NodeID == o.NodeID && i.ValidFrom.Equal(o.ValidFrom) && sameStopTime(i.ValidTo, o.ValidTo) && sameStopRevision(i.ReleasedRevision, o.ReleasedRevision) {
					match = true
				}
			}
			if !match {
				return fmt.Errorf("owner window lacks instance reconciliation")
			}
		}
	}
	for _, b := range x.PendingBatches {
		for _, s := range b.Samples {
			match := false
			for _, i := range v.Instances {
				if s.NodeID == i.NodeID && s.CoreGeneration == i.CoreGeneration && !s.StartedAt.Before(i.ValidFrom) && (i.ValidTo == nil || !s.EndedAt.After(*i.ValidTo)) && !s.EndedAt.After(i.Process.ObservedAt) {
					match = true
				}
			}
			if !match {
				return fmt.Errorf("queued sample has no reconciled core generation/window")
			}
		}
	}
	return nil
}
func sameStopTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}
func sameStopRevision(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
