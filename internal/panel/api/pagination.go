package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"proxypanel/internal/panel/domain"
)

// meCursor is authenticated with the existing master key. Scope includes the
// endpoint and authenticated member; these endpoints have no query filters.
// Adding a filter requires adding it to this scope, not just to the SQL.
type meCursor struct {
	Version int      `json:"v"`
	Scope   string   `json:"s"`
	Keys    []string `json:"k"`
}

func (s *Server) mePage(r *http.Request, endpoint, member string) (int, []string, error) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return 0, nil, domain.Validation("invalid limit").WithFieldErrors(map[string]string{"limit": "invalid"})
		}
		limit = n
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		return limit, nil, nil
	}
	invalid := func() (int, []string, error) {
		return 0, nil, domain.Validation("invalid list cursor").WithFieldErrors(map[string]string{"cursor": "invalid"})
	}
	if len(raw) > 4096 {
		return invalid()
	}
	ct, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return invalid()
	}
	plain, err := s.Store.Key.Decrypt(ct)
	if err != nil {
		return invalid()
	}
	var c meCursor
	if json.Unmarshal(plain, &c) != nil || c.Version != 1 || c.Scope != endpoint+":"+member {
		return invalid()
	}
	want := 2
	if endpoint == "nodes" || endpoint == "usage" {
		want = 3
	}
	if len(c.Keys) != want {
		return invalid()
	}
	for _, k := range c.Keys {
		if len(k) > 512 {
			return invalid()
		}
	}
	if endpoint == "nodes" {
		if _, err := strconv.Atoi(c.Keys[0]); err != nil {
			return invalid()
		}
	} else {
		if _, err := time.Parse(time.RFC3339Nano, c.Keys[0]); err != nil {
			return invalid()
		}
		if endpoint == "usage" {
			if _, err := time.Parse(time.RFC3339Nano, c.Keys[2]); err != nil {
				return invalid()
			}
		}
	}
	if c.Keys[1] == "" {
		return invalid()
	}
	return limit, c.Keys, nil
}

func (s *Server) meNext(endpoint, member string, keys []string) (*string, error) {
	plain, err := json.Marshal(meCursor{1, endpoint + ":" + member, keys})
	if err != nil {
		return nil, err
	}
	ct, err := s.Store.Key.Encrypt(plain)
	if err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(ct)
	return &token, nil
}

// Authoritative access evaluation has already loaded ALL nodes. Never page
// that input: pagination operates only on the resulting allowed-node set.
func meNodePage(nodes []domain.Node, limit int, after []string) ([]domain.Node, bool) {
	sort.Slice(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if a.SortOrder != b.SortOrder {
			return a.SortOrder < b.SortOrder
		}
		if strings.ToLower(a.Name) != strings.ToLower(b.Name) {
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		return a.ID < b.ID
	})
	start := 0
	if after != nil {
		order, _ := strconv.Atoi(after[0])
		start = sort.Search(len(nodes), func(i int) bool {
			n := nodes[i]
			name := strings.ToLower(n.Name)
			return n.SortOrder > order || n.SortOrder == order && (name > after[1] || name == after[1] && n.ID > after[2])
		})
	}
	end := start + limit
	more := end < len(nodes)
	if end > len(nodes) {
		end = len(nodes)
	}
	return nodes[start:end], more
}

func meNodeKeys(n domain.Node) []string {
	return []string{strconv.Itoa(n.SortOrder), strings.ToLower(n.Name), n.ID}
}
func meTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
