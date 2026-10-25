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
		ExternalPort:   n.ExternalPort,
		StreamSettings: n.StreamSettings, ProtocolSettings: n.ProtocolSettings,
		ClientSettings: n.ClientSettings, ServerKey: n.ServerKey,
	}
}

// ensureRealityKeys generates a REALITY key pair when the administrator did not
// supply one; only the public key reaches member output.
func ensureRealityKeys(n *domain.Node) error {
	if !strings.EqualFold(n.Protocol, protocol.VLESS) {
		return nil
	}
	security, _ := n.StreamSettings.String("security")
	if !strings.EqualFold(security, "reality") {
		return nil
	}
	if n.ServerKey == "" {
		priv, pub, err := protocol.GenerateX25519()
		if err != nil {
			return err
		}
		n.ServerKey = priv
		if _, ok := clientPublicKeyOf(n); !ok {
			if err := n.ClientSettings.Set("public_key", pub); err != nil {
				return err
			}
		}
		return nil
	}
	// The admin supplied (or imported) a private key but no matching public key.
	// The pair is a pure function of the private key, so derive it instead of
	// forcing a second paste that can silently disagree.
	if _, ok := clientPublicKeyOf(n); !ok {
		pub, err := protocol.PublicKeyFor(n.ServerKey)
		if err != nil {
			return err
		}
		if err := n.ClientSettings.Set("public_key", pub); err != nil {
			return err
		}
	}
	return nil
}

// clientPublicKeyOf reads the client-side REALITY public key from
// client_settings, falling back to the legacy realitySettings.publicKey.
func clientPublicKeyOf(n *domain.Node) (string, bool) {
	if v, ok := protocol.ClientSetting(n.ClientSettings, "public_key", "publicKey", "password"); ok {
		return v, true
	}
	if rs, ok := n.StreamSettings.Sub("realitySettings"); ok {
		if v, ok := rs.String("publicKey"); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
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
	case "all_nodes", "":
		// 空 scope 等价于全部节点（向后兼容既有调用）。
		nodeIDs = res.Decision.AllowedNodeIDs
	default:
		// 未知 scope 必须拒绝：以前落进 default 会静默按"全部节点"处理，
		// 运维误传一个拼错的 scope 就会轮换全部凭据、立即打断所有在线连接。
		s.writeError(w, r, domain.Validation("scope must be \"all_nodes\" or \"single_node\" (got %q)", in.Scope).
			WithFieldErrors(map[string]string{"scope": "must be all_nodes or single_node"}))
		return
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

// requireVersion 强制 PATCH/PUT 携带 version。
//
// store 层用 `expectedVersion != 0` 表示"跳过乐观并发检查"，这个内部约定
// 泄漏到了 API 面：客户端只要省略 version（或传 0），并发两次 PATCH 就都能
// 成功、后写覆盖先写。实测两个并发请求返回 [200,200]，notes 被静默覆盖。
//
// 所以对外接口必须要求 version > 0；需要"跳过"的只有服务端内部调用
// （DELETE 归档、批量 apply），它们不经过这里。
func requireVersion(version int64) error {
	if version <= 0 {
		return domain.Validation("version is required for this request (send the current version to avoid overwriting concurrent changes)").
			WithFieldErrors(map[string]string{"version": "required"})
	}
	return nil
}
