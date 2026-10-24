package store

import (
	"context"
	"errors"
	"time"

	"proxypanel/internal/panel/clientconfig"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/protocol"
)

// ProductStatus reads persisted evidence only. In particular it must not call
// EvaluateAccess: that path can advance periods and reconcile credentials.
func (s *Store) ProductStatus(ctx context.Context, id string) (*AccessResult, error) {
	m, err := s.GetMember(ctx, id)
	if err != nil {
		return nil, err
	}
	ev, err := s.Evidence(ctx, m)
	if err != nil {
		return nil, err
	}
	issues := []string{}
	var proven, unattributable bool
	err = s.DB.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM member_periods WHERE member_id=$1 AND period_start=$2 AND period_end=$3 AND timezone=$4 AND anchor_day=$5),
 EXISTS(SELECT 1 FROM usage_entries u WHERE u.member_id=$1 AND NOT EXISTS
 (SELECT 1 FROM member_periods p WHERE p.member_id=u.member_id AND p.period_start=u.period_start AND p.period_end=u.period_end))`,
		m.ID, m.PeriodStart, m.PeriodEnd, m.PeriodTimezone, m.PeriodAnchorDay).Scan(&proven, &unattributable)
	if err != nil {
		return nil, dbErr(err)
	}
	if m.SubscriptionID != nil || m.PeriodStart != nil || m.PeriodEnd != nil {
		_, tzerr := time.LoadLocation(m.PeriodTimezone)
		if !proven || tzerr != nil || m.PeriodStart == nil || m.PeriodEnd == nil || !m.PeriodStart.Before(*m.PeriodEnd) || s.now().Before(*m.PeriodStart) || !s.now().Before(*m.PeriodEnd) {
			issues = append(issues, "cycle_unproven")
		}
	}
	if unattributable {
		issues = append(issues, "ledger_unattributable")
	}
	d := ev.Decision(s.now())
	// A missing cycle is not proof of zero usage or unlimited service. Preserve
	// every known number/date but do not advertise authorization as established.
	if len(issues) > 0 {
		d.Access = false
		d.ProxyAccess = "unknown"
		d.AllowedNodeIDs = []string{}
		d.Reasons = append(d.Reasons, issues...)
	}
	complete := len(issues) == 0
	m.DataComplete = &complete
	m.DataIssues = issues
	m.UsedBytes = ev.UsedBytes
	m.EffectiveQuotaBytes = d.EffectiveQuotaBytes
	m.AccessReasons = d.Reasons
	m.ProxyAccess = d.ProxyAccess
	m.AllowedNodeIDs = d.AllowedNodeIDs
	m.OverQuota = d.EffectiveQuotaBytes != nil && ev.UsedBytes >= *d.EffectiveQuotaBytes
	return &AccessResult{Decision: d, Evidence: ev}, nil
}

// ProductFormats runs the very same endpoint projection and encoders as /sub,
// in memory only. Never creates credentials or starts a network core.
func (s *Store) ProductFormats(ctx context.Context, res *AccessResult) ([]domain.FormatStatus, error) {
	creds, err := s.MemberCredentials(ctx, res.Evidence.Member.ID)
	if err != nil {
		return nil, err
	}
	secrets := map[string]string{}
	for _, c := range creds {
		secrets[c.NodeID] = c.Secret
	}
	nodes := []clientconfig.NodeConfig{}
	reason := ""
	for _, n := range s.AllowedNodes(ctx, res) {
		ep, issues := protocol.EndpointFor(n)
		if len(issues) > 0 {
			reason = "node_incomplete:" + issues[0].Field
			break
		}
		if secrets[n.ID] == "" {
			reason = "node_incomplete:credential"
			break
		}
		nodes = append(nodes, clientconfig.NodeConfig{Endpoint: ep, Credential: secrets[n.ID]})
	}
	if reason == "" && len(nodes) == 0 {
		reason = "nodes_empty"
	}
	var tmpl *domain.Template
	if sub := res.Evidence.Subscription; sub != nil && sub.TemplateID != nil {
		tmpl, err = s.GetTemplate(ctx, *sub.TemplateID)
		if err != nil {
			return nil, err
		}
	}
	out := make([]domain.FormatStatus, 0, len(clientconfig.Formats))
	for _, format := range clientconfig.Formats {
		why := reason
		if why == "" {
			var template domain.Object
			if tmpl != nil && tmpl.Format == format {
				template = tmpl.Content
			}
			_, err := clientconfig.Render(clientconfig.Request{Format: format, SubscriptionName: "proxypanel", Nodes: nodes, Template: template})
			if err != nil {
				var unsupported *clientconfig.UnsupportedError
				if !errors.As(err, &unsupported) {
					return nil, err
				}
				why = "protocol_unsupported:" + unsupported.Field
			} else {
				why = "ok"
			}
		}
		out = append(out, domain.FormatStatus{Format: format, Available: why == "ok", Reason: why})
	}
	return out, nil
}

func (s *Store) productSetup(ctx context.Context) (*domain.OverviewSetup, error) {
	// Object existence alone is insufficient for delivery: require a live agent,
	// enabled connected resources, a usable group/plan, and an opened member.
	configured, err := s.listAllNodes(ctx, "")
	if err != nil {
		return nil, err
	}
	validIDs := []string{}
	for _, n := range configured {
		if _, issues := protocol.EndpointFor(n); len(issues) == 0 {
			validIDs = append(validIDs, n.ID)
		}
	}
	var server, node, group, sub, member, traffic bool
	err = s.DB.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM servers s JOIN agents a ON a.server_id=s.id WHERE NOT s.archived AND NOT a.archived AND a.last_heartbeat_at > now()-interval '45 seconds'),
 EXISTS(SELECT 1 FROM nodes n JOIN servers s ON s.id=n.server_id WHERE NOT n.archived AND n.enabled AND NOT s.archived AND n.id=ANY($1)), 
 EXISTS(SELECT 1 FROM node_groups g JOIN node_group_members gm ON gm.group_id=g.id JOIN nodes n ON n.id=gm.node_id WHERE NOT n.archived AND n.enabled AND n.id=ANY($1)), 
 EXISTS(SELECT 1 FROM subscriptions s JOIN subscription_node_groups sg ON sg.subscription_id=s.id JOIN node_group_members gm ON gm.group_id=sg.node_group_id JOIN nodes n ON n.id=gm.node_id WHERE NOT s.archived AND s.access_enabled AND NOT n.archived AND n.enabled AND n.id=ANY($1)), 
 EXISTS(SELECT 1 FROM members m JOIN subscriptions s ON s.id=m.subscription_id WHERE NOT m.archived AND m.active AND m.proxy_enabled AND NOT s.archived AND s.access_enabled AND (m.expires_at IS NULL OR m.expires_at>now()) AND EXISTS(SELECT 1 FROM member_periods p WHERE p.member_id=m.id AND p.period_start=m.period_start AND p.period_end=m.period_end AND p.period_start<=now() AND p.period_end>now()) AND EXISTS(SELECT 1 FROM subscription_node_groups sg JOIN node_group_members gm ON gm.group_id=sg.node_group_id JOIN nodes n ON n.id=gm.node_id WHERE sg.subscription_id=s.id AND NOT n.archived AND n.enabled AND n.id=ANY($1))),
 EXISTS(SELECT 1 FROM usage_entries u JOIN member_periods p ON p.member_id=u.member_id AND p.period_start=u.period_start AND p.period_end=u.period_end WHERE u.up_bytes+u.down_bytes>0 AND NOT u.estimated)`, validIDs).Scan(&server, &node, &group, &sub, &member, &traffic)
	if err != nil {
		return nil, dbErr(err)
	}
	out := &domain.OverviewSetup{Steps: []domain.SetupStep{}}
	keys := []string{"servers", "nodes", "node_groups", "subscriptions", "members", "traffic_observed"}
	reasons := []string{"agent_heartbeat_missing", "enabled_node_missing", "populated_group_missing", "deliverable_subscription_missing", "opened_member_missing", "connection_evidence_missing"}
	for i, complete := range []bool{server, node, group, sub, member, traffic} {
		step := domain.SetupStep{Key: keys[i], State: "pending", Reason: reasons[i]}
		if i == 5 {
			step.State = "unknown"
		}
		if complete {
			step.State = "completed"
			step.Reason = "ok"
			out.CompletedCount++
		}
		if i == 5 && complete {
			step.Reason = "traffic_observed_not_all_clients_verified"
		}
		out.Steps = append(out.Steps, step)
	}
	return out, nil
}
