package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/period"
	"proxypanel/internal/panel/protocol"
	"proxypanel/internal/panel/store"
)

func jsonUnmarshal(b []byte, dst any) error { return json.Unmarshal(b, dst) }

func newID(prefix string) string { return ids.New(prefix) }

func fieldErrors(issues []protocol.Issue) *domain.Error {
	fields := map[string]string{}
	for _, i := range issues {
		fields[i.Field] = i.Message
	}
	return domain.Validation("node configuration is invalid").WithFieldErrors(fields)
}

func protocolNode(n *domain.Node) protocol.Node {
	return protocol.Node{
		Protocol: n.Protocol, Flow: n.Flow, Cipher: n.Cipher, Listen: n.Listen, Host: n.Host, Port: n.Port,
		StreamSettings: n.StreamSettings, ProtocolSettings: n.ProtocolSettings,
		ClientSettings: n.ClientSettings, ServerKey: n.ServerKey,
	}
}

// ensureRealityKeys generates a REALITY key pair when the administrator did not
// supply one; only the public key reaches member output.
func ensureRealityKeys(n *domain.Node) error {
	if !strings.EqualFold(n.Protocol, protocol.VLESS) || n.ServerKey != "" {
		return nil
	}
	security, _ := n.StreamSettings.String("security")
	if !strings.EqualFold(security, "reality") {
		return nil
	}
	priv, pub, err := protocol.GenerateX25519()
	if err != nil {
		return err
	}
	n.ServerKey = priv
	if _, ok := n.ClientSettings.String("public_key"); !ok {
		if err := n.ClientSettings.Set("public_key", pub); err != nil {
			return err
		}
	}
	return nil
}

// prepareBinding validates bindability and computes the member cycle.
func (s *Server) prepareBinding(r *http.Request, m *domain.Member) error {
	if m.SubscriptionID == nil {
		return nil
	}
	sub, err := s.Store.GetSubscription(r.Context(), *m.SubscriptionID)
	if err != nil {
		return err
	}
	if sub.Archived || !sub.AllowNewBindings {
		return domain.Conflict(domain.CodeSubscriptionNotBindable, "subscription does not accept new bindings")
	}
	loc, err := period.LoadTimezone(sub.Timezone)
	if err != nil {
		return domain.Validation("%s", err.Error())
	}
	win := period.Containing(sub.AnchorDay, loc, time.Now().UTC())
	m.PeriodStart, m.PeriodEnd = &win.Start, &win.End
	m.PeriodTimezone = sub.Timezone
	m.PeriodAnchorDay = sub.AnchorDay
	now := time.Now().UTC()
	m.BoundAt = &now
	if m.ExpiresAt == nil && sub.DefaultValidDays != nil {
		exp := now.AddDate(0, 0, *sub.DefaultValidDays)
		m.ExpiresAt = &exp
	}
	return nil
}

func (s *Server) rotateCredentials(w http.ResponseWriter, r *http.Request, memberID, actor string) {
	var in struct {
		Scope  string `json:"scope"`
		NodeID string `json:"node_id"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	res, err := s.Store.EvaluateAccess(r.Context(), memberID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var nodeIDs []string
	switch in.Scope {
	case "single_node":
		if in.NodeID == "" {
			s.writeError(w, r, domain.Validation("node_id is required for single_node scope"))
			return
		}
		nodeIDs = []string{in.NodeID}
	default:
		nodeIDs = res.Decision.AllowedNodeIDs
	}
	byID := map[string]domain.Node{}
	for _, n := range res.Evidence.Nodes {
		byID[n.ID] = n
	}
	count, err := s.Store.RotateCredentials(r.Context(), memberID, nodeIDs, func(nodeID string) (string, string, string, error) {
		id, secret, kind := store.NewCredentialSecret(byID[nodeID])
		return id, secret, kind, nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "credentials.rotate", "member", memberID, nil)
	s.writeData(w, http.StatusOK, map[string]any{"rotated": count, "scope": in.Scope})
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// syncCredentials keeps member credentials aligned with the current access
// decision (called after member create/edit and explicit rotations).
func (s *Server) syncCredentials(r *http.Request, memberID string) error {
	res, err := s.Store.EvaluateAccess(r.Context(), memberID)
	if err != nil {
		return err
	}
	return s.Store.SyncCredentials(r.Context(), memberID, res.Evidence.Nodes, res.Decision.AllowedNodeIDs)
}
