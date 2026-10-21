package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/access"
	"proxypanel/internal/panel/clientconfig"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/protocol"
	"proxypanel/internal/panel/secrets"
	"proxypanel/internal/panel/store"
)

const sessionCookieName = "pp_session"

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Ping(r.Context()); err != nil {
		s.writeError(w, r, domain.E(domain.CodeDBUnavailable, http.StatusServiceUnavailable, "postgres unavailable"))
		return
	}
	redisState := "disabled"
	if reporter, ok := s.RateLimiter.(interface {
		Status(context.Context) string
	}); ok {
		redisState = reporter.Status(r.Context())
	} else if s.Cfg.RedisDSN != "" {
		redisState = "unavailable"
	}
	state := "ok"
	switch redisState {
	case "unavailable":
		state = "degraded"
	case "disabled":
		// Rate limiting runs on the bounded local fallback; not a failure but
		// reported honestly.
		if s.Cfg.RedisDSN != "" {
			state = "degraded"
		}
	}
	s.writeData(w, http.StatusOK, map[string]any{
		"status": state, "postgres": "ok", "redis": redisState,
	})
}
func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/", HttpOnly: true,
		Secure: !s.Cfg.Dev, SameSite: http.SameSiteStrictMode, Expires: expires,
	})
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) loginPayload(m *domain.Member, csrf string) map[string]any {
	return map[string]any{"user": m, "csrf_token": csrf}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if s.RateLimiter != nil {
		ok, _ := s.RateLimiter.Allow(r.Context(), "login:"+clientIP(r)+":"+strings.ToLower(in.Username), 10, time.Minute)
		if !ok {
			s.writeError(w, r, domain.E(domain.CodeRateLimited, http.StatusTooManyRequests, "too many login attempts"))
			return
		}
	}
	uniform := domain.E(domain.CodeUnauthenticated, http.StatusUnauthorized, "invalid username or password")
	m, err := s.Store.GetMemberByUsername(r.Context(), in.Username)
	if err != nil {
		s.writeError(w, r, uniform)
		return
	}
	ok, verr := secrets.VerifyPassword(m.PasswordHash, in.Password)
	if verr != nil || !ok {
		s.writeError(w, r, uniform)
		return
	}
	if !m.Active || m.Archived {
		s.writeError(w, r, uniform)
		return
	}
	ttl := s.Cfg.SessionTTL
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	token := secrets.NewToken(32)
	csrf := secrets.NewToken(32)
	sess := &domain.Session{
		ID: newID(ids.Session), MemberID: m.ID, TokenHash: secrets.HashToken(token), CSRFToken: csrf,
		IP: clientIP(r), UserAgent: r.UserAgent(), ExpiresAt: time.Now().UTC().Add(ttl),
	}
	if err := s.Store.CreateSession(r.Context(), sess); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.Store.TouchLogin(r.Context(), m.ID)
	s.setSessionCookie(w, token, sess.ExpiresAt)
	s.audit(r, "auth.login", "member", m.ID, nil)
	s.writeData(w, http.StatusOK, s.loginPayload(m, csrf))
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	m := memberFrom(r.Context())
	res, err := s.Store.EvaluateAccessForMember(r.Context(), m)
	if err == nil {
		m = res.Evidence.Member
	}
	s.writeData(w, http.StatusOK, s.loginPayload(m, sess.CSRFToken))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	if err := s.Store.RevokeSession(r.Context(), sess.ID, sess.MemberID); err != nil {
		s.writeError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: !s.Cfg.Dev, SameSite: http.SameSiteStrictMode})
	s.audit(r, "auth.logout", "session", sess.ID, nil)
	s.writeData(w, http.StatusOK, map[string]any{"revoked": true})
}

func (s *Server) handleRedeem(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code     string `json:"code"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	hash, err := secrets.HashPassword(in.Password)
	if err != nil {
		s.writeError(w, r, domain.Validation("%s", err.Error()))
		return
	}
	if strings.TrimSpace(in.Username) == "" {
		s.writeError(w, r, domain.Validation("username is required"))
		return
	}
	member := &domain.Member{
		ID: newID(ids.Member), Username: strings.TrimSpace(in.Username), PasswordHash: hash,
		Role: "member", Active: true, ProxyEnabled: true,
	}
	created, err := s.Store.RedeemInvitation(r.Context(), secrets.HashToken(strings.TrimSpace(in.Code)), member)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusCreated, created)
}

// ─── member scope ──────────────────────────────────────────────────────────

func (s *Server) handleMeStatus(w http.ResponseWriter, r *http.Request) {
	res, err := s.Store.EvaluateAccess(r.Context(), memberFrom(r.Context()).ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, map[string]any{
		"member": res.Evidence.Member, "access": res.Decision,
		"nodes_allowed": len(res.Decision.AllowedNodeIDs),
	})
}

func (s *Server) handleMeNodes(w http.ResponseWriter, r *http.Request) {
	res, err := s.Store.EvaluateAccess(r.Context(), memberFrom(r.Context()).ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	nodes := s.Store.AllowedNodes(r.Context(), res)
	items := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		ep, _ := protocol.EndpointFor(n)
		items = append(items, map[string]any{
			"id": n.ID, "name": n.Name, "protocol": n.Protocol, "host": n.Host, "port": n.Port,
			"network": ep.Network, "security": ep.Security, "flow": ep.Flow,
		})
	}
	s.writeList(w, items, nil)
}

func (s *Server) handleMeUsage(w http.ResponseWriter, r *http.Request) {
	m := memberFrom(r.Context())
	rows, err := s.Store.ListUsage(r.Context(), store.UsageFilter{MemberID: m.ID, Limit: 200})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	adj, err := s.Store.ListQuotaAdjustments(r.Context(), m.ID, 100)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeList(w, map[string]any{"usage": rows, "adjustments": adj}, nil)
}

func (s *Server) currentToken(r *http.Request, memberID string) (string, string, error) {
	token, _, err := s.Store.ActiveSubscriptionToken(r.Context(), memberID)
	if err != nil {
		if de, ok := err.(*domain.Error); ok && de.Code == domain.CodeNotFound {
			plain := secrets.NewToken(32)
			tok, cerr := s.Store.EnsureSubscriptionToken(r.Context(), memberID, secrets.HashToken(plain), plain)
			return tok, s.subscriptionURL(tok), cerr
		}
		return "", "", err
	}
	return token, s.subscriptionURL(token), nil
}

func (s *Server) subscriptionURL(token string) string {
	base := strings.TrimRight(s.Cfg.PublicURL, "/")
	if base == "" {
		base = "http://" + s.Cfg.Listen
	}
	return base + "/sub/" + token
}

func (s *Server) handleMeSubscription(w http.ResponseWriter, r *http.Request) {
	m := memberFrom(r.Context())
	token, url, err := s.currentToken(r, m.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, map[string]any{"url": url, "token": token, "formats": clientconfig.Formats})
}

func (s *Server) handleMeTokenRotation(w http.ResponseWriter, r *http.Request) {
	m := memberFrom(r.Context())
	plain := secrets.NewToken(32)
	if _, err := s.Store.RotateSubscriptionToken(r.Context(), m.ID, secrets.HashToken(plain), plain); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "subscription_token.rotate", "member", m.ID, nil)
	s.writeData(w, http.StatusOK, map[string]any{"token": plain, "url": s.subscriptionURL(plain)})
}

func (s *Server) handleMeCredentialRotation(w http.ResponseWriter, r *http.Request) {
	m := memberFrom(r.Context())
	s.rotateCredentials(w, r, m.ID, m.Username)
}

func (s *Server) handleMePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	m := memberFrom(r.Context())
	ok, err := secrets.VerifyPassword(m.PasswordHash, in.CurrentPassword)
	if err != nil || !ok {
		s.writeError(w, r, domain.E(domain.CodeUnauthenticated, http.StatusUnauthorized, "current password is incorrect"))
		return
	}
	hash, err := secrets.HashPassword(in.NewPassword)
	if err != nil {
		s.writeError(w, r, domain.Validation("%s", err.Error()))
		return
	}
	if _, err := s.Store.UpdateMember(r.Context(), m.ID, 0, func(x *domain.Member) error {
		x.PasswordHash = hash
		return nil
	}); err != nil {
		s.writeError(w, r, err)
		return
	}
	sess := sessionFrom(r.Context())
	revoked, err := s.Store.RevokeAllSessions(r.Context(), m.ID, sess.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "password.change", "member", m.ID, nil)
	s.writeData(w, http.StatusOK, map[string]any{"updated": true, "sessions_revoked": revoked})
}

func (s *Server) handleMeSessions(w http.ResponseWriter, r *http.Request) {
	m := memberFrom(r.Context())
	sess := sessionFrom(r.Context())
	items, err := s.Store.ListSessions(r.Context(), m.ID, sess.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeList(w, items, nil)
}

func (s *Server) handleMeSessionDelete(w http.ResponseWriter, r *http.Request) {
	m := memberFrom(r.Context())
	if err := s.Store.RevokeSession(r.Context(), r.PathValue("id"), m.ID); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, map[string]any{"revoked": true})
}

// ─── public subscription output ────────────────────────────────────────────

func (s *Server) handleSubscriptionOutput(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.PathValue("token"))
	format, ok := clientconfig.NormalizeFormat(r.URL.Query().Get("format"))
	if !ok {
		s.writeError(w, r, domain.Validation("unsupported format"))
		return
	}
	m, _, err := s.Store.MemberBySubscriptionToken(r.Context(), secrets.HashToken(token))
	if err != nil {
		s.writeError(w, r, domain.E(domain.CodeForbidden, http.StatusForbidden, "subscription is not available"))
		return
	}
	res, err := s.Store.EvaluateAccessForMember(r.Context(), m)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !res.Decision.Access {
		s.writeError(w, r, domain.E(domain.CodeForbidden, http.StatusForbidden,
			"no proxy access: %s", strings.Join(res.Decision.Reasons, ",")))
		return
	}
	creds, err := s.Store.MemberCredentials(r.Context(), m.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	secretByNode := map[string]string{}
	for _, c := range creds {
		secretByNode[c.NodeID] = c.Secret
	}
	allowed := s.Store.AllowedNodes(r.Context(), res)
	nodes := make([]clientconfig.NodeConfig, 0, len(allowed))
	for _, n := range allowed {
		ep, issues := protocol.EndpointFor(n)
		if len(issues) > 0 {
			s.writeError(w, r, domain.E(domain.CodeProtocolNotSupported, http.StatusUnprocessableEntity,
				"node %q has incomplete client parameters: %s", n.Name, issues[0].String()).
				WithDetail("node", n.Name).WithDetail("field", issues[0].Field))
			return
		}
		nodes = append(nodes, clientconfig.NodeConfig{Endpoint: ep, Credential: secretByNode[n.ID]})
	}
	// Templates are format-specific presentation defaults. Load the current row
	// for every response; authorization and generated credentials still win.
	var template domain.Object
	if sub := res.Evidence.Subscription; sub != nil && sub.TemplateID != nil {
		tmpl, err := s.Store.GetTemplate(r.Context(), *sub.TemplateID)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if tmpl.Format == format {
			template = tmpl.Content
		}
	}
	out, err := clientconfig.Render(clientconfig.Request{
		Format: format, SubscriptionName: "proxypanel", Nodes: nodes, Template: template,
		Base64: r.URL.Query().Get("base64") == "1" || r.URL.Query().Get("base64") == "true",
	})
	if err != nil {
		var ue *clientconfig.UnsupportedError
		if errors.As(err, &ue) {
			s.writeError(w, r, domain.E(domain.CodeProtocolNotSupported, http.StatusUnprocessableEntity,
				"%s cannot represent %s", ue.Client, ue.Field).
				WithDetail("node", ue.NodeName).WithDetail("field", ue.Field).WithDetail("reason", ue.Reason))
			return
		}
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", out.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out.Body)
}

// ─── agent scope ───────────────────────────────────────────────────────────

func (s *Server) handleAgentRegister(w http.ResponseWriter, r *http.Request) {
	var in contracts.RegistrationRequest
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	token := secrets.NewToken(32)
	agent, _, err := s.Store.RegisterAgent(r.Context(), secrets.HashToken(strings.TrimSpace(in.Code)),
		in.Name, in.Version, secrets.HashToken(token), token[:8])
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusCreated, contracts.RegistrationResponse{
		AgentID: agent.ID, Token: token, ControlGeneration: itoa64(agent.ControlGeneration),
	})
}

func (s *Server) handleAgentSnapshot(w http.ResponseWriter, r *http.Request) {
	agent := agentFrom(r.Context())
	snap, err := s.Store.BuildSnapshot(r.Context(), agent)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if r.URL.Query().Get("revision") == itoa64(snap.Revision) &&
		r.URL.Query().Get("control_generation") == snap.ControlGeneration {
		w.Header().Set("X-Current-Revision", itoa64(snap.Revision))
		w.Header().Set("X-Control-Generation", snap.ControlGeneration)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.writeData(w, http.StatusOK, snap)
}

func (s *Server) handleAgentApplied(w http.ResponseWriter, r *http.Request) {
	var in contracts.ApplyReport
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.Store.RecordApplyReport(r.Context(), agentFrom(r.Context()).ID, in); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, map[string]any{"accepted": true})
}

func (s *Server) handleAgentUsage(w http.ResponseWriter, r *http.Request) {
	var in contracts.UsageBatch
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	status, err := s.Store.SubmitUsageBatch(r.Context(), agentFrom(r.Context()), in)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, contracts.UsageAck{BatchID: in.BatchID, Status: status})
}

func (s *Server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	var in contracts.Heartbeat
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.Store.RecordHeartbeat(r.Context(), agentFrom(r.Context()).ID, in); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, map[string]any{"accepted": true})
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

var _ = access.AlgorithmVersion
