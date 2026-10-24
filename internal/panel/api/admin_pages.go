package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/store"
)

type adminCursor struct {
	Version int      `json:"v"`
	Scope   string   `json:"s"`
	Filters string   `json:"f"`
	Keys    []string `json:"k"`
}

func adminFilters(p store.AdminPageOptions) string {
	b, _ := json.Marshal([]any{p.Q, p.ServerID, p.SubscriptionID, p.IncludeArchived})
	return string(b)
}

func (s *Server) adminPage(r *http.Request, endpoint string) (store.AdminPageOptions, error) {
	p := store.AdminPageOptions{Limit: 50}
	query := r.URL.Query()
	invalid := func(field string) (store.AdminPageOptions, error) {
		if field == "cursor" {
			return p, cursorInvalid("invalid")
		}
		return p, domain.Validation("invalid list %s", field).WithFieldErrors(map[string]string{field: "invalid"})
	}
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return invalid("limit")
		}
		p.Limit = n
	}
	if p.Limit < 1 {
		p.Limit = 1
	}
	if p.Limit > 200 {
		p.Limit = 200
	}
	// Never silently reuse a cursor after changing a filter (even an unknown
	// filter): callers must request a new first page. Reject unsupported filters.
	for key, values := range query {
		if len(values) != 1 {
			return invalid(key)
		}
		if key == "cursor" || key == "limit" {
			continue
		}
		if key == "q" && (endpoint == "nodes" || endpoint == "members") {
			p.Q = strings.TrimSpace(values[0])
			continue
		}
		if key == "server_id" && endpoint == "nodes" {
			p.ServerID = values[0]
			continue
		}
		if key == "subscription_id" && endpoint == "members" {
			p.SubscriptionID = values[0]
			continue
		}
		if key == "include_archived" && endpoint == "members" {
			p.IncludeArchived = strings.EqualFold(strings.TrimSpace(values[0]), "true")
			continue
		}
		return invalid(key)
	}
	if utf8.RuneCountInString(p.Q) > 64 {
		return p, domain.Validation("query too long").WithFieldErrors(map[string]string{"q": "too_long"})
	}
	raw := query.Get("cursor")
	if raw == "" {
		return p, nil
	}
	if len(raw) > 16384 {
		return invalid("cursor")
	}
	ct, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return invalid("cursor")
	}
	plain, err := s.Store.Key.Decrypt(ct)
	if err != nil {
		return invalid("cursor")
	}
	var c adminCursor
	if json.Unmarshal(plain, &c) != nil || c.Version != 1 || c.Scope != adminPageScope(r, endpoint) || c.Filters != adminFilters(p) {
		return invalid("cursor")
	}
	want := 2
	if endpoint == "nodes" {
		want = 3
	}
	if len(c.Keys) != want || c.Keys[want-1] == "" {
		return invalid("cursor")
	}
	if endpoint == "nodes" {
		if _, err := strconv.ParseInt(c.Keys[0], 10, 32); err != nil {
			return invalid("cursor")
		}
	}
	p.After = c.Keys
	return p, nil
}

func writeAdminPage[T any](s *Server, w http.ResponseWriter, r *http.Request, endpoint string, p store.AdminPageOptions, page store.AdminPage[T]) {
	var next *string
	if page.Next != nil {
		plain, err := json.Marshal(adminCursor{1, adminPageScope(r, endpoint), adminFilters(p), page.Next})
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		ct, err := s.Store.Key.Encrypt(plain)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		token := base64.RawURLEncoding.EncodeToString(ct)
		next = &token
	}
	s.writeList(w, page.Items, next)
}
