package store

import (
	"context"
	"strconv"

	"proxypanel/internal/panel/domain"
)

// AdminPageOptions is only for public admin reads. Authoritative internal
// ListAll*/ListResources callers must not use this bounded read model.
type AdminPageOptions struct {
	Limit                       int
	After                       []string
	Q, ServerID, SubscriptionID string
	IncludeArchived             bool
}

type AdminPage[T any] struct {
	Items []T
	Next  []string
}

type adminScanRow interface{ Scan(...any) error }
type adminKeyRow struct {
	adminScanRow
	name *string
}

func (r adminKeyRow) Scan(dest ...any) error { return r.adminScanRow.Scan(append(dest, r.name)...) }

// All comparisons, including the cursor key, use the database's lower() and
// explicit byte collation. ID makes equal names and sort_order deterministic.
func adminReadPage[T any](ctx context.Context, s *Store, endpoint, table, cols string, p AdminPageOptions, scan func(adminScanRow) (*T, error), keys func(T, string) []string) (AdminPage[T], error) {
	out := AdminPage[T]{Items: make([]T, 0)}
	args := []any{}
	bind := func(v any) string { args = append(args, v); return "$" + itoa(len(args)) }
	name := "name"
	if endpoint == "members" {
		name = "username"
	}
	key := `lower(` + name + `) COLLATE "C"`
	q := `SELECT ` + cols + `, ` + key + ` FROM ` + table + ` WHERE true`
	if endpoint == "nodes" || endpoint == "servers" || endpoint == "members" && !p.IncludeArchived {
		q += ` AND NOT archived`
	}
	if endpoint == "nodes" && p.ServerID != "" {
		q += ` AND server_id=` + bind(p.ServerID)
	}
	if endpoint == "members" && p.SubscriptionID != "" {
		q += ` AND subscription_id=` + bind(p.SubscriptionID)
	}
	if p.Q != "" {
		// Literal substring search: %, _ and backslash are not LIKE operators.
		param := bind(p.Q)
		if endpoint == "members" {
			q += ` AND strpos(lower(username),lower(` + param + `))>0`
		}
		if endpoint == "nodes" {
			q += ` AND strpos(lower(name),lower(` + param + `))>0`
		}
	}
	order := key + `,id COLLATE "C"`
	if endpoint == "nodes" {
		order = `sort_order,` + order
	}
	if p.After != nil {
		if endpoint == "nodes" {
			n, _ := strconv.Atoi(p.After[0])
			q += ` AND (sort_order,` + key + `,id COLLATE "C")>(` + bind(n) + `,` + bind(p.After[1]) + ` COLLATE "C",` + bind(p.After[2]) + ` COLLATE "C")`
		} else {
			q += ` AND (` + key + `,id COLLATE "C")>(` + bind(p.After[0]) + ` COLLATE "C",` + bind(p.After[1]) + ` COLLATE "C")`
		}
	}
	limit := clampLimit(p.Limit)
	q += ` ORDER BY ` + order + ` LIMIT ` + bind(limit+1)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return out, dbErr(err)
	}
	defer rows.Close()
	var last []string
	for rows.Next() {
		var nameKey string
		v, err := scan(adminKeyRow{rows, &nameKey})
		if err != nil {
			return out, err
		}
		if len(out.Items) == limit {
			out.Next = last
			break
		}
		out.Items = append(out.Items, *v)
		last = keys(*v, nameKey)
	}
	if err := rows.Err(); err != nil {
		return out, dbErr(err)
	}
	// Release the page connection before loading related bounded collections.
	if err := rows.Close(); err != nil {
		return out, dbErr(err)
	}
	return out, nil
}

func (s *Store) ListAdminServersPage(ctx context.Context, p AdminPageOptions) (AdminPage[domain.Server], error) {
	return adminReadPage(ctx, s, "servers", "servers", serverCols, p, func(r adminScanRow) (*domain.Server, error) { return scanServer(r) }, func(v domain.Server, k string) []string { return []string{k, v.ID} })
}
func (s *Store) ListAdminNodesPage(ctx context.Context, p AdminPageOptions) (AdminPage[domain.Node], error) {
	return adminReadPage(ctx, s, "nodes", "nodes", nodeCols, p, func(r adminScanRow) (*domain.Node, error) { return s.scanNodeRow(r) }, func(v domain.Node, k string) []string { return []string{strconv.Itoa(v.SortOrder), k, v.ID} })
}
func (s *Store) ListAdminMembersPage(ctx context.Context, p AdminPageOptions) (AdminPage[domain.Member], error) {
	return adminReadPage(ctx, s, "members", "members", memberCols, p, func(r adminScanRow) (*domain.Member, error) { return scanMember(r) }, func(v domain.Member, k string) []string { return []string{k, v.ID} })
}
func (s *Store) ListAdminTemplatesPage(ctx context.Context, p AdminPageOptions) (AdminPage[domain.Template], error) {
	return adminReadPage(ctx, s, "templates", "templates", templateCols, p, func(r adminScanRow) (*domain.Template, error) { return scanTemplate(r) }, func(v domain.Template, k string) []string { return []string{k, v.ID} })
}
func (s *Store) ListAdminNodeGroupsPage(ctx context.Context, p AdminPageOptions) (AdminPage[domain.NodeGroup], error) {
	page, err := adminReadPage(ctx, s, "node-groups", "node_groups", nodeGroupCols, p, func(r adminScanRow) (*domain.NodeGroup, error) {
		var v domain.NodeGroup
		if err := r.Scan(&v.ID, &v.Name, &v.Description, &v.Version, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, dbErr(err)
		}
		v.CreatedAt = utc(v.CreatedAt)
		v.UpdatedAt = utc(v.UpdatedAt)
		return &v, nil
	}, func(v domain.NodeGroup, k string) []string { return []string{k, v.ID} })
	if err != nil {
		return page, err
	}
	for i := range page.Items {
		page.Items[i].NodeIDs, err = s.nodeGroupNodeIDs(ctx, page.Items[i].ID)
		if err != nil {
			return page, err
		}
	}
	return page, nil
}
func (s *Store) ListAdminSubscriptionsPage(ctx context.Context, p AdminPageOptions) (AdminPage[domain.Subscription], error) {
	page, err := adminReadPage(ctx, s, "subscriptions", "subscriptions", subscriptionCols, p, func(r adminScanRow) (*domain.Subscription, error) { return scanSubscription(r) }, func(v domain.Subscription, k string) []string { return []string{k, v.ID} })
	if err != nil {
		return page, err
	}
	for i := range page.Items {
		v := &page.Items[i]
		v.NodeGroupIDs, err = s.subscriptionGroupIDs(ctx, s.DB, v.ID)
		if err != nil {
			return page, err
		}
		if err = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM members WHERE subscription_id=$1 AND NOT archived`, v.ID).Scan(&v.BoundMembers); err != nil {
			return page, dbErr(err)
		}
	}
	return page, nil
}
