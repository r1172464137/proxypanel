// Package access is the single authorization implementation used by the panel,
// the member subscription output and the agent snapshot (product.md §7.2,
// plan §2.1). No other package may re-interpret the rules.
package access

import (
	"sort"
	"time"
)

// AlgorithmVersion is shared by panel, subscription output and snapshots.
const AlgorithmVersion = "access/v1"

// Proxy access states (product.md §6.2).
const (
	ProxyEnabled        = "enabled"
	ProxyPausedByAdmin  = "paused_by_admin"
	ProxyExpired        = "expired"
	ProxyOverQuota      = "over_quota"
	ProxyNoSubscription = "no_subscription"
	ProxyAccountBlocked = "account_blocked"
)

// Reason codes returned to clients and the agent. They are stable strings.
const (
	ReasonOK                = "ok"
	ReasonAccountSuspended  = "account_suspended"
	ReasonAccountArchived   = "account_archived"
	ReasonProxyPaused       = "proxy_paused"
	ReasonNoBinding         = "no_binding"
	ReasonBindingExpired    = "binding_expired"
	ReasonSubscriptionPaused = "subscription_access_paused"
	ReasonSubscriptionArchived = "subscription_archived"
	ReasonOverQuota         = "over_quota"
	ReasonNoNodesAuthorized = "no_nodes_authorized"
	ReasonNodeDisabled      = "node_disabled"
	ReasonNodeNotAuthorized = "node_not_authorized"
)

// NodeState is the minimal node view the decision needs.
type NodeState struct {
	ID       string
	Name     string
	Enabled  bool
	GroupIDs []string
}

// Input is the complete decision input. Overrides only ever replace a value
// (quota); they can never skip an upper level restriction.
type Input struct {
	Now        time.Time
	MemberID   string
	AccountActive bool
	AccountArchived bool
	ProxyEnabled    bool

	BindingPresent   bool
	SubscriptionID   string
	BindingExpiresAt *time.Time
	BoundAt          *time.Time

	SubscriptionAccessEnabled bool
	SubscriptionArchived      bool
	SubscriptionQuotaBytes    *int64

	OverrideQuotaEnabled bool
	OverrideQuotaBytes   *int64

	UsedBytes          int64
	AuthorizedGroupIDs []string
	Nodes              []NodeState
}

// Step is one evaluated rule, shown in the UI and the details of failures.
type Step struct {
	Step   string `json:"step"`
	Result string `json:"result"` // pass | fail
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Decision is the single source of truth for "may this member use the proxy,
// and which nodes".
type Decision struct {
	AlgorithmVersion    string            `json:"algorithm_version"`
	MemberID            string            `json:"member_id"`
	Access              bool              `json:"access"`
	ProxyAccess         string            `json:"proxy_access"`
	Reasons             []string          `json:"access_reasons"`
	Steps               []Step            `json:"access_chain"`
	EffectiveQuotaBytes *int64            `json:"effective_quota_bytes"`
	QuotaSource         string            `json:"quota_source"`
	UsedBytes           int64             `json:"used_bytes"`
	ExpiresAt           *time.Time        `json:"expires_at"`
	AllowedNodeIDs      []string          `json:"allowed_node_ids"`
	NodeReasons         map[string]string `json:"node_reasons"`
}

// Decide evaluates the conjunction of plan §2.1 in the frozen order.
func Decide(in Input) Decision {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	d := Decision{
		AlgorithmVersion: AlgorithmVersion,
		MemberID:         in.MemberID,
		AllowedNodeIDs:   []string{},
		NodeReasons:      map[string]string{},
		Reasons:          []string{},
		UsedBytes:        in.UsedBytes,
		ExpiresAt:        in.BindingExpiresAt,
	}
	quota, src := EffectiveQuota(in.SubscriptionQuotaBytes, in.OverrideQuotaBytes, in.OverrideQuotaEnabled)
	d.EffectiveQuotaBytes = quota
	d.QuotaSource = src

	fail := func(step, reason, detail string) Decision {
		d.Steps = append(d.Steps, Step{Step: step, Result: "fail", Reason: reason, Detail: detail})
		d.Reasons = append(d.Reasons, reason)
		return d
	}

	// 1. account status
	if in.AccountArchived {
		d.ProxyAccess = ProxyAccountBlocked
		return fail("account_status", ReasonAccountArchived, "member archived")
	}
	if !in.AccountActive {
		d.ProxyAccess = ProxyAccountBlocked
		return fail("account_status", ReasonAccountSuspended, "member inactive")
	}
	d.Steps = append(d.Steps, Step{Step: "account_status", Result: "pass"})

	// 2. proxy access pause (independent, hard revocation)
	if !in.ProxyEnabled {
		d.ProxyAccess = ProxyPausedByAdmin
		return fail("proxy_access", ReasonProxyPaused, "proxy access paused by admin")
	}
	d.Steps = append(d.Steps, Step{Step: "proxy_access", Result: "pass"})

	// 3. binding validity
	if !in.BindingPresent || in.SubscriptionID == "" {
		d.ProxyAccess = ProxyNoSubscription
		return fail("binding_validity", ReasonNoBinding, "member has no subscription binding")
	}
	if in.BindingExpiresAt != nil && !in.BindingExpiresAt.After(now) {
		d.ProxyAccess = ProxyExpired
		return fail("binding_validity", ReasonBindingExpired, "binding expired")
	}
	d.Steps = append(d.Steps, Step{Step: "binding_validity", Result: "pass"})

	// 4. subscription allows existing members
	if in.SubscriptionArchived {
		d.ProxyAccess = ProxyNoSubscription
		return fail("subscription_access", ReasonSubscriptionArchived, "subscription archived")
	}
	if !in.SubscriptionAccessEnabled {
		d.ProxyAccess = ProxyPausedByAdmin
		return fail("subscription_access", ReasonSubscriptionPaused, "subscription access paused")
	}
	d.Steps = append(d.Steps, Step{Step: "subscription_access", Result: "pass"})

	// 5. quota (authoritative in the panel ledger)
	if quota != nil && in.UsedBytes >= *quota {
		d.ProxyAccess = ProxyOverQuota
		return fail("quota", ReasonOverQuota, "used bytes reached the effective quota")
	}
	d.Steps = append(d.Steps, Step{Step: "quota", Result: "pass"})

	// 6+7. node level: enabled and covered by an authorized group
	authorized := map[string]bool{}
	for _, g := range in.AuthorizedGroupIDs {
		authorized[g] = true
	}
	for _, n := range in.Nodes {
		switch {
		case !n.Enabled:
			d.NodeReasons[n.ID] = ReasonNodeDisabled
			continue
		case !coversNode(n, authorized):
			d.NodeReasons[n.ID] = ReasonNodeNotAuthorized
			continue
		}
		d.NodeReasons[n.ID] = ReasonOK
		d.AllowedNodeIDs = append(d.AllowedNodeIDs, n.ID)
	}
	if len(d.AllowedNodeIDs) == 0 {
		d.Reasons = append(d.Reasons, ReasonNoNodesAuthorized)
	}
	d.Steps = append(d.Steps, Step{
		Step:   "node_authorization",
		Result: "pass",
		Detail: "allowed nodes: " + itoa(len(d.AllowedNodeIDs)),
	})

	d.Access = true
	d.ProxyAccess = ProxyEnabled
	if len(d.Reasons) == 0 {
		d.Reasons = append(d.Reasons, ReasonOK)
	}
	return d
}

func coversNode(n NodeState, authorized map[string]bool) bool {
	for _, g := range n.GroupIDs {
		if authorized[g] {
			return true
		}
	}
	return false
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// EffectiveQuota resolves the quota value and its source. nil means unlimited.
func EffectiveQuota(subscriptionQuota, overrideQuota *int64, overrideEnabled bool) (*int64, string) {
	if overrideEnabled {
		if overrideQuota == nil {
			return nil, "override"
		}
		v := *overrideQuota
		return &v, "override"
	}
	if subscriptionQuota == nil {
		return nil, "subscription"
	}
	v := *subscriptionQuota
	return &v, "subscription"
}

// DedupeGroupIDs returns the unique, sorted group id list (authorization output
// is deduplicated by node, never by group).
func DedupeGroupIDs(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
