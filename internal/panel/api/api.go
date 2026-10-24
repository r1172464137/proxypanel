// Package api exposes the frozen HTTP contract with the standard library
// ServeMux. Every handler reads its authorization from the store; the panel has
// no client side security boundary.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/secrets"
	"proxypanel/internal/panel/store"
)

// Config holds the process configuration derived from the environment.
type Config struct {
	Dev        bool
	Listen     string
	SessionTTL time.Duration
	PublicURL  string
	RedisDSN   string
	AdminUser  string
	AdminPass  string

	// AllowedOrigins 是前后端分离部署时的 CORS 白名单（逗号分隔）。
	// 为空表示同源部署，完全不启用 CORS。
	AllowedOrigins string
	// CookieSameSite 控制会话 Cookie 的 SameSite 策略：lax（默认，同站）
	// 或 none（跨站部署必须）。none 要求 Secure。
	CookieSameSite http.SameSite
	// WebRoot 是前端构建产物的目录。为空表示面板只提供 API，
	// 静态文件交给外部 nginx；非空则面板同时托管 SPA（见 static.go）。
	WebRoot string
	// CookieDomain 允许同主域的子域共享会话（如 .example.com）。默认空。
	CookieDomain string
}

// Server is the HTTP application.
type Server struct {
	Store *store.Store
	Cfg   Config
	// RateLimiter guards login attempts; nil disables limiting.
	RateLimiter RateLimiter
}

// RateLimiter is a small abstraction so Redis failures degrade locally instead
// of disabling protection.
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

type ctxKey string

const (
	ctxSession ctxKey = "session"
	ctxMember  ctxKey = "member"
	ctxAgent   ctxKey = "agent"
	ctxReqID   ctxKey = "request_id"
)

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("GET /api/v1/auth/session", s.requireUser(s.handleSession))
	mux.HandleFunc("POST /api/v1/auth/logout", s.requireUser(s.requireCSRF(s.handleLogout)))
	mux.HandleFunc("POST /api/v1/auth/redeem", s.handleRedeem)

	mux.HandleFunc("GET /sub/{token}", s.handleSubscriptionOutput)

	// member scope
	mux.HandleFunc("GET /api/v1/me/status", s.requireUser(s.handleMeStatus))
	mux.HandleFunc("GET /api/v1/me/nodes", s.requireUser(s.handleMeNodes))
	mux.HandleFunc("GET /api/v1/me/usage", s.requireUser(s.handleMeUsage))
	mux.HandleFunc("GET /api/v1/me/subscription", s.requireUser(s.handleMeSubscription))
	// 成员自助的写接口同样受恢复协调模式约束。
	//
	// 之前 requireWritable 只挂在管理员写路由上，恢复期间成员仍能轮换订阅
	// token / 轮换代理凭据 / 删除会话 —— 这些都会改授权相关状态并写库，
	// 与"恢复期间暂停普通变更、核对后再推送权限"的语义冲突。
	// （只保留修改自己的密码放行：那是自救路径，不改授权。）
	mux.HandleFunc("POST /api/v1/me/subscription/token/rotations", s.requireUser(s.requireCSRF(s.requireWritable(s.handleMeTokenRotation))))
	mux.HandleFunc("POST /api/v1/me/credentials/rotations", s.requireUser(s.requireCSRF(s.requireWritable(s.handleMeCredentialRotation))))
	mux.HandleFunc("POST /api/v1/me/password", s.requireUser(s.requireCSRF(s.handleMePassword)))
	mux.HandleFunc("GET /api/v1/me/sessions", s.requireUser(s.handleMeSessions))
	mux.HandleFunc("DELETE /api/v1/me/sessions/{id}", s.requireUser(s.requireCSRF(s.handleMeSessionDelete)))

	// agent scope
	mux.HandleFunc("POST /api/v1/agent/register", s.handleAgentRegister)
	mux.HandleFunc("GET /api/v1/agent/snapshot", s.requireAgent(s.handleAgentSnapshot))
	mux.HandleFunc("POST /api/v1/agent/applied", s.requireAgent(s.handleAgentApplied))
	mux.HandleFunc("POST /api/v1/agent/usage", s.requireAgent(s.handleAgentUsage))
	mux.HandleFunc("POST /api/v1/agent/heartbeat", s.requireAgent(s.handleAgentHeartbeat))

	// admin scope
	// Exiting recovery coordination is the one write allowed while recovery is
	// active: it is gated by an explicit verification payload instead.
	mux.HandleFunc("POST /api/v1/admin/recovery-mode/enter",
		s.requireUser(s.requireAdmin(s.requireCSRF(s.handleEnterRecovery))))
	mux.HandleFunc("POST /api/v1/admin/recovery-mode/exit",
		s.requireUser(s.requireAdmin(s.requireCSRF(s.handleExitRecovery))))
	s.mountAdmin(mux)

	// SPA 兜底。ServeMux 的模式优先级保证具体路由（/api/v1/...、/sub/{token}、
	// /healthz、/readyz）永远先于 "/" 匹配，所以它只接住剩下的路径。
	//
	// 这里不让 Handler() 返回错误，是因为配置错误应该在启动时直接失败：
	// 调用方（cmd/panel）在构造 Server 之前就校验 WebRoot，见 static.go。
	if s.Cfg.WebRoot != "" {
		static, err := newStaticHandler(s.Cfg.WebRoot)
		if err != nil {
			// 走到这里说明调用方没先校验。退化成"不托管静态文件"并留日志线索，
			// 比 panic 好 —— 但正常情况下启动阶段就该被拦下。
			log.Printf("web root %q unusable, SPA serving disabled: %v", s.Cfg.WebRoot, err)
		} else {
			mux.Handle("GET /", static)
		}
	}

	// CORS 在最外层：预检请求必须在鉴权之前应答，否则跨域写请求永远过不了。
	return parseAllowedOrigins(s.Cfg.AllowedOrigins).middleware(s.withRequestID(mux))
}

func (s *Server) mountAdmin(mux *http.ServeMux) {
	admin := func(pattern string, h http.HandlerFunc, write bool) {
		if write {
			mux.HandleFunc(pattern, s.requireUser(s.requireAdmin(s.requireWritable(s.requireCSRF(h)))))
			return
		}
		mux.HandleFunc(pattern, s.requireUser(s.requireAdmin(h)))
	}
	admin("GET /api/v1/admin/upgrade-readiness", s.handleGetUpgradeReadiness, false)
	admin("GET /api/v1/admin/recovery-mode", s.handleGetRecovery, false)
	admin("GET /api/v1/admin/overview", s.handleOverview, false)
	admin("GET /api/v1/admin/agents", s.handleAdminAgents, false)
	admin("GET /api/v1/admin/core-instances", s.handleAdminCoreInstances, false)
	admin("GET /api/v1/admin/servers", s.handleListServers, false)
	admin("POST /api/v1/admin/servers", s.handleCreateServer, true)
	admin("GET /api/v1/admin/servers/{id}", s.handleGetServer, false)
	admin("PATCH /api/v1/admin/servers/{id}", s.handlePatchServer, true)
	admin("DELETE /api/v1/admin/servers/{id}", s.handleDeleteServer, true)
	admin("POST /api/v1/admin/servers/{id}/registration-codes", s.handleRegistrationCode, true)

	admin("GET /api/v1/admin/nodes", s.handleListNodes, false)
	admin("POST /api/v1/admin/nodes", s.handleCreateNode, true)
	admin("GET /api/v1/admin/nodes/{id}", s.handleGetNode, false)
	admin("PATCH /api/v1/admin/nodes/{id}", s.handlePatchNode, true)
	admin("DELETE /api/v1/admin/nodes/{id}", s.handleDeleteNode, true)

	admin("GET /api/v1/admin/node-groups", s.handleListNodeGroups, false)
	admin("POST /api/v1/admin/node-groups", s.handleCreateNodeGroup, true)
	admin("GET /api/v1/admin/node-groups/{id}", s.handleGetNodeGroup, false)
	admin("PATCH /api/v1/admin/node-groups/{id}", s.handlePatchNodeGroup, true)
	admin("DELETE /api/v1/admin/node-groups/{id}", s.handleDeleteNodeGroup, true)

	admin("GET /api/v1/admin/subscriptions", s.handleListSubscriptions, false)
	admin("POST /api/v1/admin/subscriptions", s.handleCreateSubscription, true)
	admin("GET /api/v1/admin/subscriptions/{id}", s.handleGetSubscription, false)
	admin("PATCH /api/v1/admin/subscriptions/{id}", s.handlePatchSubscription, true)
	admin("DELETE /api/v1/admin/subscriptions/{id}", s.handleDeleteSubscription, true)

	admin("GET /api/v1/admin/members", s.handleListMembers, false)
	admin("POST /api/v1/admin/members", s.handleCreateMember, true)
	admin("GET /api/v1/admin/members/{id}", s.handleGetMember, false)
	admin("PATCH /api/v1/admin/members/{id}", s.handlePatchMember, true)
	admin("DELETE /api/v1/admin/members/{id}", s.handleDeleteMember, true)
	admin("POST /api/v1/admin/members/{id}/subscription-token/rotations", s.handleAdminTokenRotation, true)
	admin("POST /api/v1/admin/members/{id}/credentials/rotations", s.handleAdminCredentialRotation, true)
	admin("POST /api/v1/admin/members/{id}/quota-adjustments", s.handleQuotaAdjustment, true)

	admin("GET /api/v1/admin/templates", s.handleListTemplates, false)
	admin("POST /api/v1/admin/templates", s.handleCreateTemplate, true)
	admin("GET /api/v1/admin/templates/{id}", s.handleGetTemplate, false)
	admin("PATCH /api/v1/admin/templates/{id}", s.handlePatchTemplate, true)
	admin("DELETE /api/v1/admin/templates/{id}", s.handleDeleteTemplate, true)

	admin("GET /api/v1/admin/invitations", s.handleListInvitations, false)
	admin("POST /api/v1/admin/invitations", s.handleCreateInvitation, true)
	admin("POST /api/v1/admin/invitations/{id}/revoke", s.handleRevokeInvitation, true)

	admin("GET /api/v1/admin/usage", s.handleAdminUsage, false)
	admin("POST /api/v1/admin/batches/preview", s.handleBatchPreview, true)
	admin("POST /api/v1/admin/batches", s.handleBatchSubmit, true)
	admin("GET /api/v1/admin/batches", s.handleListBatches, false)
	admin("GET /api/v1/admin/batches/{id}", s.handleGetBatch, false)
	admin("GET /api/v1/admin/events", s.handleAdminEvents, false)
	admin("POST /api/v1/admin/events/{id}/ack", s.handleAckEvent, true)
	admin("GET /api/v1/admin/tasks", s.handleAdminTasks, false)
	admin("POST /api/v1/admin/tasks/{id}/retry", s.handleRetryTask, true)
	admin("GET /api/v1/admin/audit-logs", s.handleAuditLogs, false)
	admin("GET /api/v1/admin/settings", s.handleGetSettings, false)
	admin("PUT /api/v1/admin/settings", s.handlePutSettings, true)
}

// ─── middleware ────────────────────────────────────────────────────────────

func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := ids.New("req")
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxReqID, id)))
	})
}

func requestID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxReqID).(string); ok {
		return v
	}
	return ""
}

func (s *Server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			s.writeError(w, r, domain.E(domain.CodeUnauthenticated, http.StatusUnauthorized, "not authenticated"))
			return
		}
		sess, member, err := s.Store.SessionByTokenHash(r.Context(), secrets.HashToken(cookie.Value))
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if !member.Active || member.Archived {
			s.writeError(w, r, domain.E(domain.CodeUnauthenticated, http.StatusUnauthorized, "account is not active"))
			return
		}
		ctx := context.WithValue(r.Context(), ctxSession, sess)
		ctx = context.WithValue(ctx, ctxMember, member)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			next(w, r)
			return
		}
		sess := sessionFrom(r.Context())
		token := r.Header.Get("X-CSRF-Token")
		if sess == nil || token == "" || !secrets.ConstantTimeEqual(sess.CSRFToken, token) {
			s.writeError(w, r, domain.E(domain.CodeCSRFFailed, http.StatusForbidden, "CSRF token missing or invalid"))
			return
		}
		next(w, r)
	}
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := memberFrom(r.Context())
		if m == nil || m.Role != "admin" {
			s.writeError(w, r, domain.E(domain.CodeForbidden, http.StatusForbidden, "administrator role required"))
			return
		}
		next(w, r)
	}
}

func (s *Server) requireAgent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			s.writeError(w, r, domain.E(domain.CodeAgentAuthFailed, http.StatusUnauthorized, "agent bearer token required"))
			return
		}
		agent, err := s.Store.AgentByTokenHash(r.Context(), secrets.HashToken(strings.TrimPrefix(header, "Bearer ")))
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxAgent, agent)))
	}
}

func sessionFrom(ctx context.Context) *domain.Session {
	v, _ := ctx.Value(ctxSession).(*domain.Session)
	return v
}

func memberFrom(ctx context.Context) *domain.Member {
	v, _ := ctx.Value(ctxMember).(*domain.Member)
	return v
}

func agentFrom(ctx context.Context) *domain.Agent {
	v, _ := ctx.Value(ctxAgent).(*domain.Agent)
	return v
}

// ─── response helpers ──────────────────────────────────────────────────────

func (s *Server) writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "request_id": ""})
}

func (s *Server) writeList(w http.ResponseWriter, items any, cursor *string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "next_cursor": cursor})
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	de := domain.AsError(err)
	status := de.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	if de.Code == domain.CodeInternal {
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	body := map[string]any{
		"code":       de.Code,
		"message":    de.Message,
		"retryable":  de.Retryable,
		"request_id": requestID(r.Context()),
	}
	if len(de.Details) > 0 {
		body["details"] = de.Details
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": body})
}

// decodeJSON reads a bounded JSON body.
func decodeJSON(r *http.Request, dst any) error {
	defer io.Copy(io.Discard, r.Body)
	if r.Body == nil {
		return domain.Validation("request body is required")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return domain.Validation("request body is required")
		}
		return domain.Validation("invalid JSON body: %v", err)
	}
	return nil
}

func (s *Server) audit(r *http.Request, action, targetType, targetID string, detail domain.Object) {
	m := memberFrom(r.Context())
	actorID, actorName := "", ""
	if m != nil {
		actorID, actorName = m.ID, m.Username
	}
	ip := ""
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		ip = host
	}
	_ = s.Store.RecordAudit(r.Context(), actorID, actorName, action, targetType, targetID, detail, ip, requestID(r.Context()))
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	s.writeData(w, http.StatusOK, map[string]any{"status": "ok"})
}

// writeJSONBody encodes v and reports the write error.
func writeJSONBody(w http.ResponseWriter, v any) error {
	return json.NewEncoder(w).Encode(v)
}

// requireWritable blocks ordinary changes while recovery coordination is active
// (a restored backup must be verified before permissions are pushed again).
func (s *Server) requireWritable(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			next(w, r)
			return
		}
		state, err := s.Store.GetControlState(r.Context())
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if state.RecoveryMode {
			s.writeError(w, r, domain.E(domain.CodeRecoveryMode, http.StatusConflict,
				"panel is in recovery coordination mode; ordinary changes are paused"))
			return
		}
		next(w, r)
	}
}
