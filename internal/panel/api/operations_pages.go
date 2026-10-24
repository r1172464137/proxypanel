package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/store"
)

// Scope binds the authenticated administrator, endpoint and effective filters.
// Limit is deliberately not bound: callers may change page size mid traversal.
func (s *Server) operationsPage(r *http.Request, endpoint string) (int, []string, store.OperationsFilter, string, error) {
	f := store.OperationsFilter{}
	q := r.URL.Query()
	switch endpoint {
	case "events":
		f.State = q.Get("state")
		f.Q = q.Get("q")
	case "tasks":
		f.State = q.Get("state")
		f.BatchID = q.Get("batch_id")
		f.AgentID = q.Get("agent_id")
	case "audit-logs":
		f.Action = q.Get("action")
		f.Actor = q.Get("actor")
	}
	scopeBytes, _ := json.Marshal(struct {
		Endpoint, Admin string
		Filters         store.OperationsFilter
	}{endpoint, memberFrom(r.Context()).ID, f})
	scope := string(scopeBytes)
	fail := func(field, reason string) (int, []string, store.OperationsFilter, string, error) {
		return 0, nil, f, scope, domain.Validation("invalid list query").WithFieldErrors(map[string]string{field: reason})
	}
	if !utf8.ValidString(f.Q) || utf8.RuneCountInString(f.Q) > 64 {
		return fail("q", "too_long")
	}
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return fail("limit", "invalid")
		}
		limit = n
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	raw := q.Get("cursor")
	if raw == "" {
		return limit, nil, f, scope, nil
	}
	if len(raw) > 4096 {
		return fail("cursor", "invalid")
	}
	ct, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return fail("cursor", "invalid")
	}
	plain, err := s.Store.Key.Decrypt(ct)
	if err != nil {
		return fail("cursor", "invalid")
	}
	var c meCursor
	if json.Unmarshal(plain, &c) != nil || c.Version != 1 || len(c.Keys) != 2 || c.Keys[1] == "" || len(c.Keys[1]) > 512 {
		return fail("cursor", "invalid")
	}
	if c.Scope != "operations:"+scope {
		return fail("cursor", "expired_scope")
	}
	if endpoint != "agents" && endpoint != "core-instances" {
		if _, err := time.Parse(time.RFC3339Nano, c.Keys[0]); err != nil {
			return fail("cursor", "invalid")
		}
	}
	return limit, c.Keys, f, scope, nil
}

func operationsList[T any](s *Server, w http.ResponseWriter, r *http.Request, endpoint string, fetch func(store.OperationsFilter, int, []string) ([]T, error), keys func(T) ([]string, error)) {
	limit, after, f, scope, err := s.operationsPage(r, endpoint)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, err := fetch(f, limit, after)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		k, e := keys(items[len(items)-1])
		if e != nil {
			s.writeError(w, r, e)
			return
		}
		next, err = s.meNext("operations", scope, k)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	s.writeList(w, items, next)
}

func (s *Server) operationsAgents(w http.ResponseWriter, r *http.Request) {
	operationsList(s, w, r, "agents", func(_ store.OperationsFilter, n int, a []string) ([]domain.Agent, error) {
		return s.Store.ListAgentsPage(r.Context(), n, a)
	}, func(v domain.Agent) ([]string, error) { return s.operationsNameKeys(r, v.Name, v.ID) })
}
func (s *Server) operationsCoreInstances(w http.ResponseWriter, r *http.Request) {
	operationsList(s, w, r, "core-instances", func(_ store.OperationsFilter, n int, a []string) ([]domain.CoreInstance, error) {
		return s.Store.ListCoreInstancesPage(r.Context(), n, a)
	}, func(v domain.CoreInstance) ([]string, error) { return s.operationsNameKeys(r, v.NodeName, v.ID) })
}

// Use PostgreSQL's lower(), not Go's Unicode casing, for exact SQL cursor keys.
func (s *Server) operationsNameKeys(r *http.Request, name, id string) ([]string, error) {
	key, err := s.Store.OperationsNameKey(r.Context(), name)
	return []string{key, id}, err
}
func (s *Server) operationsInvitations(w http.ResponseWriter, r *http.Request) {
	operationsList(s, w, r, "invitations", func(_ store.OperationsFilter, n int, a []string) ([]domain.Invitation, error) {
		return s.Store.ListInvitationsPage(r.Context(), n, a)
	}, func(v domain.Invitation) ([]string, error) { return []string{meTime(v.CreatedAt), v.ID}, nil })
}
func (s *Server) operationsBatches(w http.ResponseWriter, r *http.Request) {
	operationsList(s, w, r, "batches", func(_ store.OperationsFilter, n int, a []string) ([]domain.Batch, error) {
		return s.Store.ListBatchesPage(r.Context(), n, a)
	}, func(v domain.Batch) ([]string, error) { return []string{meTime(v.CreatedAt), v.ID}, nil })
}

// These three handlers require the route replacements in the handoff fragment;
// admin2.go's non-invitation functions are outside this change's ownership.
func (s *Server) handleOperationsEvents(w http.ResponseWriter, r *http.Request) {
	operationsList(s, w, r, "events", func(f store.OperationsFilter, n int, a []string) ([]domain.Event, error) {
		return s.Store.ListEventsPage(r.Context(), f, n, a)
	}, func(v domain.Event) ([]string, error) { return []string{meTime(v.CreatedAt), v.ID}, nil })
}
func (s *Server) handleOperationsTasks(w http.ResponseWriter, r *http.Request) {
	operationsList(s, w, r, "tasks", func(f store.OperationsFilter, n int, a []string) ([]domain.Task, error) {
		return s.Store.ListTasksPage(r.Context(), f, n, a)
	}, func(v domain.Task) ([]string, error) { return []string{meTime(v.CreatedAt), v.ID}, nil })
}
func (s *Server) handleOperationsAuditLogs(w http.ResponseWriter, r *http.Request) {
	operationsList(s, w, r, "audit-logs", func(f store.OperationsFilter, n int, a []string) ([]domain.AuditLog, error) {
		return s.Store.ListAuditPage(r.Context(), f, n, a)
	}, func(v domain.AuditLog) ([]string, error) { return []string{meTime(v.CreatedAt), v.ID}, nil })
}
