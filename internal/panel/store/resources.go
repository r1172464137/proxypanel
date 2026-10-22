package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"proxypanel/internal/panel/domain"
)

// lowerString mirrors lower() in the ordering keys of the internal listings.
func lowerString(v string) string { return strings.ToLower(v) }

// ─── servers ───────────────────────────────────────────────────────────────

const serverCols = `id, name, address, region, notes, expires_at, cost_minor, currency, archived, version, created_at, updated_at`

func scanServer(row interface{ Scan(...any) error }) (*domain.Server, error) {
	var s domain.Server
	var expires sql.NullTime
	var cost sql.NullInt64
	if err := row.Scan(&s.ID, &s.Name, &s.Address, &s.Region, &s.Notes, &expires, &cost,
		&s.Currency, &s.Archived, &s.Version, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, dbErr(err)
	}
	if expires.Valid {
		t := expires.Time.UTC()
		s.ExpiresAt = &t
	}
	if cost.Valid {
		v := cost.Int64
		s.CostMinor = &v
	}
	s.CreatedAt = utc(s.CreatedAt)
	s.UpdatedAt = utc(s.UpdatedAt)
	return &s, nil
}

// ListServers returns servers ordered by name.
func (s *Store) ListServers(ctx context.Context, limit int) ([]domain.Server, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+serverCols+` FROM servers WHERE NOT archived ORDER BY lower(name) LIMIT $1`, clampLimit(limit))
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.Server, 0, 8)
	for rows.Next() {
		v, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, dbErr(rows.Err())
}

// GetServer loads one server.
func (s *Store) GetServer(ctx context.Context, id string) (*domain.Server, error) {
	v, err := scanServer(s.DB.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id = $1`, id))
	if err != nil {
		if de, ok := err.(*domain.Error); ok && de.Code == domain.CodeNotFound {
			return nil, domain.NotFound("server not found")
		}
		return nil, err
	}
	return v, nil
}

// CreateServer inserts a server.
func (s *Store) CreateServer(ctx context.Context, v *domain.Server) (*domain.Server, error) {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO servers (`+serverCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,false,1,now(),now())`,
		v.ID, v.Name, v.Address, v.Region, v.Notes, nullableTime(v.ExpiresAt), nullableInt64(v.CostMinor), v.Currency)
	if err != nil {
		return nil, dbErr(err)
	}
	return s.GetServer(ctx, v.ID)
}

// UpdateServer applies fn under a row lock and writes back with a version bump.
func (s *Store) UpdateServer(ctx context.Context, id string, expectedVersion int64, fn func(*domain.Server) error) (*domain.Server, error) {
	var out *domain.Server
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := scanServer(tx.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id = $1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if expectedVersion != 0 && cur.Version != expectedVersion {
			return domain.Conflict(domain.CodeConflict, "server was modified by another request")
		}
		if err := fn(cur); err != nil {
			return err
		}
		cur.Version++
		cur.UpdatedAt = s.now()
		if _, err := tx.ExecContext(ctx, `UPDATE servers SET name=$2,address=$3,region=$4,notes=$5,expires_at=$6,
			cost_minor=$7,currency=$8,archived=$9,version=$10,updated_at=$11 WHERE id=$1`,
			cur.ID, cur.Name, cur.Address, cur.Region, cur.Notes, nullableTime(cur.ExpiresAt),
			nullableInt64(cur.CostMinor), cur.Currency, cur.Archived, cur.Version, cur.UpdatedAt); err != nil {
			return dbErr(err)
		}
		out = cur
		return nil
	})
	return out, err
}

// ─── templates ─────────────────────────────────────────────────────────────

const templateCols = `id, name, format, content, description, version, created_at, updated_at`

func scanTemplate(row interface{ Scan(...any) error }) (*domain.Template, error) {
	var v domain.Template
	var content []byte
	if err := row.Scan(&v.ID, &v.Name, &v.Format, &content, &v.Description, &v.Version, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, dbErr(err)
	}
	v.Content = scanObject(content)
	v.CreatedAt = utc(v.CreatedAt)
	v.UpdatedAt = utc(v.UpdatedAt)
	return &v, nil
}

func (s *Store) ListTemplates(ctx context.Context, limit int) ([]domain.Template, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+templateCols+` FROM templates ORDER BY lower(name) LIMIT $1`, clampLimit(limit))
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.Template, 0, 4)
	for rows.Next() {
		v, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, dbErr(rows.Err())
}

func (s *Store) GetTemplate(ctx context.Context, id string) (*domain.Template, error) {
	return scanTemplate(s.DB.QueryRowContext(ctx, `SELECT `+templateCols+` FROM templates WHERE id = $1`, id))
}

func (s *Store) CreateTemplate(ctx context.Context, v *domain.Template) (*domain.Template, error) {
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO templates (id,name,format,content,description,version,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,1,now(),now())`, v.ID, v.Name, v.Format, encodeObject(v.Content), v.Description); err != nil {
		return nil, dbErr(err)
	}
	return s.GetTemplate(ctx, v.ID)
}

func (s *Store) UpdateTemplate(ctx context.Context, id string, expectedVersion int64, fn func(*domain.Template) error) (*domain.Template, error) {
	var out *domain.Template
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := scanTemplate(tx.QueryRowContext(ctx, `SELECT `+templateCols+` FROM templates WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if expectedVersion != 0 && cur.Version != expectedVersion {
			return domain.Conflict(domain.CodeConflict, "template was modified by another request")
		}
		if err := fn(cur); err != nil {
			return err
		}
		cur.Version++
		cur.UpdatedAt = s.now()
		if _, err := tx.ExecContext(ctx, `UPDATE templates SET name=$2,format=$3,content=$4,description=$5,version=$6,updated_at=$7 WHERE id=$1`,
			cur.ID, cur.Name, cur.Format, encodeObject(cur.Content), cur.Description, cur.Version, cur.UpdatedAt); err != nil {
			return dbErr(err)
		}
		out = cur
		return nil
	})
	return out, err
}

func (s *Store) DeleteTemplate(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM templates WHERE id=$1`, id)
	if err != nil {
		return dbErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.NotFound("template not found")
	}
	return nil
}

// ─── node groups ───────────────────────────────────────────────────────────

const nodeGroupCols = `id, name, description, version, created_at, updated_at`

func (s *Store) nodeGroupNodeIDs(ctx context.Context, groupID string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT node_id FROM node_group_members WHERE group_id=$1 ORDER BY position, node_id`, groupID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]string, 0, 8)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, dbErr(err)
		}
		out = append(out, id)
	}
	return out, dbErr(rows.Err())
}

func (s *Store) loadNodeGroup(ctx context.Context, q querier, id string) (*domain.NodeGroup, error) {
	var v domain.NodeGroup
	if err := q.QueryRowContext(ctx, `SELECT `+nodeGroupCols+` FROM node_groups WHERE id=$1`, id).Scan(
		&v.ID, &v.Name, &v.Description, &v.Version, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, dbErr(err)
	}
	v.CreatedAt = utc(v.CreatedAt)
	v.UpdatedAt = utc(v.UpdatedAt)
	ids, err := s.nodeGroupNodeIDs(ctx, id)
	if err != nil {
		return nil, err
	}
	v.NodeIDs = ids
	return &v, nil
}

func (s *Store) ListNodeGroups(ctx context.Context, limit int) ([]domain.NodeGroup, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+nodeGroupCols+` FROM node_groups ORDER BY lower(name) LIMIT $1`, clampLimit(limit))
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.NodeGroup, 0, 4)
	for rows.Next() {
		var v domain.NodeGroup
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &v.Version, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, dbErr(err)
		}
		v.CreatedAt = utc(v.CreatedAt)
		v.UpdatedAt = utc(v.UpdatedAt)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr(err)
	}
	for i := range out {
		ids, err := s.nodeGroupNodeIDs(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].NodeIDs = ids
	}
	return out, nil
}

func (s *Store) GetNodeGroup(ctx context.Context, id string) (*domain.NodeGroup, error) {
	v, err := s.loadNodeGroup(ctx, s.DB, id)
	if err != nil {
		if de, ok := err.(*domain.Error); ok && de.Code == domain.CodeNotFound {
			return nil, domain.NotFound("node group not found")
		}
		return nil, err
	}
	return v, nil
}

func setGroupNodes(ctx context.Context, tx *sql.Tx, groupID string, nodeIDs []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM node_group_members WHERE group_id=$1`, groupID); err != nil {
		return dbErr(err)
	}
	for i, nodeID := range nodeIDs {
		if nodeID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO node_group_members (group_id,node_id,position) VALUES ($1,$2,$3)`,
			groupID, nodeID, i); err != nil {
			return dbErr(err)
		}
	}
	return nil
}

func (s *Store) CreateNodeGroup(ctx context.Context, v *domain.NodeGroup) (*domain.NodeGroup, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO node_groups (id,name,description,version,created_at,updated_at)
			VALUES ($1,$2,$3,1,now(),now())`, v.ID, v.Name, v.Description); err != nil {
			return dbErr(err)
		}
		if err := setGroupNodes(ctx, tx, v.ID, v.NodeIDs); err != nil {
			return err
		}
		_, err := s.bumpRevisionsTx(ctx, tx, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetNodeGroup(ctx, v.ID)
}

func (s *Store) UpdateNodeGroup(ctx context.Context, id string, expectedVersion int64, fn func(*domain.NodeGroup) error) (*domain.NodeGroup, error) {
	var out *domain.NodeGroup
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := s.loadNodeGroup(ctx, tx, id)
		if err != nil {
			return err
		}
		if expectedVersion != 0 && cur.Version != expectedVersion {
			return domain.Conflict(domain.CodeConflict, "node group was modified by another request")
		}
		if err := fn(cur); err != nil {
			return err
		}
		cur.Version++
		cur.UpdatedAt = s.now()
		if _, err := tx.ExecContext(ctx, `UPDATE node_groups SET name=$2,description=$3,version=$4,updated_at=$5 WHERE id=$1`,
			cur.ID, cur.Name, cur.Description, cur.Version, cur.UpdatedAt); err != nil {
			return dbErr(err)
		}
		if err := setGroupNodes(ctx, tx, cur.ID, cur.NodeIDs); err != nil {
			return err
		}
		// Group membership decides which nodes a subscription may use, so the
		// revision is published in this transaction.
		if _, err := s.bumpRevisionsTx(ctx, tx, nil); err != nil {
			return err
		}
		out = cur
		return nil
	})
	return out, err
}

func (s *Store) DeleteNodeGroup(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var subs int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM subscription_node_groups WHERE node_group_id=$1`, id).Scan(&subs); err != nil {
			return dbErr(err)
		}
		if subs > 0 {
			return domain.Conflict(domain.CodeNodeInUse, "node group is referenced by %d subscription(s)", subs)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM node_groups WHERE id=$1`, id)
		if err != nil {
			return dbErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return domain.NotFound("node group not found")
		}
		_, err = s.bumpRevisionsTx(ctx, tx, nil)
		return err
	})
}

// ─── nodes ─────────────────────────────────────────────────────────────────

const nodeCols = `id, server_id, name, enabled, host, port, external_port, listen, protocol, flow, cipher, server_key_enc,
	stream_settings, protocol_settings, client_settings, sort_order, archived, version, created_at, updated_at`

func scanNode(row interface{ Scan(...any) error }, decrypt func([]byte) (string, error)) (*domain.Node, error) {
	var v domain.Node
	var keyEnc []byte
	var stream, ps, cs []byte
	var externalPort sql.NullInt64
	if err := row.Scan(&v.ID, &v.ServerID, &v.Name, &v.Enabled, &v.Host, &v.Port, &externalPort, &v.Listen, &v.Protocol,
		&v.Flow, &v.Cipher, &keyEnc, &stream, &ps, &cs, &v.SortOrder, &v.Archived, &v.Version,
		&v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, dbErr(err)
	}
	if externalPort.Valid {
		v.ExternalPort = int(externalPort.Int64)
	}
	v.StreamSettings = scanObject(stream)
	v.ProtocolSettings = scanObject(ps)
	v.ClientSettings = scanObject(cs)
	if len(keyEnc) > 0 {
		if plain, err := decrypt(keyEnc); err == nil {
			v.ServerKey = plain
			v.ServerKeySet = true
		}
	}
	v.CreatedAt = utc(v.CreatedAt)
	v.UpdatedAt = utc(v.UpdatedAt)
	return &v, nil
}

func (s *Store) scanNodeRow(row interface{ Scan(...any) error }) (*domain.Node, error) {
	return scanNode(row, s.decryptSecret)
}

// ListNodes returns nodes ordered by sort_order then name.
func (s *Store) ListNodes(ctx context.Context, serverID string, limit int) ([]domain.Node, error) {
	q := `SELECT ` + nodeCols + ` FROM nodes WHERE NOT archived`
	args := []any{}
	if serverID != "" {
		q += ` AND server_id = $1`
		args = append(args, serverID)
	}
	q += ` ORDER BY sort_order, lower(name) LIMIT $` + itoa(len(args)+1)
	args = append(args, clampLimit(limit))
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.Node, 0, 8)
	for rows.Next() {
		v, err := s.scanNodeRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, dbErr(rows.Err())
}

// internalPageSize bounds one page of an internal listing. Internal readers
// keep paging until the table is exhausted, so authoritative code never
// silently drops rows beyond the public 200 row page (see the public
// ListNodes/ListMembers limits).
const internalPageSize = 500

// listAllNodes returns every non archived node, page by page. Authorization,
// snapshots and credential reconciliation must use it instead of
// ListNodes(ctx, serverID, 200).
func (s *Store) listAllNodes(ctx context.Context, serverID string) ([]domain.Node, error) {
	out := make([]domain.Node, 0, 32)
	lastSort, lastName, lastID := 0, "", ""
	first := true
	for {
		q := `SELECT ` + nodeCols + ` FROM nodes WHERE NOT archived`
		args := []any{}
		if serverID != "" {
			args = append(args, serverID)
			q += ` AND server_id = $` + itoa(len(args))
		}
		if !first {
			base := len(args)
			args = append(args, lastSort, lastName, lastID)
			q += ` AND (sort_order, lower(name), id) > ($` + itoa(base+1) + `::int, $` +
				itoa(base+2) + `::text, $` + itoa(base+3) + `::text)`
		}
		args = append(args, internalPageSize)
		q += ` ORDER BY sort_order, lower(name), id LIMIT $` + itoa(len(args))
		rows, err := s.DB.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, dbErr(err)
		}
		page := 0
		for rows.Next() {
			v, err := s.scanNodeRow(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, *v)
			lastSort, lastName, lastID = v.SortOrder, lowerString(v.Name), v.ID
			page++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, dbErr(err)
		}
		if page < internalPageSize {
			return out, nil
		}
		first = false
	}
}

func (s *Store) GetNode(ctx context.Context, id string) (*domain.Node, error) {
	v, err := s.scanNodeRow(s.DB.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=$1`, id))
	if err != nil {
		if de, ok := err.(*domain.Error); ok && de.Code == domain.CodeNotFound {
			return nil, domain.NotFound("node not found")
		}
		return nil, err
	}
	return v, nil
}

// CreateNode inserts the node and its core instance row.
func (s *Store) CreateNode(ctx context.Context, v *domain.Node, coreInstanceID string) (*domain.Node, error) {
	keyEnc, err := s.encryptSecret(v.ServerKey)
	if err != nil {
		return nil, err
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO nodes (`+nodeCols+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,false,1,now(),now())`,
			v.ID, v.ServerID, v.Name, v.Enabled, v.Host, v.Port, nullableExternalPort(v.ExternalPort), v.Listen,
			v.Protocol, v.Flow, v.Cipher,
			keyEnc, encodeObject(v.StreamSettings), encodeObject(v.ProtocolSettings), encodeObject(v.ClientSettings),
			v.SortOrder); err != nil {
			return dbErr(err)
		}
		// One independently restartable core instance per node (contract:
		// instance id == node id for the agent). The instance is linked to the
		// agent that already serves this server so the revision bump below can
		// dispatch to it even when it registered before the node existed.
		_, err := tx.ExecContext(ctx, `INSERT INTO core_instances (id, server_id, node_id, agent_id, state, updated_at)
			VALUES ($1,$2,$3,(SELECT a.id FROM agents a WHERE a.server_id=$2 AND NOT a.archived
				ORDER BY a.created_at DESC LIMIT 1),'stopped',now())
			ON CONFLICT (node_id) DO NOTHING`, coreInstanceID, v.ServerID, v.ID)
		if err != nil {
			return dbErr(err)
		}
		_, err = s.bumpRevisionsTx(ctx, tx, []string{v.ID})
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetNode(ctx, v.ID)
}

// UpdateNode applies fn under a row lock and bumps the version.
func (s *Store) UpdateNode(ctx context.Context, id string, expectedVersion int64, fn func(*domain.Node) error) (*domain.Node, error) {
	var out *domain.Node
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := s.scanNodeRow(tx.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if expectedVersion != 0 && cur.Version != expectedVersion {
			return domain.Conflict(domain.CodeConflict, "node was modified by another request")
		}
		oldServerID := cur.ServerID
		if err := fn(cur); err != nil {
			return err
		}
		cur.Version++
		cur.UpdatedAt = s.now()
		keyEnc, err := s.encryptSecret(cur.ServerKey)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE nodes SET name=$2,server_id=$3,enabled=$4,host=$5,port=$6,external_port=$7,
			listen=$8,protocol=$9,flow=$10,cipher=$11,server_key_enc=$12,stream_settings=$13,protocol_settings=$14,
			client_settings=$15,sort_order=$16,archived=$17,version=$18,updated_at=$19 WHERE id=$1`,
			cur.ID, cur.Name, cur.ServerID, cur.Enabled, cur.Host, cur.Port, nullableExternalPort(cur.ExternalPort),
			cur.Listen, cur.Protocol, cur.Flow,
			cur.Cipher, keyEnc, encodeObject(cur.StreamSettings), encodeObject(cur.ProtocolSettings),
			encodeObject(cur.ClientSettings), cur.SortOrder, cur.Archived, cur.Version, cur.UpdatedAt); err != nil {
			return dbErr(err)
		}
		if cur.ServerID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE core_instances SET server_id=$2, updated_at=now() WHERE node_id=$1`,
				cur.ID, cur.ServerID); err != nil {
				return dbErr(err)
			}
		}
		// The change and the revision that publishes it share this
		// transaction: a node edit, disable or archive can never commit
		// without the agents being told to re-read their authorization.
		rev, err := s.bumpRevisionsTx(ctx, tx, []string{cur.ID})
		if err != nil {
			return err
		}
		if oldServerID != cur.ServerID {
			// The node left its previous server: revoke there as well, or the
			// old host would keep serving the removed authorization.
			if err := s.bumpServerAgentsTx(ctx, tx, oldServerID, rev); err != nil {
				return err
			}
		}
		out = cur
		return nil
	})
	return out, err
}

// DeleteNode archives a node, keeping usage history and revocation mappings.
func (s *Store) DeleteNode(ctx context.Context, id string) (*domain.Node, error) {
	return s.UpdateNode(ctx, id, 0, func(n *domain.Node) error {
		n.Archived = true
		n.Enabled = false
		return nil
	})
}

// ─── subscriptions ─────────────────────────────────────────────────────────

const subscriptionCols = `id, name, description, quota_bytes, timezone, anchor_day, default_valid_days,
	allow_new_bindings, access_enabled, archived, template_id, pending_period_rule, version, created_at, updated_at`

func (s *Store) subscriptionGroupIDs(ctx context.Context, q querier, subID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT node_group_id FROM subscription_node_groups WHERE subscription_id=$1 ORDER BY position`, subID)
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]string, 0, 4)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, dbErr(err)
		}
		out = append(out, id)
	}
	return out, dbErr(rows.Err())
}

func scanSubscription(row interface{ Scan(...any) error }) (*domain.Subscription, error) {
	var v domain.Subscription
	var quota, validDays sql.NullInt64
	var templateID sql.NullString
	var pending []byte
	if err := row.Scan(&v.ID, &v.Name, &v.Description, &quota, &v.Timezone, &v.AnchorDay, &validDays,
		&v.AllowNewBindings, &v.AccessEnabled, &v.Archived, &templateID, &pending, &v.Version,
		&v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, dbErr(err)
	}
	if quota.Valid {
		val := quota.Int64
		v.QuotaBytes = &val
	}
	if validDays.Valid {
		val := int(validDays.Int64)
		v.DefaultValidDays = &val
	}
	if templateID.Valid {
		val := templateID.String
		v.TemplateID = &val
	}
	if len(pending) > 0 && string(pending) != "null" {
		var rule domain.PeriodRule
		if err := json.Unmarshal(pending, &rule); err == nil {
			v.PendingPeriodRule = &rule
		}
	}
	v.CreatedAt = utc(v.CreatedAt)
	v.UpdatedAt = utc(v.UpdatedAt)
	return &v, nil
}

func (s *Store) ListSubscriptions(ctx context.Context, limit int) ([]domain.Subscription, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+subscriptionCols+`,
		(SELECT count(*) FROM members m WHERE m.subscription_id = subscriptions.id AND NOT m.archived)
		FROM subscriptions ORDER BY lower(name) LIMIT $1`, clampLimit(limit))
	if err != nil {
		return nil, dbErr(err)
	}
	defer rows.Close()
	out := make([]domain.Subscription, 0, 4)
	for rows.Next() {
		var v domain.Subscription
		var quota, validDays sql.NullInt64
		var templateID sql.NullString
		var pending []byte
		var bound int
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &quota, &v.Timezone, &v.AnchorDay, &validDays,
			&v.AllowNewBindings, &v.AccessEnabled, &v.Archived, &templateID, &pending, &v.Version,
			&v.CreatedAt, &v.UpdatedAt, &bound); err != nil {
			return nil, dbErr(err)
		}
		if quota.Valid {
			val := quota.Int64
			v.QuotaBytes = &val
		}
		if validDays.Valid {
			val := int(validDays.Int64)
			v.DefaultValidDays = &val
		}
		if templateID.Valid {
			val := templateID.String
			v.TemplateID = &val
		}
		if len(pending) > 0 && string(pending) != "null" {
			var rule domain.PeriodRule
			if err := json.Unmarshal(pending, &rule); err == nil {
				v.PendingPeriodRule = &rule
			}
		}
		v.CreatedAt = utc(v.CreatedAt)
		v.UpdatedAt = utc(v.UpdatedAt)
		v.BoundMembers = bound
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, dbErr(err)
	}
	for i := range out {
		ids, err := s.subscriptionGroupIDs(ctx, s.DB, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].NodeGroupIDs = ids
	}
	return out, nil
}

func (s *Store) GetSubscription(ctx context.Context, id string) (*domain.Subscription, error) {
	v, err := scanSubscription(s.DB.QueryRowContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions WHERE id=$1`, id))
	if err != nil {
		if de, ok := err.(*domain.Error); ok && de.Code == domain.CodeNotFound {
			return nil, domain.NotFound("subscription not found")
		}
		return nil, err
	}
	ids, err := s.subscriptionGroupIDs(ctx, s.DB, id)
	if err != nil {
		return nil, err
	}
	v.NodeGroupIDs = ids
	var bound int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM members WHERE subscription_id=$1 AND NOT archived`, id).Scan(&bound); err == nil {
		v.BoundMembers = bound
	}
	return v, nil
}

func pendingJSON(rule *domain.PeriodRule) any {
	if rule == nil {
		return nil
	}
	raw, err := json.Marshal(rule)
	if err != nil {
		return nil
	}
	return raw
}

func setSubscriptionGroups(ctx context.Context, tx *sql.Tx, subID string, groupIDs []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM subscription_node_groups WHERE subscription_id=$1`, subID); err != nil {
		return dbErr(err)
	}
	for i, gid := range groupIDs {
		if gid == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO subscription_node_groups (subscription_id,node_group_id,position)
			VALUES ($1,$2,$3)`, subID, gid, i); err != nil {
			return dbErr(err)
		}
	}
	return nil
}

func (s *Store) CreateSubscription(ctx context.Context, v *domain.Subscription) (*domain.Subscription, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO subscriptions (`+subscriptionCols+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,false,$10,$11,1,now(),now())`,
			v.ID, v.Name, v.Description, nullableInt64(v.QuotaBytes), v.Timezone, v.AnchorDay,
			nullableInt(v.DefaultValidDays), v.AllowNewBindings, v.AccessEnabled,
			nullableString(v.TemplateID), pendingJSON(v.PendingPeriodRule)); err != nil {
			return dbErr(err)
		}
		if err := setSubscriptionGroups(ctx, tx, v.ID, v.NodeGroupIDs); err != nil {
			return err
		}
		_, err := s.bumpRevisionsTx(ctx, tx, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetSubscription(ctx, v.ID)
}

func (s *Store) UpdateSubscription(ctx context.Context, id string, expectedVersion int64, fn func(*domain.Subscription) error) (*domain.Subscription, error) {
	var out *domain.Subscription
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := lockMemberChangesTx(ctx, tx); err != nil {
			return err
		}
		cur, err := scanSubscription(tx.QueryRowContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if expectedVersion != 0 && cur.Version != expectedVersion {
			return domain.Conflict(domain.CodeConflict, "subscription was modified by another request")
		}
		oldAnchor, oldTZ := cur.AnchorDay, cur.Timezone
		if err := fn(cur); err != nil {
			return err
		}
		cur.Version++
		cur.UpdatedAt = s.now()
		if _, err := tx.ExecContext(ctx, `UPDATE subscriptions SET name=$2,description=$3,quota_bytes=$4,timezone=$5,
			anchor_day=$6,default_valid_days=$7,allow_new_bindings=$8,access_enabled=$9,archived=$10,
			template_id=$11,pending_period_rule=$12,version=$13,updated_at=$14 WHERE id=$1`,
			cur.ID, cur.Name, cur.Description, nullableInt64(cur.QuotaBytes), cur.Timezone, cur.AnchorDay,
			nullableInt(cur.DefaultValidDays), cur.AllowNewBindings, cur.AccessEnabled, cur.Archived,
			nullableString(cur.TemplateID), pendingJSON(cur.PendingPeriodRule), cur.Version, cur.UpdatedAt); err != nil {
			return dbErr(err)
		}
		if cur.NodeGroupIDs != nil {
			if err := setSubscriptionGroups(ctx, tx, cur.ID, cur.NodeGroupIDs); err != nil {
				return err
			}
		}
		if cur.AnchorDay != oldAnchor || cur.Timezone != oldTZ {
			if err := s.syncSubscriptionCyclesTx(ctx, tx, cur.ID, cur.UpdatedAt); err != nil {
				return err
			}
		}
		// Subscription rules (quota, groups, access switch, archive) decide
		// member authorization: publish the revision in this transaction.
		if _, err := s.bumpRevisionsTx(ctx, tx, nil); err != nil {
			return err
		}
		out = cur
		return nil
	})
	return out, err
}

// A direct store update follows the same scheduling semantics as a batch:
// preserve a running cycle and persist its replacement rule at edit time.
func (s *Store) syncSubscriptionCyclesTx(ctx context.Context, tx *sql.Tx, subID string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT `+memberCols+` FROM members WHERE subscription_id=$1 ORDER BY id FOR UPDATE`, subID)
	if err != nil {
		return dbErr(err)
	}
	var members []*domain.Member
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			rows.Close()
			return err
		}
		members = append(members, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return dbErr(err)
	}
	for _, m := range members {
		if _, err := s.advanceMemberPeriodTx(ctx, tx, m, now); err != nil {
			return err
		}
		if err := s.syncMemberCycle(ctx, tx, m, now); err != nil {
			return err
		}
	}
	return nil
}

// DeleteSubscription archives a subscription; bound members must be migrated
// first (enforced by the handler via BoundMembers).
// DeleteSubscription physically removes a subscription.
//
// 这里以前是"软删"（只把 Archived 置 true），导致三个问题：
//   - 界面按钮叫"删除"、确认框也写"将删除"，但数据其实还在；
//   - 归档的订阅仍出现在"目标订阅"下拉里，可以被选为新绑定的目标；
//   - 归档订阅仍占用节点组引用，使节点组永远删不掉，且没有任何入口能清理。
//
// 现在改成真删除。两个引用关系由数据库保证：
//   - subscription_node_groups.subscription_id 是 ON DELETE CASCADE，
//     删除订阅会自动解除与节点组的关联，节点组随后可删；
//   - members.subscription_id 没有级联，所以仍有成员绑定时删不掉 ——
//     这正是我们要的行为：先解绑成员，再删订阅。
//
// 已归档的订阅同样适用（归档只是一种标记，不影响能否删除）。
func (s *Store) DeleteSubscription(ctx context.Context, id string) (*domain.Subscription, error) {
	// 先取出快照用于返回，并在同一事务里校验引用。
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, dbErr(err)
	}
	defer func() { _ = tx.Rollback() }()

	cur, err := scanSubscription(tx.QueryRowContext(ctx,
		`SELECT `+subscriptionCols+` FROM subscriptions WHERE id=$1`, id))
	if err != nil {
		if de, ok := err.(*domain.Error); ok && de.Code == domain.CodeNotFound {
			return nil, domain.NotFound("subscription not found")
		}
		return nil, err
	}
	groupIDs, err := s.subscriptionGroupIDs(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	cur.NodeGroupIDs = groupIDs

	var bound int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM members WHERE subscription_id = $1 AND NOT archived`, id).Scan(&bound); err != nil {
		return nil, dbErr(err)
	}
	if bound > 0 {
		return nil, domain.Conflict(domain.CodeNodeInUse,
			"subscription still has %d bound member(s)", bound)
	}
	// 归档成员不该再持有订阅引用：它们已经失去访问权，只是为审计/账本保留记录。
	// 这里先解除关联，否则外键会永久阻止删除订阅 —— 而成员删除本身也是归档，
	// 两者叠加就形成"谁都无法清理"的死循环。
	if _, err := tx.ExecContext(ctx,
		`UPDATE members SET subscription_id = NULL WHERE subscription_id = $1 AND archived`, id); err != nil {
		return nil, dbErr(err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM subscriptions WHERE id = $1`, id); err != nil {
		return nil, dbErr(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, dbErr(err)
	}
	return cur, nil
}

// ─── settings & control state ──────────────────────────────────────────────

func (s *Store) GetSettings(ctx context.Context) (*domain.Settings, error) {
	return settingsQ(ctx, s.DB)
}

// settingsQ is the transaction scoped variant of GetSettings. Usage accounting
// reads the fallback timezone inside its own transaction instead of opening a
// second connection that could wait on the row lock it already holds.
func settingsQ(ctx context.Context, q querier) (*domain.Settings, error) {
	var v domain.Settings
	var tokenEnc []byte
	var defaults []byte
	if err := q.QueryRowContext(ctx, `SELECT timezone, retention_days, telegram_enabled, telegram_chat_id,
		telegram_token_enc, telegram_token_set, template_defaults, version, updated_at FROM settings WHERE id=1`).Scan(
		&v.Timezone, &v.RetentionDays, &v.TelegramEnabled, &v.TelegramChatID, &tokenEnc, &v.TelegramTokenSet,
		&defaults, &v.Version, &v.UpdatedAt); err != nil {
		return nil, dbErr(err)
	}
	v.TemplateDefaults = scanObject(defaults)
	v.UpdatedAt = utc(v.UpdatedAt)
	return &v, nil
}

// UpdateSettings applies fn and encrypts the telegram token when provided.
func (s *Store) UpdateSettings(ctx context.Context, expectedVersion int64, fn func(*domain.Settings) error) (*domain.Settings, error) {
	var out *domain.Settings
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var v domain.Settings
		var defaults []byte
		var tokenEnc []byte
		if err := tx.QueryRowContext(ctx, `SELECT timezone, retention_days, telegram_enabled, telegram_chat_id,
			telegram_token_enc, telegram_token_set, template_defaults, version, updated_at FROM settings WHERE id=1 FOR UPDATE`).Scan(
			&v.Timezone, &v.RetentionDays, &v.TelegramEnabled, &v.TelegramChatID, &tokenEnc, &v.TelegramTokenSet,
			&defaults, &v.Version, &v.UpdatedAt); err != nil {
			return dbErr(err)
		}
		v.TemplateDefaults = scanObject(defaults)
		if expectedVersion != 0 && v.Version != expectedVersion {
			return domain.Conflict(domain.CodeConflict, "settings were modified by another request")
		}
		if err := fn(&v); err != nil {
			return err
		}
		newToken := tokenEnc
		tokenSet := v.TelegramTokenSet
		if v.TelegramToken != "" {
			enc, err := s.encryptSecret(v.TelegramToken)
			if err != nil {
				return err
			}
			newToken = enc
			tokenSet = true
		}
		v.Version++
		v.UpdatedAt = s.now()
		if _, err := tx.ExecContext(ctx, `UPDATE settings SET timezone=$1,retention_days=$2,telegram_enabled=$3,
			telegram_chat_id=$4,telegram_token_enc=$5,telegram_token_set=$6,template_defaults=$7,version=$8,
			updated_at=$9 WHERE id=1`,
			v.Timezone, v.RetentionDays, v.TelegramEnabled, v.TelegramChatID, newToken, tokenSet,
			encodeObject(v.TemplateDefaults), v.Version, v.UpdatedAt); err != nil {
			return dbErr(err)
		}
		v.TelegramToken = ""
		v.TelegramTokenSet = tokenSet
		out = &v
		return nil
	})
	return out, err
}

// TelegramToken returns the decrypted bot token (notifications only).
func (s *Store) TelegramToken(ctx context.Context) (string, error) {
	var enc []byte
	if err := s.DB.QueryRowContext(ctx, `SELECT telegram_token_enc FROM settings WHERE id=1`).Scan(&enc); err != nil {
		return "", dbErr(err)
	}
	return s.decryptSecret(enc)
}

func (s *Store) GetControlState(ctx context.Context) (*domain.ControlState, error) {
	var v domain.ControlState
	var entered sql.NullTime
	if err := s.DB.QueryRowContext(ctx, `SELECT control_generation, revision_counter, recovery_mode,
		recovery_entered_at, updated_at FROM control_state WHERE id=1`).Scan(
		&v.ControlGeneration, &v.RevisionCounter, &v.RecoveryMode, &entered, &v.UpdatedAt); err != nil {
		return nil, dbErr(err)
	}
	if entered.Valid {
		t := entered.Time.UTC()
		v.RecoveryEnteredAt = &t
	}
	v.UpdatedAt = utc(v.UpdatedAt)
	return &v, nil
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
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

var _ = time.Now

// NodeGroupUsage counts subscriptions that reference the group.
func (s *Store) NodeGroupUsage(ctx context.Context, groupID string) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM subscription_node_groups WHERE node_group_id=$1`, groupID).Scan(&n)
	return n, dbErr(err)
}

// nullableExternalPort 把「未设置」的对外端口写成 NULL 而不是 0，
// 这样数据库里只有真正的端口值，检查约束也不需要为 0 开特例。
func nullableExternalPort(port int) any {
	if port <= 0 {
		return nil
	}
	return port
}
