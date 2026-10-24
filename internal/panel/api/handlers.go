package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/access"
	"proxypanel/internal/panel/clientconfig"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/protocol"
	"proxypanel/internal/panel/secrets"
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

// cookieSameSite 返回会话 Cookie 的 SameSite 策略。
//
// 默认 Strict（与改造前一致，最严格）。前后端分离部署时后端需要返回 None
// 并配合 Secure，否则浏览器在跨站请求里根本不会带上 Cookie。
func (s *Server) cookieSameSite() http.SameSite {
	if s.Cfg.CookieSameSite != 0 {
		return s.Cfg.CookieSameSite
	}
	return http.SameSiteStrictMode
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/", HttpOnly: true,
		Secure:   !s.Cfg.Dev,
		SameSite: s.cookieSameSite(),
		Domain:   s.Cfg.CookieDomain,
		Expires:  expires,
	})
}

// clientIP 返回用于限流的客户端地址。
//
// 面板部署在 nginx 之后时，RemoteAddr 永远是反代（容器里是 127.0.0.1），
// 直接用它当限流键会让**全站共享一个桶**（10 次/分钟），任何人被锁都会
// 连带锁住其他人。实测 Redis 键一直是 `pp:rl:login:127.0.0.1:admin`。
//
// 但也不能无条件相信 X-Forwarded-For —— 客户端可以自带这个头来绕过限流。
// 折中做法与业界一致：**只有当直连方是可信代理（回环或私有网段）时才采信
// XFF**，且取最右一项（离我们最近的那跳由我们自己的 nginx 写入）。
// 我们的 nginx 用 `proxy_set_header X-Forwarded-For $remote_addr`（覆盖而非
// 追加），所以最右项就是真实客户端地址。
func clientIP(r *http.Request) string {
	remote := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		remote = host
	}
	if !isTrustedProxy(remote) {
		// 直连（或来自不可信来源）：忽略任何 XFF，防止伪造绕过限流。
		return remote
	}
	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff == "" {
		return remote
	}
	parts := strings.Split(xff, ",")
	candidate := strings.TrimSpace(parts[len(parts)-1])
	if ip := net.ParseIP(candidate); ip != nil {
		return candidate
	}
	return remote
}

// isTrustedProxy 判断直连方是否是我们自己的反向代理。
// 回环 + RFC1918 私有网段 + 链路本地都视为可信（容器/内网反代）。
func isTrustedProxy(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	return false
}

func (s *Server) loginPayload(m *domain.Member, csrf string) map[string]any {
	return map[string]any{"user": m, "csrf_token": csrf}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := s.validateLoginRequest(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeAuthJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	// 空字段是入参问题，应当在鉴权之前就以 400 拒绝（而不是落到统一的 401 分支）。
	// 注意顺序：先做字段非空校验，再做限流，避免用空用户名消耗配额。
	in.Username = strings.TrimSpace(in.Username)
	if in.Username == "" || in.Password == "" {
		s.writeError(w, r, domain.Validation("username and password are required").
			WithFieldErrors(map[string]string{"username": "required", "password": "required"}))
		return
	}
	if err := s.authBudget(r, "login", in.Username); err != nil {
		s.writeError(w, r, err)
		return
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
	if err := s.Store.CreateSessionForPassword(r.Context(), sess, m.PasswordHash); err != nil {
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
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure:   !s.Cfg.Dev,
		SameSite: s.cookieSameSite(),
		Domain:   s.Cfg.CookieDomain,
	})
	s.audit(r, "auth.logout", "session", sess.ID, nil)
	s.writeData(w, http.StatusOK, map[string]any{"revoked": true})
}

func (s *Server) handleRedeem(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code     string `json:"code"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeAuthJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	in.Code = strings.TrimSpace(in.Code)
	if in.Username == "" || in.Code == "" {
		s.writeError(w, r, domain.Validation("username and code are required"))
		return
	}
	if err := s.authBudget(r, "redeem", in.Username); err != nil {
		s.writeError(w, r, err)
		return
	}
	// Cheap checks precede bcrypt; the transaction rechecks state under locks.
	if err := s.Store.CheckInvitation(r.Context(), secrets.HashToken(in.Code)); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.redeemWorkBudget(r); err != nil {
		s.writeError(w, r, err)
		return
	}
	hash, err := secrets.HashPassword(in.Password)
	if err != nil {
		s.writeError(w, r, domain.Validation("%s", err.Error()))
		return
	}
	member := &domain.Member{
		ID: newID(ids.Member), Username: strings.TrimSpace(in.Username), PasswordHash: hash,
		Role: "member", Active: true, ProxyEnabled: true,
	}
	created, err := s.Store.RedeemInvitation(r.Context(), secrets.HashToken(strings.TrimSpace(in.Code)), member)
	if err != nil {
		// Public redemption never reveals the invitation's binding state.
		if domain.AsError(err).Code == domain.CodeSubscriptionNotBindable {
			err = domain.Validation("invitation is not usable")
		}
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusCreated, created)
}

// ─── member scope ──────────────────────────────────────────────────────────

func (s *Server) handleMeStatus(w http.ResponseWriter, r *http.Request) {
	res, err := s.Store.ProductStatus(r.Context(), memberFrom(r.Context()).ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, map[string]any{
		"member": res.Evidence.Member, "access": res.Decision,
		"nodes_allowed": len(res.Decision.AllowedNodeIDs),
		"data_complete": res.Evidence.Member.DataComplete, "data_issues": res.Evidence.Member.DataIssues,
	})
}

func (s *Server) handleMeNodes(w http.ResponseWriter, r *http.Request) {
	member := memberFrom(r.Context()).ID
	limit, after, err := s.mePage(r, "nodes", member)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	res, err := s.Store.EvaluateAccess(r.Context(), member)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	nodes, more := meNodePage(s.Store.AllowedNodes(r.Context(), res), limit, after)
	var next *string
	if more {
		next, err = s.meNext("nodes", member, meNodeKeys(nodes[len(nodes)-1]))
		if err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	items := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		ep, _ := protocol.EndpointFor(n)
		items = append(items, map[string]any{
			"id": n.ID, "name": n.Name, "protocol": n.Protocol, "host": n.Host, "port": n.Port,
			"network": ep.Network, "security": ep.Security, "flow": ep.Flow,
		})
	}
	s.writeList(w, items, next)
}

func (s *Server) handleMeUsage(w http.ResponseWriter, r *http.Request) {
	m := memberFrom(r.Context())
	limit, after, err := s.mePage(r, "usage", m.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	rows, err := s.Store.ListMeUsagePage(r.Context(), m.ID, limit, after)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next, err = s.meNext("usage", m.ID, []string{meTime(last.PeriodStart), last.NodeID, meTime(last.PeriodEnd)})
		if err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	adj := make([]domain.QuotaAdjustment, 0)
	if after == nil {
		adj, err = s.Store.ListMeAdjustments(r.Context(), m.ID)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	// These are paged detail aggregates; the whole-cycle summary remains in me/status.
	s.writeList(w, map[string]any{"usage": rows, "adjustments": adj}, next)
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
	res, err := s.Store.ProductStatus(r.Context(), m.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	formats, err := s.Store.ProductFormats(r.Context(), res)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	token, url, err := s.currentToken(r, m.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, map[string]any{"url": url, "token": token, "formats": clientconfig.Formats,
		"format_status": formats, "expires_at": res.Evidence.Member.ExpiresAt,
		"period_end": res.Evidence.Member.PeriodEnd, "rotation_impact": "old_link_invalid_new_required"})
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
	sess := sessionFrom(r.Context())
	revoked, err := s.Store.ChangePassword(r.Context(), m.ID, m.PasswordHash, hash, sess.ID)
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
	limit, after, err := s.mePage(r, "sessions", m.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, err := s.Store.ListMeSessionsPage(r.Context(), m.ID, sess.ID, limit, after)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		next, err = s.meNext("sessions", m.ID, []string{meTime(last.CreatedAt), last.ID})
		if err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	s.writeList(w, items, next)
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

// JSON plus an explicit origin check protects the unauthenticated login too:
// SameSite does not stop a top-level form from replacing a login cookie.
func (s *Server) validateLoginRequest(r *http.Request) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return domain.Validation("Content-Type must be application/json")
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			return domain.E(domain.CodeCSRFFailed, 403, "login origin is not allowed")
		}
		return nil // non-browser JSON clients and same-origin requests without Origin
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return domain.E(domain.CodeCSRFFailed, 403, "login origin is not allowed")
	}
	if parseAllowedOrigins(s.Cfg.AllowedOrigins).allows(origin) {
		return nil
	}
	// PublicURL is the canonical origin behind TLS termination. Never trust XFP.
	if base, err := url.Parse(s.Cfg.PublicURL); err == nil && base.Host != "" {
		if u.Scheme == base.Scheme && strings.EqualFold(u.Host, base.Host) {
			return nil
		}
	} else {
		scheme := "https"
		if s.Cfg.Dev && r.TLS == nil {
			scheme = "http"
		}
		if u.Scheme == scheme && strings.EqualFold(u.Host, r.Host) {
			return nil
		}
	}
	return domain.E(domain.CodeCSRFFailed, 403, "login origin is not allowed")
}

// Source admission runs BEFORE allocating attacker-chosen account buckets or
// invoking bcrypt. Hash identifiers to keep local/Redis keys bounded in bytes.
func (s *Server) authBudget(r *http.Request, kind, username string) error {
	if s.RateLimiter == nil {
		return nil
	}
	allow := func(key string, n int) bool {
		ok, err := s.RateLimiter.Allow(r.Context(), key, n, time.Minute)
		return err == nil && ok
	}
	source := secrets.HashToken(clientIP(r))
	n := 50
	if kind == "redeem" {
		n = 20
	}
	if !allow(kind+":source:"+source, n) {
		return domain.E(domain.CodeRateLimited, 429, "too many authentication attempts")
	}
	if kind == "login" {
		if !allow("login:"+source+":"+secrets.HashToken(strings.ToLower(username)), 10) {
			return domain.E(domain.CodeRateLimited, 429, "too many login attempts")
		}
	}
	return nil
}

// Only a usable invitation can consume the global expensive-work budget.
func (s *Server) redeemWorkBudget(r *http.Request) error {
	if s.RateLimiter == nil {
		return nil
	}
	ok, err := s.RateLimiter.Allow(r.Context(), "redeem:work", 100, time.Minute)
	if err != nil || !ok {
		return domain.E(domain.CodeRateLimited, 429, "too many redemption attempts")
	}
	return nil
}

// Read at most limit+1; do not drain arbitrary attacker-controlled bodies.
func decodeAuthJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return domain.Validation("request body is required")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return domain.Validation("invalid or oversized JSON body")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return domain.Validation("invalid JSON body")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return domain.Validation("one JSON document is required")
	}
	return nil
}
