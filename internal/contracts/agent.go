// Package contracts defines the versioned panel-agent wire protocol.
package contracts

import (
	"encoding/json"
	"time"
)

const SchemaVersion = "proxypanel.agent/v1"

const AgentEvidenceFormat = "proxypanel.agent-state-evidence/v1"

// AgentStateEvidence is an offline inspection artifact, not an Agent HTTP request.
// It omits tokens, configuration, user credentials and free-form error payloads.
type AgentStateEvidence struct {
	FormatVersion           string       `json:"format_version"`
	CapturedAt              time.Time    `json:"captured_at"`
	DatabaseSHA256          string       `json:"database_sha256"`
	StateSchemaVersion      string       `json:"state_schema_version"`
	AgentID                 string       `json:"agent_id"`
	ControlGeneration       string       `json:"control_generation"`
	AppliedRevision         int64        `json:"applied_revision"`
	PendingBatches          []UsageBatch `json:"pending_batches"`
	PendingReportCount      int64        `json:"pending_report_count"`
	PendingApplyIntentCount int64        `json:"pending_apply_intent_count"`
	// Legacy absence of an intent table is not proof that no apply was interrupted.
	ApplyIntentTrackingSupported bool `json:"apply_intent_tracking_supported"`
}

type RegistrationRequest struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Version string `json:"version"`
}
type RegistrationResponse struct {
	AgentID           string `json:"agent_id"`
	Token             string `json:"token"`
	ControlGeneration string `json:"control_generation"`
}
type AuthorizedUser struct {
	MemberID  string     `json:"member_id"`
	Email     string     `json:"email"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Config is an Xray document without a management API; the agent adds a local
// API listener. Users are matched to inbound clients by their stable email.
type Instance struct {
	ID     string           `json:"id"`
	NodeID string           `json:"node_id"`
	Config json.RawMessage  `json:"config"`
	Users  []AuthorizedUser `json:"users"`
}
type Snapshot struct {
	SchemaVersion     string     `json:"schema_version"`
	ControlGeneration string     `json:"control_generation"`
	Revision          int64      `json:"revision"`
	IssuedAt          time.Time  `json:"issued_at"`
	ExpectedUserCount int        `json:"expected_user_count"`
	Instances         []Instance `json:"instances"`
}
type InstanceResult struct {
	InstanceID               string `json:"instance_id"`
	State                    string `json:"state"`
	Error                    string `json:"error,omitempty"`
	AppliedUserCount         int    `json:"applied_user_count"`
	OldConnectionsTerminated bool   `json:"old_connections_terminated"`
}
type ApplyReport struct {
	ControlGeneration string           `json:"control_generation"`
	Revision          int64            `json:"revision"`
	State             string           `json:"state"`
	AppliedAt         time.Time        `json:"applied_at"`
	Results           []InstanceResult `json:"results"`
}
type UsageSample struct {
	NodeID         string    `json:"node_id"`
	MemberID       string    `json:"member_id"`
	CoreGeneration string    `json:"core_generation"`
	StartedAt      time.Time `json:"started_at"`
	EndedAt        time.Time `json:"ended_at"`
	UploadBytes    int64     `json:"upload_bytes"`
	DownloadBytes  int64     `json:"download_bytes"`
}
type UsageBatch struct {
	SchemaVersion string        `json:"schema_version"`
	BatchID       string        `json:"batch_id"`
	Samples       []UsageSample `json:"samples"`
}
type UsageAck struct {
	BatchID string `json:"batch_id"`
	Status  string `json:"status"` // accepted or duplicate, only after durable commit
}
type Heartbeat struct {
	Version       string             `json:"version"`
	XrayVersion   string             `json:"xray_version"`
	Revision      int64              `json:"revision"`
	SentAt        time.Time          `json:"sent_at"`
	UptimeSeconds int64              `json:"uptime_seconds"`
	QueueDepth    int64              `json:"queue_depth"`
	Metrics       map[string]float64 `json:"metrics"`
	Instances     []InstanceResult   `json:"instances"`
}
