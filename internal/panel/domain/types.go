// Package domain holds the persisted panel entities and the JSON shapes that
// the frozen HTTP contract exposes. Unknown nested configuration is preserved
// through Object so that nothing is silently dropped on decode.
package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// Object is a JSON object whose nested values are kept as raw JSON.
type Object map[string]json.RawMessage

func NewObject() Object { return Object{} }

func (o Object) MarshalJSON() ([]byte, error) {
	if o == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(map[string]json.RawMessage(o))
}

func (o *Object) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*o = Object{}
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("invalid JSON object: %w", err)
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	*o = Object(m)
	return nil
}

func (o Object) Clone() Object {
	out := Object{}
	for k, v := range o {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out
}

func (o Object) Get(key string) (json.RawMessage, bool) {
	v, ok := o[key]
	return v, ok
}

func (o Object) Has(key string) bool {
	_, ok := o[key]
	return ok
}

func (o Object) Delete(key string) { delete(o, key) }

// Set stores v as raw JSON.
func (o Object) Set(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	o[key] = b
	return nil
}

func (o Object) String(key string) (string, bool) {
	raw, ok := o[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

func (o Object) Int(key string) (int64, bool) {
	raw, ok := o[key]
	if !ok {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		var f float64
		if err2 := json.Unmarshal(raw, &f); err2 != nil {
			return 0, false
		}
		return int64(f), true
	}
	return n, true
}

func (o Object) Bool(key string) (bool, bool) {
	raw, ok := o[key]
	if !ok {
		return false, false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, false
	}
	return b, true
}

func (o Object) Strings(key string) ([]string, bool) {
	raw, ok := o[key]
	if !ok {
		return nil, false
	}
	var s []string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false
	}
	return s, true
}

func (o Object) Sub(key string) (Object, bool) {
	raw, ok := o[key]
	if !ok {
		return nil, false
	}
	var sub Object
	if err := json.Unmarshal(raw, &sub); err != nil {
		return nil, false
	}
	return sub, true
}

// Time aliases JSON encoding of UTC timestamps (RFC3339).
type Time = time.Time

// PeriodRule is the pending (next cycle) or active cycle rule of a
// subscription. A changed rule never rewrites the running cycle.
type PeriodRule struct {
	AnchorDay     int        `json:"anchor_day"`
	Timezone      string     `json:"timezone"`
	EffectiveFrom *time.Time `json:"effective_from,omitempty"`
	SubmittedAt   *time.Time `json:"submitted_at,omitempty"`
}

type Server struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Address   string     `json:"address"`
	Region    string     `json:"region"`
	Notes     string     `json:"notes"`
	ExpiresAt *time.Time `json:"expires_at"`
	CostMinor *int64     `json:"cost_minor"`
	Currency  string     `json:"currency"`
	Archived  bool       `json:"archived"`
	Version   int64      `json:"version"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type Node struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ServerID string `json:"server_id"`
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	// ExternalPort 是客户端连接的端口；0/NULL 表示与 Port 相同。
	// 用于「对外 443 → 实际监听 20101」这类中转场景：转发由运维在机器上配，
	// 面板只负责把对外地址写进成员配置。
	ExternalPort     int       `json:"external_port,omitempty"`
	Listen           string    `json:"listen"`
	Protocol         string    `json:"protocol"`
	Flow             string    `json:"flow"`
	Cipher           string    `json:"cipher"`
	StreamSettings   Object    `json:"stream_settings"`
	ProtocolSettings Object    `json:"protocol_settings"`
	ClientSettings   Object    `json:"client_settings"`
	SortOrder        int       `json:"sort_order"`
	Archived         bool      `json:"archived"`
	Version          int64     `json:"version"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`

	// ServerKey is the node's server side secret (REALITY private key or
	// Shadowsocks server password). It is encrypted at rest, write-only over
	// the API and never included in member facing output.
	ServerKey string `json:"-"`
	// ServerKeySet reports whether a server secret exists (admin read only).
	ServerKeySet bool `json:"server_key_set"`
}

type NodeGroup struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	NodeIDs     []string  `json:"node_ids"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Subscription struct {
	ID                string      `json:"id"`
	Name              string      `json:"name"`
	Description       string      `json:"description"`
	NodeGroupIDs      []string    `json:"node_group_ids"`
	QuotaBytes        *int64      `json:"quota_bytes"`
	Timezone          string      `json:"timezone"`
	AnchorDay         int         `json:"anchor_day"`
	DefaultValidDays  *int        `json:"default_valid_days"`
	AllowNewBindings  bool        `json:"allow_new_bindings"`
	AccessEnabled     bool        `json:"access_enabled"`
	Archived          bool        `json:"archived"`
	TemplateID        *string     `json:"template_id"`
	PendingPeriodRule *PeriodRule `json:"pending_period_rule,omitempty"`
	BoundMembers      int         `json:"bound_members"`
	Version           int64       `json:"version"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
}

type Member struct {
	ID                   string      `json:"id"`
	Username             string      `json:"username"`
	Role                 string      `json:"role"`
	Active               bool        `json:"active"`
	ProxyEnabled         bool        `json:"proxy_enabled"`
	Archived             bool        `json:"archived"`
	SubscriptionID       *string     `json:"subscription_id"`
	ExpiresAt            *time.Time  `json:"expires_at"`
	QuotaOverrideBytes   *int64      `json:"quota_override_bytes"`
	QuotaOverrideEnabled bool        `json:"quota_override_enabled"`
	OverQuota            bool        `json:"over_quota"`
	BoundAt              *time.Time  `json:"bound_at"`
	PeriodStart          *time.Time  `json:"period_start"`
	PeriodEnd            *time.Time  `json:"period_end"`
	PeriodTimezone       string      `json:"period_timezone"`
	PeriodAnchorDay      int         `json:"period_anchor_day"`
	PendingPeriodRule    *PeriodRule `json:"pending_period_rule,omitempty"`
	LastLoginAt          *time.Time  `json:"last_login_at"`
	Version              int64       `json:"version"`
	CreatedAt            time.Time   `json:"created_at"`
	UpdatedAt            time.Time   `json:"updated_at"`

	// Derived values (computed per request, never stored).
	DataComplete        *bool    `json:"data_complete,omitempty"`
	DataIssues          []string `json:"data_issues"`
	UsedBytes           int64    `json:"used_bytes"`
	EffectiveQuotaBytes *int64   `json:"effective_quota_bytes"`
	AccessReasons       []string `json:"access_reasons"`
	ProxyAccess         string   `json:"proxy_access"`
	AllowedNodeIDs      []string `json:"allowed_node_ids"`
	PasswordHash        string   `json:"-"`
}

type FormatStatus struct {
	Format    string `json:"format"`
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

type SetupStep struct {
	Key    string `json:"key"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type OverviewSetup struct {
	Steps          []SetupStep `json:"steps"`
	CompletedCount int         `json:"completed_count"`
	BlockingReason string      `json:"blocking_reason,omitempty"`
}

type Template struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Format      string    `json:"format"`
	Content     Object    `json:"content"`
	Description string    `json:"description"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Agent struct {
	ID                string          `json:"id"`
	ServerID          string          `json:"server_id"`
	Name              string          `json:"name"`
	Version           string          `json:"version"`
	XrayVersion       string          `json:"xray_version"`
	TokenPrefix       string          `json:"token_prefix"`
	TokenHash         string          `json:"-"`
	ControlGeneration int64           `json:"control_generation"`
	DesiredRevision   int64           `json:"desired_revision"`
	AppliedRevision   int64           `json:"applied_revision"`
	AppliedAt         *time.Time      `json:"applied_at"`
	LastHeartbeatAt   *time.Time      `json:"last_heartbeat_at"`
	UptimeSeconds     int64           `json:"uptime_seconds"`
	QueueDepth        int64           `json:"queue_depth"`
	Metrics           Object          `json:"metrics"`
	Instances         json.RawMessage `json:"instances"`
	Online            bool            `json:"online"`
	OfflineSeconds    int64           `json:"offline_seconds"`
	Archive           bool            `json:"archived"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type CoreInstance struct {
	ID             string     `json:"id"`
	ServerID       string     `json:"server_id"`
	AgentID        *string    `json:"agent_id"`
	NodeID         string     `json:"node_id"`
	NodeName       string     `json:"node_name"`
	Generation     int64      `json:"generation"`
	ConfigRevision int64      `json:"config_revision"`
	State          string     `json:"state"`
	LastStartedAt  *time.Time `json:"last_started_at"`
	AppliedAt      *time.Time `json:"applied_at"`
	LastError      string     `json:"last_error"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Credential is a member x node proxy credential. Secret is only populated for
// internal config generation and never marshalled.
type Credential struct {
	ID            string     `json:"id"`
	MemberID      string     `json:"member_id"`
	NodeID        string     `json:"node_id"`
	NodeName      string     `json:"node_name"`
	Kind          string     `json:"kind"`
	State         string     `json:"state"`
	CreatedAt     time.Time  `json:"created_at"`
	RevokedAt     *time.Time `json:"revoked_at"`
	RotatedReason string     `json:"rotated_reason"`
	Secret        string     `json:"-"`
}

type SubscriptionToken struct {
	ID        string     `json:"id"`
	MemberID  string     `json:"member_id"`
	CreatedAt time.Time  `json:"created_at"`
	RotatedAt *time.Time `json:"rotated_at"`
	RevokedAt *time.Time `json:"revoked_at"`
	Token     string     `json:"token,omitempty"`
	URL       string     `json:"url,omitempty"`
}

type Invitation struct {
	ID             string     `json:"id"`
	SubscriptionID *string    `json:"subscription_id"`
	ExpiresAt      time.Time  `json:"expires_at"`
	MaxUses        int        `json:"max_uses"`
	UsedCount      int        `json:"used_count"`
	UsedAt         *time.Time `json:"used_at"`
	UsedByMemberID *string    `json:"used_by_member_id"`
	RevokedAt      *time.Time `json:"revoked_at"`
	State          string     `json:"state"`
	CreatedBy      string     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	Code           string     `json:"code,omitempty"`
}

type Session struct {
	ID         string     `json:"id"`
	MemberID   string     `json:"member_id"`
	CSRFToken  string     `json:"csrf_token"`
	TokenHash  string     `json:"-"`
	IP         string     `json:"ip"`
	UserAgent  string     `json:"user_agent"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	Current    bool       `json:"current"`
}

type Task struct {
	Revision    *int64     `json:"revision"`
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	AgentID     string     `json:"agent_id"`
	NodeID      string     `json:"node_id"`
	MemberID    string     `json:"member_id"`
	BatchID     string     `json:"batch_id"`
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	MaxAttempts int        `json:"max_attempts"`
	LastError   string     `json:"last_error"`
	AvailableAt time.Time  `json:"available_at"`
	LockedAt    *time.Time `json:"locked_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type Event struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Severity    string     `json:"severity"`
	SubjectType string     `json:"subject_type"`
	SubjectID   string     `json:"subject_id"`
	Message     string     `json:"message"`
	State       string     `json:"state"`
	DedupeKey   string     `json:"dedupe_key,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	AckedAt     *time.Time `json:"acked_at"`
	AckedBy     string     `json:"acked_by"`
}

type AuditLog struct {
	ID            string    `json:"id"`
	ActorID       string    `json:"actor_id"`
	ActorUsername string    `json:"actor_username"`
	Action        string    `json:"action"`
	TargetType    string    `json:"target_type"`
	TargetID      string    `json:"target_id"`
	Detail        Object    `json:"detail"`
	IP            string    `json:"ip"`
	RequestID     string    `json:"request_id"`
	CreatedAt     time.Time `json:"created_at"`
}

type Settings struct {
	Timezone         string    `json:"timezone"`
	RetentionDays    int       `json:"retention_days"`
	TelegramEnabled  bool      `json:"telegram_enabled"`
	TelegramChatID   string    `json:"telegram_chat_id"`
	TelegramTokenSet bool      `json:"telegram_token_set"`
	TemplateDefaults Object    `json:"template_defaults"`
	Version          int64     `json:"version"`
	UpdatedAt        time.Time `json:"updated_at"`

	// TelegramToken is write-only; it is never returned by the API.
	TelegramToken string `json:"-"`
}

type ControlState struct {
	ControlGeneration int64      `json:"control_generation"`
	RevisionCounter   int64      `json:"revision_counter"`
	RecoveryMode      bool       `json:"recovery_mode"`
	RecoveryEnteredAt *time.Time `json:"recovery_entered_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type QuotaAdjustment struct {
	ID          string     `json:"id"`
	MemberID    string     `json:"member_id"`
	Bytes       int64      `json:"bytes"`
	Reason      string     `json:"reason"`
	PeriodStart *time.Time `json:"period_start"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
}

type UsageRow struct {
	MemberID         string    `json:"member_id"`
	NodeID           string    `json:"node_id"`
	PeriodStart      time.Time `json:"period_start"`
	PeriodEnd        time.Time `json:"period_end"`
	UpBytes          int64     `json:"up_bytes"`
	DownBytes        int64     `json:"down_bytes"`
	Estimated        bool      `json:"estimated"`
	BoundarySplit    bool      `json:"boundary_split"`
	Correction       bool      `json:"correction"`
	Direction        string    `json:"direction"`
	AlgorithmVersion string    `json:"algorithm_version"`
}

type Batch struct {
	ID             string      `json:"id"`
	PreviewID      string      `json:"preview_id"`
	Operation      string      `json:"operation"`
	State          string      `json:"state"`
	IdempotencyKey string      `json:"idempotency_key"`
	CreatedBy      string      `json:"created_by"`
	CreatedAt      time.Time   `json:"created_at"`
	CompletedAt    *time.Time  `json:"completed_at"`
	Results        []BatchItem `json:"results"`
	Summary        Object      `json:"summary"`
}

type BatchItem struct {
	ID       string `json:"id"`
	BatchID  string `json:"batch_id"`
	MemberID string `json:"member_id"`
	NodeID   string `json:"node_id"`
	AgentID  string `json:"agent_id"`
	State    string `json:"state"`
	TaskID   string `json:"task_id"`
	Error    string `json:"error"`
}
