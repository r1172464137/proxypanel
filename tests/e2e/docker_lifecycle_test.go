package e2e

// Bounded acceptance for goal criteria 2, 4 and 8 (A02, A07, A13) on the owned
// Docker acceptance stack. Goal:
// .pi/goal/完成计划剩余任务与-docker-ssh-隧道验收-20261021-1045.md
//
// Really executed when PANEL_DOCKER_TEST=1 and the SSH tunnel is up:
//   - A02: one batch against two owned nodes while one Agent container is
//     stopped. The partial dispatch state is read back from the panel, the item
//     that never reached its Agent is swept to timeout with its reason, the
//     recovered Agent applies the revocation, the *same* timed out task is
//     retried through POST /api/v1/admin/tasks/{id}/retry and the original batch
//     must converge with the nodes.
//   - A07: an Xray document the real core rejects is injected through the panel
//     for one node of an Agent that also serves a second node: it is never
//     applied, the other node keeps serving, the affected node keeps its last
//     good document, and the technical rollback does not restore a revoked
//     member.
//   - A13: the Agent binary is replaced by the 0.1.1 build inside the container
//     and rolled back to the shipped 0.1.0 build; the core is switched
//     26.3.27 -> 26.3.23 -> 26.3.27 under the *new* Agent and then back to the
//     shipped 0.1.0. A batch that is pending at the switch instant is followed
//     per batch id/bytes and must be either still in the durable queue or
//     credited exactly once in PostgreSQL.
//
// Every injected fault registers its restore before it is injected. When a case
// ends the fixture node is archived (DELETE /api/v1/admin/nodes/{id}, which keeps
// its usage history, audit rows and ledger entries) and both members are
// deactivated. Only an archived node leaves the desired snapshot, so only the
// archive releases the listener on 20105; merely disabling the node keeps its core
// listening with no clients. A rerun therefore rebuilds the fixture with a new
// uniquely named node (see lifecycleFixture) instead of re-enabling the old one.
//
// Not claimed here: host systemd, host reboot, WSL, public-internet exposure,
// Telegram delivery. This case shares the Docker fixture with
// TestDockerAcceptance and must never run in parallel.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/usagecalc"
)

const (
	lifecycleScript = "lifecycle.sh"
	// lifecycleNodePort is a node of the same Agent as docker-vless-1 (server 1,
	// Agent 1). The Agent publishes 20101-20105 on loopback, so a second instance
	// of the same Agent is reachable through the SSH tunnel. The node is disabled
	// again at the end of every case, so the port is free for the client matrix.
	lifecycleNodePort        = 20105
	lifecycleShippedVersion  = "0.1.0"
	lifecycleUpgradedVersion = "0.1.1"
	lifecycleShippedCore     = "26.3.27"
	lifecycleStagedCore      = "26.3.23"
)

// lifecycleFixture holds the extra objects of this file. They never join the
// shared docker-group, so the export of the shared member used by
// TestDockerAcceptance keeps exactly the nodes it expects.
type lifecycleFixture struct {
	Group        string `json:"group"`
	Subscription string `json:"subscription"`
	Node         string `json:"node"`
	Active       string `json:"active"`
	Revoked      string `json:"revoked"`
}

// loadLifecycleFixture only reads the persisted fixture ids. It never creates
// anything, so read-only entries (the diagnose case) cannot add data to a broken
// stack; a missing or incomplete file is reported instead.
func (d *dockerTest) loadLifecycleFixture() (lifecycleFixture, string, bool) {
	d.t.Helper()
	path := filepath.Join(d.private, "lifecycle-fixture.json")
	var f lifecycleFixture
	b, err := os.ReadFile(path)
	if err != nil {
		return f, path, false
	}
	if json.Unmarshal(b, &f) != nil || f.Group == "" || f.Node == "" || f.Active == "" || f.Revoked == "" {
		return f, path, false
	}
	return f, path, true
}

// lifecycleFixture returns the fixture the three acceptance cases share. The first
// run creates the node, the group, the subscription and both members. A later run
// finds the node archived by lcQuiesce: PATCH cannot un-archive a node and the name
// index is global (migrations/0001_init.sql:64), so the archived node stays in the
// panel for history, audit and ledger, and a new node with a unique name joins the
// same group, subscription and members (see lcRebuildFixtureNode).
func (d *dockerTest) lifecycleFixture() lifecycleFixture {
	d.t.Helper()
	if existing, path, ok := d.loadLifecycleFixture(); ok {
		archived, found := d.lcNodeArchived(existing.Node)
		if found && !archived {
			return existing
		}
		if !found {
			d.t.Logf("the lifecycle node %s recorded in %s does not exist anymore; rebuilding it", existing.Node, path)
		}
		return d.lcRebuildFixtureNode(existing, path)
	}
	var f lifecycleFixture
	f.Node = d.lcCreateNode()
	group := d.api.request("POST", "/api/v1/admin/node-groups", map[string]any{
		"name": "docker-lifecycle-group", "node_ids": []string{d.fixture.Nodes[0], f.Node}})
	f.Group = group["id"].(string)
	sub := d.api.request("POST", "/api/v1/admin/subscriptions", map[string]any{
		"name": "docker-lifecycle-sub", "node_group_ids": []string{f.Group}, "quota_bytes": 1 << 30,
		"timezone": "UTC", "anchor_day": 1, "allow_new_bindings": true, "access_enabled": true})
	f.Subscription = sub["id"].(string)
	f.Active = d.api.request("POST", "/api/v1/admin/members", map[string]any{
		"username": "docker-lifecycle-active", "password": d.password, "role": "member",
		"subscription_id": f.Subscription})["id"].(string)
	f.Revoked = d.api.request("POST", "/api/v1/admin/members", map[string]any{
		"username": "docker-lifecycle-revoked", "password": d.password, "role": "member",
		"subscription_id": f.Subscription})["id"].(string)
	writeConfig(d.t, d.private, "lifecycle-fixture.json", f)
	return f
}

// lcCreateNode creates the lifecycle node with a name the global name index cannot
// have taken yet: an archived node keeps its name, so reusing the name of an
// archived fixture node would collide.
func (d *dockerTest) lcCreateNode() string {
	d.t.Helper()
	name := fmt.Sprintf("docker-lifecycle-%d", time.Now().UTC().UnixNano())
	node := d.api.request("POST", "/api/v1/admin/nodes", map[string]any{
		"name": name, "server_id": d.fixture.Servers[0], "enabled": true,
		"host": "127.0.0.1", "listen": "0.0.0.0", "port": lifecycleNodePort, "protocol": "vless",
		"stream_settings":   map[string]any{"network": "tcp", "security": "none"},
		"protocol_settings": lcValidProtocolSettings(),
		"client_settings":   map[string]any{},
	})
	d.t.Logf("lifecycle node %s created on the owned server %s, port %d", name, d.fixture.Servers[0], lifecycleNodePort)
	return node["id"].(string)
}

// lcRebuildFixtureNode replaces an archived lifecycle node. Only the node is new:
// the group, the subscription and both members are reused, so the fixed usernames
// the cases log in with stay valid and no extra fixture object appears.
func (d *dockerTest) lcRebuildFixtureNode(previous lifecycleFixture, path string) lifecycleFixture {
	d.t.Helper()
	updated := previous
	updated.Node = d.lcCreateNode()
	d.lcAttachGroupNode(previous.Group, updated.Node)
	writeConfig(d.t, d.private, filepath.Base(path), updated)
	d.t.Logf("the archived lifecycle node %s keeps its history; the fixture now uses node %s in group %s with members %s and %s",
		previous.Node, updated.Node, previous.Group, previous.Active, previous.Revoked)
	return updated
}

// lcAttachGroupNode puts the lifecycle node into the fixture group through the real
// preview + submit flow: a direct node_ids PATCH is refused while a subscription
// references the group (internal/panel/api/admin2.go:69-79), and that is exactly
// this case. The group must keep the shared node 20101, or the lifecycle member
// could not reach it.
func (d *dockerTest) lcAttachGroupNode(groupID, nodeID string) {
	d.t.Helper()
	want := []string{d.fixture.Nodes[0], nodeID}
	preview := d.api.request("POST", "/api/v1/admin/batches/preview", map[string]any{
		"operation": "update_node_group",
		"values":    map[string]any{"id": groupID, "patch": map[string]any{"node_ids": want}},
		"reason":    "lifecycle fixture: the previous node was archived, the group follows the new node",
	})
	submit := d.api.request("POST", "/api/v1/admin/batches", map[string]any{
		"preview_id": preview["id"].(string), "confirm": true})
	group := d.api.request("GET", "/api/v1/admin/node-groups/"+groupID, nil)
	var got []string
	if ids, ok := group["node_ids"].([]any); ok {
		for _, id := range ids {
			if s, ok := id.(string); ok {
				got = append(got, s)
			}
		}
	}
	sort.Strings(got)
	expected := append([]string{}, want...)
	sort.Strings(expected)
	if strings.Join(got, ",") != strings.Join(expected, ",") {
		d.t.Fatalf("node group %s lists %v after the preview batch %v, expected %v", groupID, got, submit["id"], expected)
	}
	d.t.Logf("node group %s now serves %v (batch %v, state=%v)", groupID, got, submit["id"], submit["state"])
}

// patchNode applies one node edit with the optimistic version the panel requires.
func (d *dockerTest) patchNode(nodeID string, values map[string]any) {
	d.t.Helper()
	values["version"] = d.api.request("GET", "/api/v1/admin/nodes/"+nodeID, nil)["version"]
	d.api.request("PATCH", "/api/v1/admin/nodes/"+nodeID, values)
}

// patchMember applies one direct member edit with the optimistic version the
// panel requires. It never touches the shared fixture member.
func (d *dockerTest) patchMember(memberID string, values map[string]any) {
	d.t.Helper()
	detail := d.api.request("GET", "/api/v1/admin/members/"+memberID, nil)
	member, ok := detail["member"].(map[string]any)
	if !ok || member["version"] == nil {
		d.t.Fatal("member detail missing versioned member object")
	}
	values["version"] = member["version"]
	d.api.request("PATCH", "/api/v1/admin/members/"+memberID, values)
}

// lifecycle runs one fixed action of the owned lifecycle script.
func (d *dockerTest) lifecycle(action string) string {
	d.t.Helper()
	out, err := d.tryLifecycle(action)
	if err != nil {
		d.t.Fatal(err)
	}
	return out
}

func (d *dockerTest) tryLifecycle(action string) (string, error) {
	return d.runOwned(lifecycleScript, action, 240*time.Second)
}

// lcTransfer reads the Agent state volume. The framed base64 is transfer data
// only: it is never logged and never embedded in an error message, and the
// offline reader below only ever queries batches and samples (never the meta
// table that holds the Agent token).
func (d *dockerTest) lcTransfer() (map[string][]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	args := append(d.sshArgs(), "root@192.168.5.203", "sh "+dockerRemote+"/"+lifecycleScript+" state-transfer")
	out, err := exec.CommandContext(ctx, "ssh", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("the owned state transfer failed (output withheld on purpose)")
	}
	return decodeStateDump(string(out))
}

// exportAs returns the subscription output of one member. It is the same real
// export path the member client uses; only the login differs.
func (d *dockerTest) exportAs(username string) map[string]any {
	d.t.Helper()
	jar, _ := cookiejar.New(nil)
	u := &platformAPI{t: d.t, base: d.api.base, client: &http.Client{Jar: jar, Transport: d.transport, Timeout: 15 * time.Second}}
	u.login(username, d.password)
	s := u.request("GET", "/api/v1/me/subscription", nil)
	req, err := http.NewRequest("GET", d.api.base+"/sub/"+s["token"].(string)+"?format=xray", nil)
	if err != nil {
		d.t.Fatal(err)
	}
	r, err := u.client.Do(req)
	if err != nil {
		d.t.Fatal("export request failed")
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		d.t.Fatalf("export status=%d", r.StatusCode)
	}
	var cfg map[string]any
	if json.NewDecoder(r.Body).Decode(&cfg) != nil {
		d.t.Fatal("export decode failed")
	}
	return cfg
}

// lcOutbound selects the client outbound that points at one owned node port.
func lcOutbound(t *testing.T, cfg map[string]any, port int) map[string]any {
	t.Helper()
	outbounds, ok := cfg["outbounds"].([]any)
	if !ok {
		t.Fatal("exported configuration has no outbounds")
	}
	for _, item := range outbounds {
		out, ok := item.(map[string]any)
		if !ok {
			continue
		}
		settings, _ := out["settings"].(map[string]any)
		vnext, _ := settings["vnext"].([]any)
		if len(vnext) == 0 {
			continue
		}
		server, _ := vnext[0].(map[string]any)
		if p, _ := server["port"].(float64); int(p) == port {
			return out
		}
	}
	t.Fatalf("the exported configuration has no outbound for the owned node port %d", port)
	return nil
}

// lcProxy is one local client core that reaches the acceptance stack through a
// single owned node. Every probe closes idle connections first so a cached
// connection can never mask a refusal.
type lcProxy struct {
	t      *testing.T
	tr     *http.Transport
	client *http.Client
}

func (d *dockerTest) proxyFor(username string, port int) *lcProxy {
	d.t.Helper()
	out := lcOutbound(d.t, d.exportAs(username), port)
	client, tr, _ := dockerProxy(d.t, map[string]any{"outbounds": []any{out}})
	return &lcProxy{t: d.t, tr: tr, client: client}
}

func (p *lcProxy) ok() bool {
	p.tr.CloseIdleConnections()
	return dockerFetch(p.client)
}

// lcText and lcInt run one acceptance query. Driver errors are never swallowed.
func (d *dockerTest) lcText(query string, args ...any) string {
	d.t.Helper()
	var out string
	if err := d.db.QueryRow(context.Background(), query, args...).Scan(&out); err != nil {
		d.t.Fatalf("acceptance query failed: %v", err)
	}
	return out
}

func (d *dockerTest) lcInt(query string, args ...any) int64 {
	d.t.Helper()
	var out int64
	if err := d.db.QueryRow(context.Background(), query, args...).Scan(&out); err != nil {
		d.t.Fatalf("acceptance query failed: %v", err)
	}
	return out
}

// lcBatchSummary reads one batch back from the panel and returns its per object
// dispatch counts.
func (d *dockerTest) lcBatchSummary(batchID string) (state string, counts map[string]int64) {
	d.t.Helper()
	data := d.api.request("GET", "/api/v1/admin/batches/"+batchID, nil)
	state, _ = data["state"].(string)
	summary, _ := data["summary"].(map[string]any)
	counts = map[string]int64{}
	for _, key := range []string{"succeeded", "failed", "timeout", "pending", "superseded"} {
		if v, ok := summary[key].(float64); ok {
			counts[key] = int64(v)
		}
	}
	return state, counts
}

// lcRevoke revokes one member through the real preview + submit flow.
func (d *dockerTest) lcRevoke(memberID string) (string, map[string]int64) {
	d.t.Helper()
	preview := d.api.request("POST", "/api/v1/admin/batches/preview", map[string]any{
		"operation": "set_active", "member_ids": []string{memberID},
		"values": map[string]any{"active": false}})
	submit := d.api.request("POST", "/api/v1/admin/batches", map[string]any{
		"preview_id": preview["id"].(string), "confirm": true})
	batchID := submit["id"].(string)
	counts := map[string]int64{}
	if summary, ok := submit["summary"].(map[string]any); ok {
		for _, key := range []string{"succeeded", "failed", "timeout", "pending", "superseded"} {
			if v, ok := summary[key].(float64); ok {
				counts[key] = int64(v)
			}
		}
	}
	return batchID, counts
}

// lcBatch is one batch the Agent still holds in its durable outbox, rebuilt from
// the very rows the uploader sends: id, sample count, total bytes, the bytes of
// one member (for exact ledger accounting) and the canonical payload digest the
// panel stores for idempotency.
type lcBatch struct {
	ID          string
	Samples     int64
	Bytes       int64
	MemberBytes int64
	Attempts    int64
	SHA         string
}

// lcPending returns the batches the owned Agent still has to deliver. It reads
// the Agent SQLite state from the owned volume, so it also works while the
// shipped container is stopped and replaced by a version-switch container.
// memberID selects the member whose bytes are accounted separately ("" skips it).
func (d *dockerTest) lcPending(memberID string) map[string]lcBatch {
	d.t.Helper()
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		pending, err := d.readPendingOnce(memberID)
		if err == nil {
			return pending
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	d.t.Fatalf("cannot read the owned agent1 outbox: %v", lastErr)
	return nil
}

func (d *dockerTest) readPendingOnce(memberID string) (map[string]lcBatch, error) {
	files, err := d.lcTransfer()
	if err != nil {
		return nil, err
	}
	dir := d.t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			return nil, err
		}
	}
	return parseAgentQueue(filepath.Join(dir, "agent.db"), memberID)
}

// parseAgentQueue rebuilds the uploader payload from the Agent SQLite file: the
// samples in seq order, the millisecond timestamps mapped back to UTC exactly as
// internal/agent/state does, and the canonical digest the panel recomputes on
// receipt (usagecalc.PayloadSHA256). A batch whose rows cannot be rebuilt is an
// error, never a silent zero.
func parseAgentQueue(dbPath, memberID string) (map[string]lcBatch, error) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`select batch_id, sample_count, attempts from batches where state='pending' order by batch_id`)
	if err != nil {
		return nil, fmt.Errorf("read pending batches: %w", err)
	}
	type header struct {
		id             string
		samples, tries int64
	}
	var pending []header
	for rows.Next() {
		var h header
		if err := rows.Scan(&h.id, &h.samples, &h.tries); err != nil {
			rows.Close()
			return nil, err
		}
		pending = append(pending, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	out := map[string]lcBatch{}
	for _, h := range pending {
		samples, err := lcReadBatchSamples(db, h.id)
		if err != nil {
			return nil, err
		}
		if int64(len(samples)) != h.samples {
			return nil, fmt.Errorf("batch %s holds %d sample row(s) but its header records %d", h.id, len(samples), h.samples)
		}
		batch := lcBatch{ID: h.id, Samples: int64(len(samples)), Attempts: h.tries}
		for _, s := range samples {
			batch.Bytes += s.UploadBytes + s.DownloadBytes
			if memberID != "" && s.MemberID == memberID {
				batch.MemberBytes += s.UploadBytes + s.DownloadBytes
			}
		}
		batch.SHA = usagecalc.PayloadSHA256(contracts.UsageBatch{
			SchemaVersion: contracts.SchemaVersion, BatchID: h.id, Samples: samples,
		})
		out[h.id] = batch
	}
	return out, nil
}

// lcReadBatchSamples mirrors the Agent's own loader (ORDER BY seq, millisecond
// timestamps in UTC), so the digest below is the one the Agent really uploads.
func lcReadBatchSamples(db *sql.DB, batchID string) ([]contracts.UsageSample, error) {
	rows, err := db.Query(`select node_id, member_id, core_generation, started_at, ended_at, upload, download
		from samples where batch_id=? order by seq`, batchID)
	if err != nil {
		return nil, fmt.Errorf("read samples of %s: %w", batchID, err)
	}
	defer rows.Close()
	var out []contracts.UsageSample
	for rows.Next() {
		var (
			sample             contracts.UsageSample
			startedMs, endedMs int64
		)
		if err := rows.Scan(&sample.NodeID, &sample.MemberID, &sample.CoreGeneration, &startedMs, &endedMs,
			&sample.UploadBytes, &sample.DownloadBytes); err != nil {
			return nil, err
		}
		sample.StartedAt = time.UnixMilli(startedMs).UTC()
		sample.EndedAt = time.UnixMilli(endedMs).UTC()
		out = append(out, sample)
	}
	return out, rows.Err()
}

// lcCredited reports how PostgreSQL recorded one Agent batch: the number of
// usage_batches rows (must be exactly one), its payload digest, its sample count
// and the bytes its usage_entries prove - in total and for one member.
type lcCredited struct {
	Rows        int64
	Samples     int64
	Bytes       int64
	MemberBytes int64
	SHA         string
}

func (d *dockerTest) lcCredited(agentID, batchID, memberID string) lcCredited {
	d.t.Helper()
	var out lcCredited
	out.Rows = d.lcInt(`select count(*) from usage_batches where agent_id=$1 and batch_id=$2`, agentID, batchID)
	if out.Rows == 1 {
		out.SHA = d.lcText(`select payload_sha256 from usage_batches where agent_id=$1 and batch_id=$2`, agentID, batchID)
		out.Samples = d.lcInt(`select sample_count from usage_batches where agent_id=$1 and batch_id=$2`, agentID, batchID)
	}
	out.Bytes = d.lcInt(`select coalesce(sum(up_bytes+down_bytes),0) from usage_entries where agent_id=$1 and batch_id=$2`, agentID, batchID)
	if memberID != "" {
		out.MemberBytes = d.lcInt(`select coalesce(sum(up_bytes+down_bytes),0) from usage_entries
			where agent_id=$1 and batch_id=$2 and member_id=$3`, agentID, batchID, memberID)
	}
	return out
}

// lcBatchesSince lists the batch ids PostgreSQL stored for one Agent after the
// accounting window started. It is the "nothing outside the captured set"
// evidence, so it reads ids instead of a bare count.
func (d *dockerTest) lcBatchesSince(agentID, since string) []string {
	d.t.Helper()
	rows, err := d.db.Query(context.Background(),
		`select batch_id from usage_batches where agent_id=$1 and received_at >= $2::timestamptz order by batch_id`,
		agentID, since)
	if err != nil {
		d.t.Fatalf("acceptance query failed: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			d.t.Fatalf("acceptance query failed: %v", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		d.t.Fatalf("acceptance query failed: %v", err)
	}
	return out
}

// lcBatchIDs lists the batch ids of one outbox snapshot in a stable order, so a
// failure message and the stability barrier always talk about the same set.
func lcBatchIDs(batches map[string]lcBatch) []string {
	ids := make([]string, 0, len(batches))
	for id := range batches {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// lcSameBatch reports whether two snapshots of one batch id describe the same
// payload and the same bytes. The attempt counter is ignored on purpose: it
// grows on every retry while PostgreSQL is unreachable, which is exactly what
// the injected outage causes.
func lcSameBatch(a, b lcBatch) bool {
	return a.ID == b.ID && a.Samples == b.Samples && a.Bytes == b.Bytes &&
		a.MemberBytes == b.MemberBytes && a.SHA == b.SHA
}

// lcSameBatches is the stability test of the outbox: same ids, same payload
// digest and same byte accounting, no additions and no removals.
func lcSameBatches(a, b map[string]lcBatch) bool {
	if len(a) != len(b) {
		return false
	}
	for id, left := range a {
		right, ok := b[id]
		if !ok || !lcSameBatch(left, right) {
			return false
		}
	}
	return true
}

// lcCarried returns the ids of the first snapshot that are missing from the
// second one or whose accounting changed. An empty result proves the barrier
// batches survived the switch unchanged. The second snapshot may legally be a
// superset: the switch can still flush the samples the core produced while it
// was being replaced, and those have to be included in the final accounting.
func lcCarried(before, after map[string]lcBatch) []string {
	var changed []string
	for _, id := range lcBatchIDs(before) {
		right, ok := after[id]
		if !ok || !lcSameBatch(before[id], right) {
			changed = append(changed, id)
		}
	}
	return changed
}

// lcWaitEmpty waits until the owned outbox reads empty twice in a row, more
// than one sampling interval apart (the Agent samples usage every 5s). A single
// empty read is not a drain: the sampler may still append the final sample of
// the traffic that was just generated.
func lcWaitEmpty(t *testing.T, d *dockerTest, budget, settle time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if len(d.lcPending("")) == 0 {
			time.Sleep(settle)
			if len(d.lcPending("")) == 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the owned outbox did not drain %s within %s: %v still pending",
				what, budget, lcBatchIDs(d.lcPending("")))
		}
		time.Sleep(2 * time.Second)
	}
}

// lcWaitStable waits until two consecutive snapshots of the owned outbox are
// identical. Without that barrier a snapshot taken while the Agent is still
// appending samples would silently under-report the retained bytes.
func lcWaitStable(t *testing.T, d *dockerTest, memberID string, budget, settle time.Duration, what string) map[string]lcBatch {
	t.Helper()
	deadline := time.Now().Add(budget)
	previous := d.lcPending(memberID)
	for {
		time.Sleep(settle)
		current := d.lcPending(memberID)
		if lcSameBatches(previous, current) {
			return current
		}
		if time.Now().After(deadline) {
			t.Fatalf("the owned outbox did not settle %s within %s: %v -> %v",
				what, budget, lcBatchIDs(previous), lcBatchIDs(current))
		}
		previous = current
	}
}

// lifecycleVersion extracts one version token from the `baseline` action.
func lifecycleVersion(raw, key string) (string, error) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, key) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, key))
		if len(fields) < 2 {
			return "", fmt.Errorf("%q carries no version: %q", key, line)
		}
		switch fields[0] {
		case "proxypanel-agent", "Xray":
			return fields[1], nil
		default:
			return "", fmt.Errorf("%q has an unexpected prefix: %q", key, line)
		}
	}
	return "", fmt.Errorf("missing %q in the lifecycle output", key)
}

// lifecycleDigest extracts the sha256 that the `baseline` action measured for
// the binary behind one key (the image binary or the staged file).
func lifecycleDigest(raw, key string) (string, error) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, key) {
			continue
		}
		idx := strings.LastIndex(line, " sha256=")
		if idx < 0 {
			return "", fmt.Errorf("%q carries no measured digest: %q", key, line)
		}
		digest := strings.TrimSpace(line[idx+len(" sha256="):])
		if len(digest) != 64 {
			return "", fmt.Errorf("%q has an invalid digest %q", key, digest)
		}
		return digest, nil
	}
	return "", fmt.Errorf("missing %q in the lifecycle output", key)
}

// lcBaseline is the measured version state. The shipped side is the binary that
// is really installed in the acceptance image (not an older build artifact).
type lcBaseline struct {
	ImageAgent, ImageAgentSHA   string
	ImageCore, ImageCoreSHA     string
	StagedAgent, StagedAgentSHA string
	StagedCore, StagedCoreSHA   string
}

func parseLifecycleBaseline(raw string) (lcBaseline, error) {
	var out lcBaseline
	var err error
	if out.ImageAgent, err = lifecycleVersion(raw, "image agent:"); err != nil {
		return out, err
	}
	if out.ImageAgentSHA, err = lifecycleDigest(raw, "image agent:"); err != nil {
		return out, err
	}
	if out.ImageCore, err = lifecycleVersion(raw, "image core:"); err != nil {
		return out, err
	}
	if out.ImageCoreSHA, err = lifecycleDigest(raw, "image core:"); err != nil {
		return out, err
	}
	if out.StagedAgent, err = lifecycleVersion(raw, "staged agent:"); err != nil {
		return out, err
	}
	if out.StagedAgentSHA, err = lifecycleDigest(raw, "staged agent:"); err != nil {
		return out, err
	}
	if out.StagedCore, err = lifecycleVersion(raw, "staged core:"); err != nil {
		return out, err
	}
	if out.StagedCoreSHA, err = lifecycleDigest(raw, "staged core:"); err != nil {
		return out, err
	}
	return out, nil
}

// lcAgentRow is one atomic observation of the panel's view of an owned Agent.
// last_heartbeat_at is written by the very same UPDATE that stores version and
// xray_version (internal/panel/store/ops.go:252-254), so a single row read either
// predates a switch as a whole or carries the heartbeat of the new process as a
// whole - it can never mix an old build with a new core.
type lcAgentRow struct {
	ID, Version, XrayVersion string
	Applied, Desired         int64
	ControlGeneration        int64
	Heartbeat                time.Time
}

func (d *dockerTest) lcAgent(serverID string) lcAgentRow {
	d.t.Helper()
	var row lcAgentRow
	var heartbeat sql.NullTime
	if err := d.db.QueryRow(context.Background(), `select id, version, xray_version,
		applied_revision, desired_revision, control_generation, last_heartbeat_at
		from agents where server_id=$1 and archived=false`, serverID).
		Scan(&row.ID, &row.Version, &row.XrayVersion, &row.Applied, &row.Desired,
			&row.ControlGeneration, &heartbeat); err != nil {
		d.t.Fatalf("read the owned agent row: %v", err)
	}
	if heartbeat.Valid {
		row.Heartbeat = heartbeat.Time
	}
	return row
}

// lcValidProtocolSettings returns a fresh, valid protocol_settings object. A new
// map is returned on purpose: patchNode adds the optimistic version to the map
// it receives, so sharing one literal between calls would leak a stale version
// and could keep a removed key alive through map aliasing.
func lcValidProtocolSettings() map[string]any {
	return map[string]any{"decryption": "none"}
}

// lcNodeState is the non-secret part of a node document this acceptance needs.
// It never carries credentials: only flags and JSON key types. Archived is the
// flag that decides whether the node has a core instance at all - a disabled node
// keeps its instance, an archived node leaves the snapshot.
type lcNodeState struct {
	Archived      bool
	Enabled       bool
	Port          int
	SettingsKeys  []string
	HasFallbacks  bool
	FallbacksType string
}

func (d *dockerTest) lcNode(nodeID string) lcNodeState {
	d.t.Helper()
	data := d.api.request("GET", "/api/v1/admin/nodes/"+nodeID, nil)
	state := lcNodeState{}
	state.Archived, _ = data["archived"].(bool)
	state.Enabled, _ = data["enabled"].(bool)
	if port, ok := data["port"].(float64); ok {
		state.Port = int(port)
	}
	settings, _ := data["protocol_settings"].(map[string]any)
	for key := range settings {
		state.SettingsKeys = append(state.SettingsKeys, key)
	}
	sort.Strings(state.SettingsKeys)
	if value, ok := settings["fallbacks"]; ok {
		state.HasFallbacks = true
		switch value.(type) {
		case string:
			state.FallbacksType = "string"
		case []any:
			state.FallbacksType = "array"
		case nil:
			state.FallbacksType = "null"
		case map[string]any:
			state.FallbacksType = "object"
		case float64:
			state.FallbacksType = "number"
		case bool:
			state.FallbacksType = "boolean"
		default:
			state.FallbacksType = "other"
		}
	}
	return state
}

// lcAssertNodeDocumentValid fails when the panel still carries the invalid
// fallbacks key (or the node is disabled): an Agent apply failure must never be
// misread as "the panel document is fine".
func (d *dockerTest) lcAssertNodeDocumentValid(nodeID string) lcNodeState {
	d.t.Helper()
	state := d.lcNode(nodeID)
	if state.HasFallbacks {
		d.t.Fatalf("the node document still carries protocol_settings.fallbacks (%s, keys=%v): the restore did not replace the object",
			state.FallbacksType, state.SettingsKeys)
	}
	if !state.Enabled {
		d.t.Fatalf("the lifecycle node is disabled while it is expected to serve (keys=%v)", state.SettingsKeys)
	}
	return state
}

// lcNodeOrError reads the same non-secret node facts straight from the panel
// database and returns an error instead of failing the test. Cleanups use it so
// they can report without aborting the remaining cleanups.
func (d *dockerTest) lcNodeOrError(nodeID string) (lcNodeState, error) {
	var enabled, port int
	var archived bool
	var fallbacksType, keys string
	var hasFallbacks bool
	err := d.db.QueryRow(context.Background(), `select case when enabled then 1 else 0 end, archived, port,
		coalesce(jsonb_typeof(protocol_settings->'fallbacks'),'missing'),
		(protocol_settings ? 'fallbacks'),
		coalesce((select string_agg(k, ',' order by k) from jsonb_object_keys(protocol_settings) k),'')
		from nodes where id=$1`, nodeID).Scan(&enabled, &archived, &port, &fallbacksType, &hasFallbacks, &keys)
	if err != nil {
		return lcNodeState{}, err
	}
	state := lcNodeState{Archived: archived, Enabled: enabled == 1, Port: port, HasFallbacks: hasFallbacks}
	if fallbacksType != "missing" {
		state.FallbacksType = fallbacksType
	}
	if keys != "" {
		state.SettingsKeys = strings.Split(keys, ",")
	}
	return state, nil
}

// lcNodeArchived reads the persisted archive flag of one node. found=false means
// the row is gone (the fixture file then points at nothing); a read error is a
// test failure, never "not archived".
func (d *dockerTest) lcNodeArchived(nodeID string) (archived, found bool) {
	d.t.Helper()
	var total int64
	if err := d.db.QueryRow(context.Background(),
		`select count(*), coalesce(bool_or(archived), false) from nodes where id=$1`, nodeID).
		Scan(&total, &archived); err != nil {
		d.t.Fatalf("read the archive flag of node %s: %v", nodeID, err)
	}
	return archived, total > 0
}

// parseListenerCount reads the deterministic output of the `listeners` lifecycle
// action: exactly one line "listeners container=<name> port=<port> count=<n>".
// Anything else (missing line, wrong container/port, duplicate, non-numeric) is
// rejected, so a silent change of the probe can never be read as "no listener".
func parseListenerCount(raw, container string, port int) (int, error) {
	want := fmt.Sprintf("listeners container=%s port=%d count=", container, port)
	count := -1
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, want) {
			return 0, fmt.Errorf("unexpected listener probe output %q", line)
		}
		if count >= 0 {
			return 0, fmt.Errorf("duplicate listener probe line %q", line)
		}
		value := strings.TrimPrefix(line, want)
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("listener probe count %q is not a non-negative integer", value)
		}
		count = n
	}
	if count < 0 {
		return 0, fmt.Errorf("missing listener probe line for %s port %d", container, port)
	}
	return count, nil
}

// lcListenerCount counts LISTEN sockets on one fixed port inside the owned
// container's network namespace. A host side SSH forward listens on the same
// port, so a TCP handshake against 127.0.0.1 proves nothing: only the kernel
// table inside the container can.
func (d *dockerTest) lcListenerCount(container string, port int) (int, error) {
	raw, err := d.tryLifecycle(fmt.Sprintf("listeners %s %d", container, port))
	if err != nil {
		return 0, err
	}
	return parseListenerCount(raw, container, port)
}

// lcWaitNoListener waits until the owned container no longer LISTENs on the port.
// It reports the last observed count so callers can fail with real evidence.
func lcWaitNoListener(t *testing.T, d *dockerTest, container string, port int, budget time.Duration) (bool, int, error) {
	t.Helper()
	deadline := time.Now().Add(budget)
	var last int
	for {
		count, err := d.lcListenerCount(container, port)
		if err != nil {
			if time.Now().After(deadline) {
				return false, last, err
			}
		} else {
			last = count
			if count == 0 {
				return true, 0, nil
			}
			if time.Now().After(deadline) {
				return false, count, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// lcConverged reports whether both owned Agents applied their desired revision.
func (d *dockerTest) lcConverged() bool {
	return d.lcInt(`select count(*) from agents where archived=false and applied_revision=desired_revision and applied_revision>0`) == 2
}

// lcLogSafeState logs only non-secret facts about a broken fixture: revision
// pointers, node flags, the JSON type of the rejected key, panel task/batch item
// states and the Agent receipt error text. It never prints instance payloads,
// configs, tokens or credentials (agents.instances carries the full Xray
// document, so the raw JSON is deliberately not selected).
func (d *dockerTest) lcLogSafeState(label string) {
	d.t.Helper()
	d.t.Logf("== safe state: %s ==", label)
	rows, err := d.db.Query(context.Background(), `select id, version, xray_version, desired_revision, applied_revision,
		coalesce(round(extract(epoch from (now()-last_heartbeat_at)))::int, -1) as heartbeat_age_s,
		jsonb_array_length(coalesce(instances, '[]'::jsonb)) as instance_count
		from agents where archived=false order by id`)
	if err != nil {
		d.t.Logf("agents query failed: %v", err)
	} else {
		for rows.Next() {
			var id, version, xray string
			var desired, applied, heartbeatAge, instanceCount int64
			if err := rows.Scan(&id, &version, &xray, &desired, &applied, &heartbeatAge, &instanceCount); err != nil {
				d.t.Logf("agents scan failed: %v", err)
				break
			}
			d.t.Logf("agent %s version=%s core=%q desired=%d applied=%d heartbeat_age=%ds instances=%d",
				id, version, xray, desired, applied, heartbeatAge, instanceCount)
		}
		rows.Close()
	}
	instances, err := d.db.Query(context.Background(), `select node_id, state, config_revision, generation,
		left(coalesce(last_error,''), 160) from core_instances order by node_id`)
	if err != nil {
		d.t.Logf("core_instances query failed: %v", err)
	} else {
		for instances.Next() {
			var nodeID, state, lastError string
			var configRevision, generation int64
			if err := instances.Scan(&nodeID, &state, &configRevision, &generation, &lastError); err != nil {
				d.t.Logf("core_instances scan failed: %v", err)
				break
			}
			d.t.Logf("instance node=%s state=%s config_revision=%d generation=%d error=%q",
				nodeID, state, configRevision, generation, lastError)
		}
		instances.Close()
	}
	// agent_receipts carries received_at (migrations/0001_init.sql:415), not
	// created_at: only the state and a truncated error text are read, never the
	// payload (it holds the full report of the Agent).
	receipts, err := d.db.Query(context.Background(), `select agent_id, revision, state,
		left(coalesce(payload->'results'->0->>'error',''), 200) from agent_receipts order by received_at desc limit 3`)
	if err != nil {
		d.t.Logf("agent_receipts query failed: %v", err)
	} else {
		for receipts.Next() {
			var agentID, state, firstError string
			var revision int64
			if err := receipts.Scan(&agentID, &revision, &state, &firstError); err != nil {
				d.t.Logf("agent_receipts scan failed: %v", err)
				break
			}
			d.t.Logf("receipt agent=%s revision=%d state=%s first_error=%q", agentID, revision, state, firstError)
		}
		receipts.Close()
	}
	tasks, err := d.db.Query(context.Background(), `select id, agent_id, state, attempts, max_attempts,
		left(last_error, 160) from tasks where batch_id<>'' order by updated_at desc limit 5`)
	if err != nil {
		d.t.Logf("tasks query failed: %v", err)
	} else {
		for tasks.Next() {
			var id, agentID, state, lastError string
			var attempts, maxAttempts int64
			if err := tasks.Scan(&id, &agentID, &state, &attempts, &maxAttempts, &lastError); err != nil {
				d.t.Logf("tasks scan failed: %v", err)
				break
			}
			d.t.Logf("task %s agent=%s state=%s attempts=%d/%d error=%q", id, agentID, state, attempts, maxAttempts, lastError)
		}
		tasks.Close()
	}
	items, err := d.db.Query(context.Background(), `select id, agent_id, state, revision, left(error, 160)
		from batch_items order by updated_at desc limit 5`)
	if err != nil {
		d.t.Logf("batch_items query failed: %v", err)
	} else {
		for items.Next() {
			var id, agentID, state, itemError string
			var revision int64
			if err := items.Scan(&id, &agentID, &state, &revision, &itemError); err != nil {
				d.t.Logf("batch_items scan failed: %v", err)
				break
			}
			d.t.Logf("batch_item %s agent=%s state=%s revision=%d error=%q", id, agentID, state, revision, itemError)
		}
		items.Close()
	}
}

// lcConverge waits for applied==desired on both owned Agents. A skipped instance
// re-arms its own failure backoff every snapshot
// (internal/agent/supervisor/supervisor.go:592-601 counts StateDeferred as a
// failure and :1114-1118 doubles the delay up to 320s), so a recovery after a
// rejected document can be postponed forever. When the budget runs out this
// helper restarts the owned Agent containers - which drops the in-memory backoff
// and never touches the SQLite state volume - and reports whether that was what
// made the stack converge.
func lcConverge(t *testing.T, d *dockerTest, budget time.Duration) (converged, restarted bool) {
	t.Helper()
	if waitConvergeWithin(d, budget) {
		return true, false
	}
	d.lcLogSafeState("convergence timed out")
	t.Log("the Agents did not reach the desired revision; restarting the owned Agent containers to clear the in-memory failure backoff (state volumes untouched)")
	if _, err := d.tryRemote("restart agent1"); err != nil {
		t.Errorf("restart the owned agent1 container: %v", err)
	}
	if _, err := d.tryRemote("restart agent2"); err != nil {
		t.Errorf("restart the owned agent2 container: %v", err)
	}
	if waitConvergeWithin(d, 90*time.Second) {
		return true, true
	}
	return false, true
}

func waitConvergeWithin(d *dockerTest, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		if d.lcConverged() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// lcPrepare brings the stack into a known state: both Agents of the fixture run,
// the shared member is active and the lifecycle node serves with a clean document.
// A fixture whose node was archived by the previous quiesce is rebuilt first (see
// lifecycleFixture): PATCH cannot un-archive a node, and re-enabling a disabled
// node would not free the port either.
func lcPrepare(t *testing.T, d *dockerTest) lifecycleFixture {
	t.Helper()
	d.remote("agent1")
	d.remote("agent2")
	d.remote("start agent1")
	d.remote("start agent2")
	d.memberPatch(map[string]any{"active": true, "proxy_enabled": true, "expires_at": nil})
	f := d.lifecycleFixture()
	state, err := d.lcNodeOrError(f.Node)
	if err != nil {
		t.Fatalf("the lifecycle node %s could not be read after the fixture step: %v", f.Node, err)
	}
	if state.Archived {
		t.Fatalf("the lifecycle node %s is archived although the fixture was prepared", f.Node)
	}
	d.patchNode(f.Node, map[string]any{"enabled": true, "protocol_settings": lcValidProtocolSettings()})
	d.lcAssertNodeDocumentValid(f.Node)
	d.patchMember(f.Active, map[string]any{"active": true, "proxy_enabled": true, "expires_at": nil})
	d.patchMember(f.Revoked, map[string]any{"active": true, "proxy_enabled": true, "expires_at": nil})
	d.applied()
	lcWaitListening(t, d, "agent1", lifecycleNodePort, 90*time.Second)
	return f
}

// lcWaitListening waits until the owned agent1 container really LISTENs on the
// fixture port. A rebuilt node (new id, new core instance) starts its core a
// moment after the applied revision, and a client probe must not race that start.
// The kernel table is the only trustworthy signal here: core_instances.state is
// written from Agent receipts (applied/unchanged/rollback_applied/deferred) and
// never tracks the live Xray process, so waiting on it can only time out.
// Listening proves the core is up, not that a member is authorized: callers still
// assert a successful proxy round trip.
func lcWaitListening(t *testing.T, d *dockerTest, container string, port int, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	var last int
	var lastErr error
	for {
		count, err := d.lcListenerCount(container, port)
		if err != nil {
			lastErr = err
		} else {
			lastErr = nil
			last = count
			if count > 0 {
				return
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	d.lcLogSafeState("the lifecycle instance never listened")
	if lastErr != nil {
		t.Fatalf("%s never reported a LISTEN socket on %d within %s; the last probe failed: %v", container, port, budget, lastErr)
	}
	t.Fatalf("%s holds no LISTEN socket on %d after %s (last count %d): the rebuilt lifecycle node did not serve its port", container, port, budget, last)
}

// lcQuiesce takes the lifecycle fixture out of service: both members inactive and
// the node archived, so the Agent removes the core instance and 20105 is free
// again. Disabling alone does not: BuildSnapshot keeps an instance for every non
// archived node, so a disabled node without members still runs a listening core
// (docs/verification/docker-lifecycle.md sections 5 and 12.1). It runs as a
// cleanup so no case leaves 20105 behind for the client matrix, it never adds a
// fixture object, and a repeat run archives nothing twice - the next lcPrepare
// rebuilds the node.
func lcQuiesce(t *testing.T, d *dockerTest, f lifecycleFixture) {
	t.Helper()
	d.patchMember(f.Active, map[string]any{"active": false})
	d.patchMember(f.Revoked, map[string]any{"active": false})
	if err := lcArchiveNode(t, d, f.Node); err != nil {
		d.lcLogSafeState("quiesce: the lifecycle node could not be archived")
		t.Errorf("the lifecycle fixture could not be quiesced: %v", err)
		return
	}
	converged, restarted := lcConverge(t, d, 30*time.Second)
	if !converged {
		d.lcLogSafeState("quiesce did not converge")
		t.Errorf("the lifecycle fixture could not be quiesced; node %s may still be served on its port", f.Node)
		return
	}
	if restarted {
		t.Log("the fixture converged only after restarting the owned Agents (failure backoff was re-armed)")
	}
	if state, err := d.lcNodeOrError(f.Node); err != nil {
		t.Errorf("the lifecycle node state could not be read after the quiesce: %v", err)
	} else if !state.Archived || state.Enabled {
		t.Errorf("the lifecycle node is not archived after the quiesce (archived=%t enabled=%t)", state.Archived, state.Enabled)
	}
	// Panel bookkeeping is reported, not used as a pass criterion: the kernel
	// table inside the container is the authoritative answer to "is the port
	// still served". An archived node whose instance row lags behind must not turn
	// a clean quiesce into a false red, and a still listening port must not be
	// hidden by a stale "stopped" row.
	running := d.lcInt(`select count(*) from core_instances where node_id=$1 and state='running'`, f.Node)
	ok, count, err := lcWaitNoListener(t, d, "agent1", lifecycleNodePort, 60*time.Second)
	if err != nil {
		t.Errorf("the listener probe inside the owned agent1 container failed: %v", err)
		return
	}
	if !ok {
		t.Errorf("agent1 still LISTENs on %d (%d socket(s)) after the quiesce (panel reports %d running instance(s)): a later case would collide",
			lifecycleNodePort, count, running)
		return
	}
	if running != 0 {
		t.Logf("note: no LISTEN socket remains on %d but the panel still reports %d running core instance(s); the Agent receipt was not applied yet", lifecycleNodePort, running)
	}
	t.Logf("lifecycle fixture quiesced: node archived, both members inactive, agent1 holds no LISTEN socket on %d",
		lifecycleNodePort)
}

// lcArchiveNode takes the lifecycle node out of the desired snapshot with the
// protected DELETE endpoint and proves that the archive really landed.
// DeleteNode archives through UpdateNode (internal/panel/store/resources.go:569-576),
// so the node row, its usage history, audit rows and ledger entries survive and
// only the snapshot entry disappears - which is what stops the instance and frees
// the port. Repeating the call on an archived node is a no-op, so the recovery
// stays idempotent.
func lcArchiveNode(t *testing.T, d *dockerTest, nodeID string) error {
	t.Helper()
	archived, found := d.lcNodeArchived(nodeID)
	if !found {
		return fmt.Errorf("the lifecycle node %s does not exist", nodeID)
	}
	if archived {
		t.Logf("lifecycle node %s is already archived: nothing to archive", nodeID)
		return nil
	}
	d.api.request("DELETE", "/api/v1/admin/nodes/"+nodeID, nil)
	state, err := d.lcNodeOrError(nodeID)
	if err != nil {
		return fmt.Errorf("read the archived lifecycle node %s back: %w", nodeID, err)
	}
	if !state.Archived || state.Enabled {
		return fmt.Errorf("the lifecycle node %s is not archived after DELETE (archived=%t enabled=%t)",
			nodeID, state.Archived, state.Enabled)
	}
	t.Logf("lifecycle node %s archived through DELETE /api/v1/admin/nodes/%s (usage history, audit rows and ledger entries kept)",
		nodeID, nodeID)
	return nil
}

// lcRevokeAndProve revokes one member for real and returns a proxy built from the
// credentials it had while it was still authorized, so a later "still denied"
// assertion really retries a retired credential.
func lcRevokeAndProve(t *testing.T, d *dockerTest, memberID string, port int, username string) *lcProxy {
	t.Helper()
	proxy := d.proxyFor(username, port)
	if !proxy.ok() {
		t.Fatalf("the lifecycle member %s is not authorized on the owned node %d before the revocation", username, port)
	}
	revokedAt := time.Now()
	d.lcRevoke(memberID)
	waitFor(t, 60*time.Second, "the revoked lifecycle member is refused on the owned node", func() bool { return !proxy.ok() })
	t.Logf("real revocation of %s on node %d took %.3fs", username, port, time.Since(revokedAt).Seconds())
	d.applied()
	return proxy
}

// TestDockerBatchPartialFailure covers A02: a real two node batch with one Agent
// down stays partially applied and visible, the item that never reached its
// Agent surfaces as a retryable failure, the recovered Agent applies the
// revocation, and retrying that same task through the panel must converge the
// original batch with what the nodes really do.
func TestDockerBatchPartialFailure(t *testing.T) {
	d := dockerSetup(t)
	// Restore order matters: t.Cleanup runs last-in-first-out, so the member
	// re-enable is registered first and the container restore last. A failing
	// assertion anywhere below therefore still restarts the owned agents.
	t.Cleanup(func() {
		d.memberPatch(map[string]any{"active": true, "proxy_enabled": true})
	})
	t.Cleanup(func() {
		if _, err := d.tryRemote("start agent2"); err != nil {
			t.Errorf("restore the owned agent2 container: %v", err)
		}
		if _, err := d.tryRemote("start agent1"); err != nil {
			t.Errorf("restore the owned agent1 container: %v", err)
		}
	})
	d.remote("agent1")
	d.remote("agent2")
	d.remote("start agent1")
	d.remote("start agent2")
	d.memberPatch(map[string]any{"active": true, "proxy_enabled": true, "expires_at": nil})
	d.applied()

	node1 := d.proxyFor("docker-alice", 20101)
	node2 := d.proxyFor("docker-alice", 20201)
	if !node1.ok() || !node2.ok() {
		t.Fatal("both owned nodes must serve the shared member before the batch")
	}
	ledgerBefore := d.ledger()

	t.Log("owned failure: stopping the acceptance container agent2")
	d.remote("stop agent2")
	waitFor(t, 30*time.Second, "the owned node of agent2 stops answering", func() bool { return !node2.ok() })

	batchID, submitted := d.lcRevoke(d.fixture.Member)
	t.Logf("batch %s submitted with one Agent down: %v", batchID, submitted)
	revokedAt := time.Now()
	waitFor(t, 60*time.Second, "the live node revokes the member", func() bool { return !node1.ok() })
	t.Logf("the reachable node revoked the member in %.3fs", time.Since(revokedAt).Seconds())

	// Partial success must be visible per object, not only in the HTTP response.
	var succeeded, outstanding int64
	waitFor(t, 45*time.Second, "the batch shows one applied and one outstanding item", func() bool {
		state, counts := d.lcBatchSummary(batchID)
		if state == "" {
			return false
		}
		succeeded, outstanding = counts["succeeded"], counts["pending"]+counts["timeout"]+counts["failed"]
		return succeeded >= 1 && outstanding >= 1
	})
	t.Logf("partial dispatch visible in the panel: %d succeeded, %d outstanding", succeeded, outstanding)

	// The item that never reached its Agent must become an explicit, retryable
	// failure with its reason instead of staying "pending" forever.
	waitFor(t, 240*time.Second, "the unreachable Agent's dispatch is swept to timeout", func() bool {
		return d.lcInt(`select count(*) from batch_items where batch_id=$1 and state='timeout'`, batchID) >= 1 &&
			d.lcInt(`select count(*) from tasks where batch_id=$1 and state='timeout' and last_error<>''`, batchID) >= 1
	})
	reason := d.lcText(`select coalesce(max(error),'') from batch_items where batch_id=$1 and state='timeout'`, batchID)
	state, counts := d.lcBatchSummary(batchID)
	t.Logf("batch %s state=%s timeout=%d (%s)", batchID, state, counts["timeout"], reason)
	taskID := d.lcText(`select id from tasks where batch_id=$1 and state='timeout' order by id limit 1`, batchID)

	// Recovery: the Agent applies the revocation it missed. The swept item stays
	// a recorded timeout (a delivery failure is never rewritten to success), so
	// the convergence of the runtime is asserted through real traffic.
	t.Log("recovery: starting the acceptance container agent2 again")
	d.remote("start agent2")
	waitFor(t, 180*time.Second, "the recovered Agent reaches the desired revision", func() bool {
		return d.lcInt(`select count(*) from agents where archived=false and applied_revision=desired_revision and applied_revision>0`) == 2
	})
	waitFor(t, 60*time.Second, "the recovered node really refuses the retired credential", func() bool { return !node2.ok() })
	if active := d.lcInt(`select count(*) from credentials where member_id=$1 and state='active'`, d.fixture.Member); active != 0 {
		t.Fatalf("%d credential(s) of the revoked member are still active", active)
	}
	if active := d.lcInt(`select count(*) from members where id=$1 and (active or proxy_enabled)`, d.fixture.Member); active != 0 {
		t.Fatal("the revoked member is still active in the database")
	}
	t.Log("recovered node and database agree: the member is revoked everywhere, the missed dispatch stays a visible timeout")

	// Retry the *same* timed out task through the panel. The retried dispatch has
	// to converge in the original batch and in the task record - repeating the
	// business operation in a new batch would not prove anything.
	retried := d.api.request("POST", "/api/v1/admin/tasks/"+taskID+"/retry", map[string]any{})
	if got, _ := retried["state"].(string); got != "pending" {
		t.Fatalf("the retried task is %q, expected pending", got)
	}
	if attempts, _ := retried["attempts"].(float64); attempts < 1 {
		t.Fatalf("the retried task kept %v attempt(s)", retried["attempts"])
	}
	t.Logf("task %s retried through the panel: state=pending attempts=%v", taskID, retried["attempts"])
	// Required behaviour: the retried dispatch re-opens the original batch item,
	// the item reaches the same terminal success as the task, and the recovered
	// node is consistent with it.
	waitFor(t, 180*time.Second, "the retried task and its original batch item converge", func() bool {
		return d.lcInt(`select count(*) from tasks where id=$1 and state='succeeded'`, taskID) == 1 &&
			d.lcInt(`select count(*) from batch_items where batch_id=$1 and task_id=$2 and state='succeeded'`, batchID, taskID) == 1
	})
	if remaining := d.lcInt(`select count(*) from batch_items where batch_id=$1 and state in ('pending','dispatched','failed','timeout')`, batchID); remaining != 0 {
		t.Fatalf("the original batch still reports %d unresolved item(s) after the retry", remaining)
	}
	// The recovered node must still refuse the retired credential after the retry.
	// The probe deliberately reuses the *same* client core and the *same* retired
	// credential built before the revocation: logging in again to fetch a fresh
	// export would fail with 403 (the member is inactive) and prove nothing about
	// whether the node re-granted the withdrawn access.
	if node2.ok() {
		t.Fatal("the retried dispatch re-granted the revoked member on the recovered node")
	}
	t.Log("the retired credential of the recovered node is still refused after the task retry")
	if after := d.ledger(); after < ledgerBefore {
		t.Fatalf("the usage ledger regressed from %d to %d across the batch", ledgerBefore, after)
	}

	// Leave the shared fixture usable for the other cases.
	d.memberPatch(map[string]any{"active": true, "proxy_enabled": true})
	d.applied()
	t.Log("partial failure, visible timeout, real task retry, recovery and node consistency finished")
}

// TestDockerConfigSafety covers A07: a document the real core rejects is injected
// through the panel for one node of Agent 1 while the same Agent serves a second
// node. The invalid document is never applied, the other node is unaffected, and
// the technical rollback does not restore a revoked member.
func TestDockerConfigSafety(t *testing.T) {
	d := dockerSetup(t)
	f := lcPrepare(t, d)
	// Registered before the injection: the invalid document and the disabled
	// node must be repaired even when an assertion fails.
	t.Cleanup(func() {
		d.patchNode(f.Node, map[string]any{"enabled": true,
			"protocol_settings": map[string]any{"decryption": "none"}})
		lcQuiesce(t, d, f)
	})

	otherNode := d.proxyFor("docker-lifecycle-active", 20101)
	lifecycleNode := d.proxyFor("docker-lifecycle-active", lifecycleNodePort)
	if !otherNode.ok() || !lifecycleNode.ok() {
		t.Fatal("both nodes of Agent 1 must serve the lifecycle member before the invalid edit")
	}
	retired := lcRevokeAndProve(t, d, f.Revoked, lifecycleNodePort, "docker-lifecycle-revoked")
	ledgerBefore := d.ledger()
	appliedBefore := d.lcAgent(d.fixture.Servers[0]).Applied

	// The panel accepts this administrator supplied protocol setting; the real
	// core rejects a string where a fallback array is required, so the Agent's
	// own `xray run -test` must refuse it.
	d.patchNode(f.Node, map[string]any{
		"protocol_settings": map[string]any{"decryption": "none", "fallbacks": "not-an-array"}})
	t.Log("invalid Xray document dispatched for the lifecycle node; the real core must reject it")

	waitFor(t, 120*time.Second, "the invalid document is not applied as a new revision", func() bool {
		current := d.lcAgent(d.fixture.Servers[0])
		return current.Desired > current.Applied
	})
	waitFor(t, 120*time.Second, "the Agent reports the rejected document and keeps its last good one", func() bool {
		logs, err := d.tryRemote("logs agent1")
		return err == nil && strings.Contains(logs, "applied last known good technical config with current authorization")
	})
	if mounts := d.lifecycle("mount-check agent1"); !strings.Contains(mounts, "ppaccept-20261021-agent1=/state") {
		t.Fatalf("the shipped agent does not mount the owned state volume: %s", strings.TrimSpace(mounts))
	}

	for i := 0; i < 3; i++ {
		if !otherNode.ok() {
			t.Fatal("the second node of the same Agent stopped serving: an invalid document affected another node")
		}
		time.Sleep(time.Second)
	}
	waitFor(t, 120*time.Second, "the affected node serves again from its last good document", func() bool {
		return lifecycleNode.ok()
	})
	if retired.ok() {
		t.Fatal("a withdrawn credential was restored by the technical rollback")
	}
	if active := d.lcInt(`select count(*) from credentials where member_id=$1 and state='active'`, f.Revoked); active != 0 {
		t.Fatalf("%d credential(s) of the revoked member are active in the database", active)
	}
	t.Log("invalid document rejected by the real core; other node unaffected; rollback did not restore revoked access")

	// Restore a clean document (the whole protocol_settings object is replaced by
	// the PATCH, so the rejected fallbacks key must be gone) and prove it: an
	// apply failure from here on is an Agent-side problem, never a stale panel
	// document.
	d.patchNode(f.Node, map[string]any{"enabled": true, "protocol_settings": lcValidProtocolSettings()})
	if state := d.lcAssertNodeDocumentValid(f.Node); state.FallbacksType != "" {
		t.Fatalf("the restored document still reports fallbacks=%s", state.FallbacksType)
	}
	t.Log("the panel document was restored without protocol_settings.fallbacks")
	converged, restartedBackoff := lcConverge(t, d, 180*time.Second)
	if !converged {
		d.lcLogSafeState("apply after the valid document")
		t.Fatalf("the Agents never applied the valid document (applied!=desired after the restart recovery)")
	}
	if restartedBackoff {
		d.lcLogSafeState("apply after the valid document")
		t.Fatalf("the Agents converged only after restarting the owned Agent containers: a skipped instance kept re-arming its own failure backoff " +
			"(internal/agent/supervisor/supervisor.go:592-601 treats StateDeferred as a new failure and :1114-1118 grows the delay to 320s), " +
			"so a rejected document locked the node out for longer than the 60s revocation SLA; the panel document itself was valid")
	}
	after := d.lcAgent(d.fixture.Servers[0])
	if after.Applied < appliedBefore {
		t.Fatalf("the applied revision regressed from %d to %d", appliedBefore, after.Applied)
	}
	if after.Applied != after.Desired {
		t.Fatalf("the Agent did not converge after the valid document: applied=%d desired=%d", after.Applied, after.Desired)
	}
	if after := d.ledger(); after < ledgerBefore {
		t.Fatalf("the usage ledger regressed from %d to %d across the invalid document", ledgerBefore, after)
	}
	if !lifecycleNode.ok() || !otherNode.ok() {
		t.Fatal("traffic did not resume on both nodes after the valid document")
	}
	t.Log("valid document applied again; both nodes serve; ledger intact")
}

// lcExpect is the exact state the panel has to report for one switch step. Every
// field is checked on the same row, and the heartbeat has to be strictly newer
// than the snapshot taken right before the action: the first core switch step
// keeps the core version 26.3.27, so matching that string alone would accept a
// heartbeat of the old 0.1.0 process that predates the whole switch.
type lcExpect struct {
	AgentVersion string
	CoreVersion  string
	After        time.Time
}

// lcPhaseReached reports why one panel observation does not yet prove that the
// action behind a ladder step really happened. It is pure, so the negative cases
// (stale heartbeat, old Agent build, old core, unconverged revisions) are
// regression tested without a server.
func lcPhaseReached(now lcAgentRow, want lcExpect) error {
	if want.AgentVersion == "" || want.CoreVersion == "" {
		return fmt.Errorf("the expectation is incomplete (agent %q, core %q)", want.AgentVersion, want.CoreVersion)
	}
	if want.After.IsZero() {
		return fmt.Errorf("no pre-action heartbeat snapshot was taken")
	}
	if now.Version != want.AgentVersion {
		return fmt.Errorf("the panel reports Agent %q, expected %q", now.Version, want.AgentVersion)
	}
	if now.XrayVersion != want.CoreVersion {
		return fmt.Errorf("the panel reports core %q, expected %q", now.XrayVersion, want.CoreVersion)
	}
	if now.Heartbeat.IsZero() {
		return fmt.Errorf("the Agent row carries no heartbeat at all")
	}
	if !now.Heartbeat.After(want.After) {
		return fmt.Errorf("the newest heartbeat %s is not newer than the pre-action snapshot %s: the whole observation predates the switch",
			now.Heartbeat.UTC().Format(time.RFC3339Nano), want.After.UTC().Format(time.RFC3339Nano))
	}
	if now.Applied <= 0 || now.Applied != now.Desired {
		return fmt.Errorf("applied=%d desired=%d: the new process did not converge", now.Applied, now.Desired)
	}
	return nil
}

// lcBothServe is the pure gate behind the two node barrier: one healthy node of
// the Agent is not enough, the same probe iteration has to see both of them
// answer (the switch replaces the Agent container, so it is not zero downtime,
// but a single surviving node must never close the step).
func lcBothServe(first, second bool) error {
	switch {
	case first && second:
		return nil
	case !first && !second:
		return fmt.Errorf("neither node answered")
	case !first:
		return fmt.Errorf("the first node (%d) did not answer", 20101)
	default:
		return fmt.Errorf("the second node (%d) did not answer", lifecycleNodePort)
	}
}

// lcWaitBothServe waits for one probe iteration in which both nodes of the same
// Agent really answer. Probing them one after the other in different iterations
// (the previous shape) let a half restarted Agent pass: one core was already
// listening while the other one had not started yet.
func lcWaitBothServe(t *testing.T, step string, node1, node2 *lcProxy, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		first, second := node1.ok(), node2.ok()
		err := lcBothServe(first, second)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("both nodes of the same Agent never answered in the same probe within %s during %s: %v (first=%v second=%v)",
				budget, step, err, first, second)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// lcVersionLadder runs the assertions for one switch step: the Agent row keeps
// its identity and control generation, the observation carries the expected
// Agent build and core version with a heartbeat that is newer than the snapshot
// taken before the action, the Agent converged, the outbox drains, the ledger
// never regresses, the retired credential stays refused and both nodes of the
// same Agent answer in one probe iteration.
func lcVersionLadder(t *testing.T, d *dockerTest, step string, want lcExpect, before lcAgentRow, node1, node2, retired *lcProxy, ledgerBefore int64) lcAgentRow {
	t.Helper()
	if before.Heartbeat.IsZero() {
		t.Fatalf("the pre-action snapshot for %s carries no heartbeat: the step cannot prove that it observed the new process", step)
	}
	deadline := time.Now().Add(240 * time.Second)
	var after lcAgentRow
	var lastErr error
	for {
		current := d.lcAgent(d.fixture.Servers[0])
		if current.ID != before.ID {
			t.Fatalf("the Agent identity changed during %s: %s -> %s", step, before.ID, current.ID)
		}
		if current.ControlGeneration != before.ControlGeneration {
			t.Fatalf("the control generation changed during %s: %d -> %d", step, before.ControlGeneration, current.ControlGeneration)
		}
		if err := lcPhaseReached(current, want); err == nil {
			after = current
			break
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			d.lcLogSafeState(step)
			t.Fatalf("%s never became the newest panel observation within 240s: %v (last row agent=%s version=%q core=%q applied=%d/%d heartbeat=%s, pre-action heartbeat=%s)",
				step, lastErr, current.ID, current.Version, current.XrayVersion, current.Applied, current.Desired,
				current.Heartbeat.UTC().Format(time.RFC3339Nano), before.Heartbeat.UTC().Format(time.RFC3339Nano))
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("%s: agent=%s version=%s core=%q applied=%d/%d heartbeat=%s",
		step, after.ID, after.Version, after.XrayVersion, after.Applied, after.Desired, after.Heartbeat.UTC().Format(time.RFC3339Nano))
	// A batch that is still draining right after a switch is not a lost batch:
	// wait for the outbox to empty instead of asserting it at one instant.
	waitFor(t, 120*time.Second, "the outbox drains during "+step, func() bool { return len(d.lcPending("")) == 0 })
	if ledgerAfter := d.ledger(); ledgerAfter < ledgerBefore {
		t.Fatalf("the usage ledger regressed during %s: %d -> %d", step, ledgerBefore, ledgerAfter)
	}
	if retired.ok() {
		t.Fatalf("%s restored a withdrawn credential", step)
	}
	lcWaitBothServe(t, step, node1, node2, 180*time.Second)
	return after
}

// lcQueueDurability proves the switch preserves the durable outbox with an exact
// per batch accounting. The order is fixed:
//  1. drain the outbox while PostgreSQL still answers and record the baseline
//     (member ledger, stored batch count) plus the PostgreSQL clock, which opens
//     the accounting window;
//  2. stop the owned PostgreSQL container;
//  3. generate a fixed amount of real traffic through both nodes of the member -
//     every probe must really succeed, a refused probe would produce no bytes
//     and would make the whole accounting vacuous;
//  4. wait for a stable snapshot: two identical reads of the Agent SQLite state
//     more than one sampling interval apart, so the final sample of that traffic
//     is already captured and nothing is still being appended;
//  5. switch while those batches are pending;
//  6. take the final stable snapshot *after* the switch, so it contains every
//     batch the switch itself produced; the barrier batches must all still be
//     there with the same payload digest and the same bytes;
//  7. restore PostgreSQL and drain again;
//  8. per batch id: exactly one usage_batches row whose stored payload_sha256
//     equals the digest recomputed from the Agent rows (the one the uploader
//     sends), with the same sample count, the same bytes and the same member
//     bytes;
//  9. no usage batch outside that captured set reached PostgreSQL inside the
//     window, and the member ledger grew by exactly the sum of the captured
//     member bytes - no lost batch and no double credit.
//
// The clients of both nodes are built by the caller *before* the outage:
// proxyFor logs in and exports the subscription, so fetching the configuration
// after PostgreSQL was stopped would fail, and the withdrawn member cannot be
// logged in again at all.
// The return value is the panel observation taken right before the switch, so the
// caller's ladder step proves that the newest heartbeat is newer than that
// snapshot instead of reusing an older baseline from a previous step.
func lcQueueDurability(t *testing.T, d *dockerTest, f lifecycleFixture, switchAction string, node1, node2 *lcProxy) lcAgentRow {
	t.Helper()
	if node1 == nil || node2 == nil {
		t.Fatal("the queue durability case needs both node clients, prepared while the panel still answers")
	}
	postgresRestored := false
	t.Cleanup(func() {
		if postgresRestored {
			return
		}
		if _, err := d.tryRemote("start postgres"); err != nil {
			t.Errorf("restore the owned PostgreSQL container: %v", err)
		}
	})
	agentID := d.lcAgent(d.fixture.Servers[0]).ID
	ledgerQuery := `select coalesce(sum(up_bytes+down_bytes),0) from usage_entries where member_id=$1`

	// 1. Drain first: the exact byte accounting below is only sound when nothing
	//    from an earlier assertion is still in flight, and the window must open
	//    on an empty outbox.
	lcWaitEmpty(t, d, 180*time.Second, 8*time.Second, "before the injected outage")
	ledgerBefore := d.lcInt(ledgerQuery, f.Active)
	batchesBefore := d.lcInt(`select count(*) from usage_batches where agent_id=$1`, agentID)
	windowStart := d.lcText(`select now()::text`)
	t.Logf("outbox drained: %d byte(s) for the member, %d stored batch(es), accounting window opens at %s",
		ledgerBefore, batchesBefore, windowStart)

	// 2. Real usage while delivery is impossible: the proxy keeps working, the
	//    upload cannot be committed. This is an injected outage, named as such.
	//    The panel row is read here, as the last action while PostgreSQL still
	//    answers: it is the pre-action snapshot the caller's ladder step has to beat
	//    with a newer heartbeat (nothing can be read while the outage lasts).
	switchBase := d.lcAgent(d.fixture.Servers[0])
	t.Log("queue durability: stopping the owned PostgreSQL container so the Agent must retain batches")
	d.remote("stop postgres")

	// 3. Fixed traffic, and it really has to flow: a probe that is refused here
	//    would not generate the bytes this case accounts for.
	trafficAt := time.Now()
	for i := 0; i < 3; i++ {
		if !node1.ok() {
			t.Fatalf("the first node stopped serving during the injected outage (probe %d)", i+1)
		}
		if !node2.ok() {
			t.Fatalf("the second node stopped serving during the injected outage (probe %d)", i+1)
		}
	}
	t.Logf("injected outage: three real probes through each node finished in %.3fs", time.Since(trafficAt).Seconds())

	// 4. Stable barrier before the switch.
	preSwitch := lcWaitStable(t, d, f.Active, 180*time.Second, 12*time.Second, "before the switch")
	if len(preSwitch) == 0 {
		t.Fatal("the Agent retained no batch during the injected outage: the durability claim would be vacuous")
	}

	// 5. The switch happens while those batches are unacknowledged. switchBase is the
	//    snapshot taken just before the outage, so the heartbeat the ladder step waits
	//    for has to be strictly newer than it.
	d.lifecycle(switchAction)

	// 6. Final barrier after the switch: this snapshot is the complete set that
	//    has to be delivered, including what the switch itself produced.
	postSwitch := lcWaitStable(t, d, f.Active, 180*time.Second, 12*time.Second, "after the switch")
	if changed := lcCarried(preSwitch, postSwitch); len(changed) != 0 {
		t.Fatalf("batch(es) %v changed or vanished across %s: the switch did not preserve the durable queue",
			changed, switchAction)
	}
	if lcSameBatches(preSwitch, postSwitch) {
		t.Logf("the switch itself appended no batch: the same %d batch(es) stayed pending", len(postSwitch))
	} else {
		t.Logf("the switch appended %d batch(es), they are part of the final accounting",
			len(postSwitch)-len(preSwitch))
	}
	queuedBytes := int64(0)
	for _, id := range lcBatchIDs(postSwitch) {
		b := postSwitch[id]
		queuedBytes += b.MemberBytes
		t.Logf("queued batch %s: samples=%d bytes=%d member_bytes=%d attempts=%d sha256=%s",
			b.ID, b.Samples, b.Bytes, b.MemberBytes, b.Attempts, b.SHA)
	}
	if queuedBytes <= 0 {
		t.Fatalf("the fixed traffic of the injected outage produced no member byte in the retained queue (%d batch(es))", len(postSwitch))
	}

	// 7. Restore and drain.
	t.Log("queue durability: restoring the owned PostgreSQL container")
	d.remote("start postgres")
	postgresRestored = true
	waitFor(t, 180*time.Second, "the panel reports PostgreSQL healthy again", func() bool {
		status, state, postgres, _ := d.readyz()
		return status == http.StatusOK && state == "ok" && postgres == "ok"
	})
	lcWaitEmpty(t, d, 300*time.Second, 8*time.Second, "after the recovery")

	// 8. Per batch, not per total: the identical batch id must be credited
	//    exactly once, with the identical payload digest and the identical bytes.
	for _, id := range lcBatchIDs(postSwitch) {
		snapshot := postSwitch[id]
		credited := d.lcCredited(agentID, id, f.Active)
		switch {
		case credited.Rows == 0:
			t.Fatalf("batch %s is neither in the durable queue (it drained empty) nor credited in PostgreSQL: the switch lost %d byte(s)",
				id, snapshot.Bytes)
		case credited.Rows > 1:
			t.Fatalf("batch %s was credited %d times", id, credited.Rows)
		}
		if credited.SHA != snapshot.SHA {
			t.Fatalf("batch %s was stored with payload digest %s, the queued Agent rows hash to %s: a different payload was accounted under the same id",
				id, credited.SHA, snapshot.SHA)
		}
		if credited.Samples != snapshot.Samples {
			t.Fatalf("batch %s credited %d sample(s), the queue held %d", id, credited.Samples, snapshot.Samples)
		}
		if credited.Bytes != snapshot.Bytes {
			t.Fatalf("batch %s credited %d byte(s), the queue held %d", id, credited.Bytes, snapshot.Bytes)
		}
		if credited.MemberBytes != snapshot.MemberBytes {
			t.Fatalf("batch %s credited %d byte(s) for the member, the queue held %d", id, credited.MemberBytes, snapshot.MemberBytes)
		}
		t.Logf("batch %s credited exactly once: %d sample(s), %d byte(s) (%d for the member), sha256=%s",
			id, credited.Samples, credited.Bytes, credited.MemberBytes, credited.SHA)
	}

	// 9. Nothing outside the captured set may have reached PostgreSQL inside the
	//    window: the panel was unreachable, so every stored batch has to come from
	//    the outbox snapshot above. Together with the exact ledger delta this is
	//    the no-loss / no-double-credit proof.
	received := d.lcBatchesSince(agentID, windowStart)
	if len(received) != len(postSwitch) {
		t.Fatalf("PostgreSQL stored %d batch(es) since %s, the durable queue snapshot held %d: %v vs %v",
			len(received), windowStart, len(postSwitch), received, lcBatchIDs(postSwitch))
	}
	for _, id := range received {
		if _, ok := postSwitch[id]; !ok {
			t.Fatalf("batch %s reached PostgreSQL inside the accounting window but was not in the durable queue snapshot: the snapshot was not the complete pending set", id)
		}
	}
	if after := d.lcInt(`select count(*) from usage_batches where agent_id=$1`, agentID); after != batchesBefore+int64(len(postSwitch)) {
		t.Fatalf("the Agent stored %d batch(es) in total, expected %d baseline + %d retained: a batch was added or removed outside the captured set",
			after, batchesBefore, len(postSwitch))
	}
	if delta := d.lcInt(ledgerQuery, f.Active) - ledgerBefore; delta != queuedBytes {
		t.Fatalf("the member ledger grew by %d byte(s) after the recovery, the retained queue held exactly %d: a batch was lost or credited twice",
			delta, queuedBytes)
	}
	t.Logf("queue durability verified per batch across %s: %d batch(es) / %d member byte(s) delivered exactly once, ledger %d -> %d",
		switchAction, len(postSwitch), queuedBytes, ledgerBefore, ledgerBefore+queuedBytes)
	return switchBase
}

// TestDockerAgentVersionUpgrade covers the Agent half of A13: the shipped 0.1.0
// binary that is really installed in the image is replaced by the staged 0.1.1
// build on the same state volume, the durable outbox survives the switch, and the
// rollback returns to the shipped image binary.
func TestDockerAgentVersionUpgrade(t *testing.T) {
	d := dockerSetup(t)
	f := lcPrepare(t, d)
	t.Cleanup(func() {
		d.lifecycle("cleanup")
		lcQuiesce(t, d, f)
	})
	t.Logf("staged artifacts: %s", strings.TrimSpace(d.lifecycle("stage")))
	baselineText := d.lifecycle("baseline")
	baseline, err := parseLifecycleBaseline(baselineText)
	if err != nil {
		t.Fatalf("the lifecycle baseline could not be measured: %v", err)
	}
	t.Logf("image agent=%s sha256=%s core=%s sha256=%s", baseline.ImageAgent, baseline.ImageAgentSHA, baseline.ImageCore, baseline.ImageCoreSHA)
	t.Logf("staged agent=%s sha256=%s core=%s sha256=%s", baseline.StagedAgent, baseline.StagedAgentSHA, baseline.StagedCore, baseline.StagedCoreSHA)
	if baseline.ImageAgent != lifecycleShippedVersion || baseline.StagedAgent != lifecycleUpgradedVersion {
		t.Fatalf("unexpected Agent versions: image %s, staged %s", baseline.ImageAgent, baseline.StagedAgent)
	}
	if baseline.ImageAgentSHA == baseline.StagedAgentSHA {
		t.Fatal("the staged Agent binary equals the installed one: this would not be an upgrade")
	}
	if !strings.Contains(baseline.ImageCore, lifecycleShippedCore) || !strings.Contains(baseline.StagedCore, lifecycleStagedCore) {
		t.Fatalf("unexpected core versions: image %s, staged %s", baseline.ImageCore, baseline.StagedCore)
	}

	retired := lcRevokeAndProve(t, d, f.Revoked, lifecycleNodePort, "docker-lifecycle-revoked")
	node1 := d.proxyFor("docker-lifecycle-active", 20101)
	node2 := d.proxyFor("docker-lifecycle-active", lifecycleNodePort)
	if !node1.ok() || !node2.ok() {
		t.Fatal("both nodes of Agent 1 must serve the lifecycle member before the upgrade")
	}
	ledgerBefore := d.lcInt(`select coalesce(sum(up_bytes+down_bytes),0) from usage_entries where member_id=$1`, f.Active)
	before := d.lcAgent(d.fixture.Servers[0])
	if before.Version != lifecycleShippedVersion {
		t.Fatalf("the panel reports agent version %q before the upgrade, expected %s", before.Version, lifecycleShippedVersion)
	}
	if summary := strings.TrimSpace(d.lifecycle("state-summary")); summary != "" {
		t.Logf("state before the upgrade (digests only): %s", summary)
	}

	// Both node clients were exported while the panel still answered; the queue
	// durability case must never fetch a configuration after the outage started.
	switchBase := lcQueueDurability(t, d, f, "agent-upgrade", node1, node2)
	// agent-upgrade keeps the image core: the new Agent has to report 0.1.1 with the
	// unchanged 26.3.27 and a heartbeat that is newer than the row read right before
	// the switch (switchBase), so a stale 0.1.0 heartbeat can never close this step.
	after := lcVersionLadder(t, d, "agent upgrade", lcExpect{
		AgentVersion: lifecycleUpgradedVersion,
		CoreVersion:  lifecycleShippedCore,
		After:        switchBase.Heartbeat,
	}, switchBase, node1, node2, retired, ledgerBefore)
	if after.Version != lifecycleUpgradedVersion {
		t.Fatalf("the panel reports agent version %q after the upgrade", after.Version)
	}
	if mounts := d.lifecycle("mount-check agent1v2"); !strings.Contains(mounts, "ppaccept-20261021-agent1=/state") {
		t.Fatalf("the upgraded container does not use the owned state volume: %s", strings.TrimSpace(mounts))
	}

	// The rollback gets its own pre-action snapshot: reusing the row from before the
	// upgrade would let a 0.1.0 heartbeat from the old process satisfy the wait.
	preRollback := d.lcAgent(d.fixture.Servers[0])
	d.lifecycle("agent-rollback")
	final := lcVersionLadder(t, d, "agent rollback", lcExpect{
		AgentVersion: lifecycleShippedVersion,
		CoreVersion:  lifecycleShippedCore,
		After:        preRollback.Heartbeat,
	}, preRollback, node1, node2, retired, ledgerBefore)
	if final.Version != lifecycleShippedVersion {
		t.Fatalf("the panel reports agent version %q after the rollback", final.Version)
	}
	if mounts := d.lifecycle("mount-check agent1"); !strings.Contains(mounts, "ppaccept-20261021-agent1=/state") {
		t.Fatalf("the rolled back container does not use the owned state volume: %s", strings.TrimSpace(mounts))
	}
	t.Log("agent 0.1.0 -> 0.1.1 -> 0.1.0 verified on the unchanged state volume")
}

// TestDockerCoreVersionSwitch covers the core half of A13 on top of the new
// Agent: under the same 0.1.1 build the core really moves 26.3.27 -> 26.3.23 ->
// 26.3.27, and the case then returns to the shipped 0.1.0 image.
func TestDockerCoreVersionSwitch(t *testing.T) {
	d := dockerSetup(t)
	f := lcPrepare(t, d)
	t.Cleanup(func() {
		d.lifecycle("cleanup")
		lcQuiesce(t, d, f)
	})
	t.Logf("staged artifacts: %s", strings.TrimSpace(d.lifecycle("stage")))
	baseline, err := parseLifecycleBaseline(d.lifecycle("baseline"))
	if err != nil {
		t.Fatalf("the lifecycle baseline could not be measured: %v", err)
	}
	if baseline.StagedCore != lifecycleStagedCore || !strings.Contains(baseline.ImageCore, lifecycleShippedCore) {
		t.Fatalf("unexpected core versions: image %s, staged %s", baseline.ImageCore, baseline.StagedCore)
	}
	if baseline.ImageCoreSHA == baseline.StagedCoreSHA {
		t.Fatal("the staged core binary equals the installed one: this would not be a switch")
	}

	retired := lcRevokeAndProve(t, d, f.Revoked, lifecycleNodePort, "docker-lifecycle-revoked")
	node1 := d.proxyFor("docker-lifecycle-active", 20101)
	node2 := d.proxyFor("docker-lifecycle-active", lifecycleNodePort)
	if !node1.ok() || !node2.ok() {
		t.Fatal("both nodes of Agent 1 must serve the lifecycle member before the core switch")
	}
	ledgerBefore := d.lcInt(`select coalesce(sum(up_bytes+down_bytes),0) from usage_entries where member_id=$1`, f.Active)
	before := d.lcAgent(d.fixture.Servers[0])

	// Step 1: the new Agent with the shipped core (26.3.27). `before` is the row read
	// right before this action, so the 0.1.1 heartbeat has to be strictly newer than
	// it; matching the unchanged core string 26.3.27 alone used to end the wait on
	// the heartbeat of the old 0.1.0 process, before the switch had happened.
	d.lifecycle("agent-upgrade")
	upgraded := lcVersionLadder(t, d, "core 26.3.27 under the new agent", lcExpect{
		AgentVersion: lifecycleUpgradedVersion,
		CoreVersion:  lifecycleShippedCore,
		After:        before.Heartbeat,
	}, before, node1, node2, retired, ledgerBefore)
	if upgraded.Version != lifecycleUpgradedVersion {
		t.Fatalf("the core switch must run on the new agent, the panel reports %q", upgraded.Version)
	}
	// Step 2: the same new Agent with the staged core (26.3.23), with a fresh
	// pre-action snapshot: the row of step 1 would let a 0.1.1/26.3.27 heartbeat that
	// predates this action satisfy the wait.
	preSwitch := d.lcAgent(d.fixture.Servers[0])
	d.lifecycle("core-switch")
	switched := lcVersionLadder(t, d, "core 26.3.23 under the new agent", lcExpect{
		AgentVersion: lifecycleUpgradedVersion,
		CoreVersion:  lifecycleStagedCore,
		After:        preSwitch.Heartbeat,
	}, preSwitch, node1, node2, retired, ledgerBefore)
	if switched.Version != lifecycleUpgradedVersion {
		t.Fatalf("the staged core was not started by the same new agent: panel reports %q", switched.Version)
	}
	if mounts := d.lifecycle("mount-check agent1core"); !strings.Contains(mounts, "ppaccept-20261021-agent1=/state") {
		t.Fatalf("the core-switch container does not use the owned state volume: %s", strings.TrimSpace(mounts))
	}
	// Step 3: back to the shipped core under the same new Agent, again with a fresh
	// pre-action snapshot (26.3.23 -> 26.3.27 is a real core restart).
	preCoreRollback := d.lcAgent(d.fixture.Servers[0])
	d.lifecycle("core-rollback")
	back := lcVersionLadder(t, d, "core 26.3.27 again under the new agent", lcExpect{
		AgentVersion: lifecycleUpgradedVersion,
		CoreVersion:  lifecycleShippedCore,
		After:        preCoreRollback.Heartbeat,
	}, preCoreRollback, node1, node2, retired, ledgerBefore)
	if back.Version != lifecycleUpgradedVersion {
		t.Fatalf("the core rollback left the new agent: panel reports %q", back.Version)
	}
	// Step 4: cleanup must return to the shipped image agent (0.1.0 with 26.3.27).
	preCleanup := d.lcAgent(d.fixture.Servers[0])
	d.lifecycle("cleanup")
	final := lcVersionLadder(t, d, "shipped agent restored", lcExpect{
		AgentVersion: lifecycleShippedVersion,
		CoreVersion:  lifecycleShippedCore,
		After:        preCleanup.Heartbeat,
	}, preCleanup, node1, node2, retired, ledgerBefore)
	if final.Version != lifecycleShippedVersion || !strings.Contains(final.XrayVersion, lifecycleShippedCore) {
		t.Fatalf("cleanup left version=%q core=%q, expected %s/%s", final.Version, final.XrayVersion, lifecycleShippedVersion, lifecycleShippedCore)
	}
	t.Log("core 26.3.27 -> 26.3.23 -> 26.3.27 verified under agent 0.1.1, then back to the shipped 0.1.0")
}

// TestLifecycleParsing checks the parsers used above against the exact output
// shape of the lifecycle script, without any remote access.
func TestLifecycleParsing(t *testing.T) {
	raw := "installed image: sha256:abc\n" +
		"image agent:  proxypanel-agent 0.1.0 sha256=" + strings.Repeat("a", 64) + "\n" +
		"image core:   Xray 26.3.27 (Xray, Penetrates Everything.) d2758a0 (go1.26.1 linux/amd64) sha256=" + strings.Repeat("b", 64) + "\n" +
		"staged agent: proxypanel-agent 0.1.1 sha256=" + strings.Repeat("c", 64) + "\n" +
		"staged core:  Xray 26.3.23 (Xray, Penetrates Everything.) cb7bfeb (go1.26.1 linux/amd64) sha256=" + strings.Repeat("d", 64) + "\n"
	baseline, err := parseLifecycleBaseline(raw)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.ImageAgent != "0.1.0" || baseline.StagedAgent != "0.1.1" ||
		baseline.ImageCore != "26.3.27" || baseline.StagedCore != "26.3.23" {
		t.Fatalf("parsed versions are wrong: %+v", baseline)
	}
	if baseline.ImageAgentSHA != strings.Repeat("a", 64) || baseline.StagedCoreSHA != strings.Repeat("d", 64) {
		t.Fatalf("parsed digests are wrong: %+v", baseline)
	}
	if _, err := parseLifecycleBaseline("image agent: proxypanel-agent 0.1.0\n"); err == nil {
		t.Fatal("an incomplete baseline must be rejected")
	}
	if _, err := lifecycleDigest("image agent: proxypanel-agent 0.1.0 sha256=short\n", "image agent:"); err == nil {
		t.Fatal("a short digest must be rejected")
	}
	if _, err := lifecycleVersion("garbage 1.2.3\n", "image agent:"); err == nil {
		t.Fatal("a missing key must be rejected")
	}

	// The listener probe output is parsed deterministically: a changed or missing
	// line must never be read as "no listener".
	if count, err := parseListenerCount("listeners container=agent1 port=20105 count=0\n", "agent1", 20105); err != nil || count != 0 {
		t.Fatalf("a clean listener report must parse to 0: count=%d err=%v", count, err)
	}
	if count, err := parseListenerCount("\nlisteners container=agent1 port=20105 count=2\n", "agent1", 20105); err != nil || count != 2 {
		t.Fatalf("a listener report with 2 sockets must parse to 2: count=%d err=%v", count, err)
	}
	for _, raw := range []string{
		"",
		"listeners container=agent2 port=20105 count=0\n",
		"listeners container=agent1 port=20104 count=0\n",
		"listeners container=agent1 port=20105 count=\n",
		"listeners container=agent1 port=20105 count=-1\n",
		"listeners container=agent1 port=20105 count=many\n",
		"listeners container=agent1 port=20105 count=0\nlisteners container=agent1 port=20105 count=0\n",
		"something else\n",
	} {
		if _, err := parseListenerCount(raw, "agent1", 20105); err == nil {
			t.Fatalf("listener probe output %q must be rejected", raw)
		}
	}
}

// TestLifecycleSwitchGate pins the pure gates the real switch steps use. The first
// real TestDockerCoreVersionSwitch run failed here: the step "core 26.3.27 under
// the new agent" accepted the heartbeat of the old 0.1.0 process because the core
// string was unchanged, so the wait ended before the switch had happened and the
// following probe raced a half restarted Agent. Every negative case below is a
// state that a naive "the field contains the expected text" wait would accept.
func TestLifecycleSwitchGate(t *testing.T) {
	pre := time.Date(2026, 10, 21, 5, 14, 40, 0, time.UTC)
	healthy := lcAgentRow{
		ID: "agt_lifecycle", Version: "0.1.1", XrayVersion: "26.3.27",
		Applied: 146, Desired: 146, ControlGeneration: 4, Heartbeat: pre.Add(3 * time.Second),
	}
	want := lcExpect{AgentVersion: "0.1.1", CoreVersion: "26.3.27", After: pre}
	if err := lcPhaseReached(healthy, want); err != nil {
		t.Fatalf("the exact post-action observation must pass: %v", err)
	}
	with := func(mutate func(*lcAgentRow)) lcAgentRow {
		out := healthy
		mutate(&out)
		return out
	}
	for _, c := range []struct {
		name string
		now  lcAgentRow
		want lcExpect
	}{
		{"the heartbeat of the pre-action snapshot itself", with(func(r *lcAgentRow) { r.Heartbeat = pre }), want},
		{"a heartbeat older than the pre-action snapshot", with(func(r *lcAgentRow) { r.Heartbeat = pre.Add(-2 * time.Second) }), want},
		{"the old 0.1.0 Agent with the unchanged core 26.3.27", with(func(r *lcAgentRow) { r.Version = "0.1.0" }), want},
		{"the new Agent with the old core 26.3.23", with(func(r *lcAgentRow) { r.XrayVersion = "26.3.23" }), want},
		{"a core string that only ends with the expected token", with(func(r *lcAgentRow) { r.XrayVersion = "126.3.27" }), want},
		{"an Agent row without a heartbeat", with(func(r *lcAgentRow) { r.Heartbeat = time.Time{} }), want},
		{"an Agent that did not converge", with(func(r *lcAgentRow) { r.Applied = 145 }), want},
		{"an Agent without an applied revision", with(func(r *lcAgentRow) { r.Applied = 0; r.Desired = 0 }), want},
		{"a step without a pre-action snapshot", healthy, lcExpect{AgentVersion: "0.1.1", CoreVersion: "26.3.27"}},
		{"a step without an expected Agent version", healthy, lcExpect{CoreVersion: "26.3.27", After: pre}},
		{"a step without an expected core version", healthy, lcExpect{AgentVersion: "0.1.1", After: pre}},
	} {
		if err := lcPhaseReached(c.now, c.want); err == nil {
			t.Fatalf("%s was accepted as proof that the switch happened", c.name)
		} else {
			t.Logf("refused: %s (%v)", c.name, err)
		}
	}
	for _, c := range []struct {
		name          string
		first, second bool
		ok            bool
	}{
		{"both nodes answer", true, true, true},
		{"only the first node answers", true, false, false},
		{"only the second node answers", false, true, false},
		{"neither node answers", false, false, false},
	} {
		err := lcBothServe(c.first, c.second)
		if c.ok != (err == nil) {
			t.Fatalf("%s: lcBothServe(%v, %v) = %v, expected ok=%v", c.name, c.first, c.second, err, c.ok)
		}
	}
	if err := lcBothServe(true, false); err == nil || !strings.Contains(err.Error(), "20105") {
		t.Fatalf("a single healthy node must be refused and name the missing node: %v", err)
	}
	if err := lcBothServe(false, true); err == nil || !strings.Contains(err.Error(), "20101") {
		t.Fatalf("a single healthy node must be refused and name the missing node: %v", err)
	}
}

// TestLifecycleScriptStub runs the no-Docker regression of the owned lifecycle
// script (deploy/docker-lifecycle-selftest.sh). It reproduces the first real A13
// failure: a nested helper loop rewrote the caller's `name`, so `agent-upgrade`
// started ppaccept-20261021-agent1core and the caller's next `mount-check
// agent1v2` failed with exit 52. The regression needs neither Docker nor SSH,
// only a POSIX shell; without one it is skipped instead of failed.
func TestLifecycleScriptStub(t *testing.T) {
	shell := lifecyclePOSIXShell()
	if shell == "" {
		t.Skip("no POSIX shell found (set PANEL_BASH or put bash/sh on PATH); the lifecycle stub regression is skipped, not failed")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "deploy", "docker-lifecycle-selftest.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("the lifecycle stub regression is missing: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, shell, filepath.ToSlash(script)).CombinedOutput()
	if err != nil {
		t.Fatalf("the lifecycle stub regression failed (%v):\n%s", err, out)
	}
	if !strings.Contains(string(out), "all checks passed") {
		t.Fatalf("the lifecycle stub regression did not report success:\n%s", out)
	}
}

// lifecyclePOSIXShell returns a shell that can run the POSIX scripts of deploy/.
// PANEL_BASH wins so a machine with Git for Windows can point at it explicitly.
// The Windows WSL launcher (C:\Windows\System32\bash.exe) is skipped: it exists
// on many machines but cannot open a Windows script path, which would turn the
// regression into a bogus failure instead of a skip.
func lifecyclePOSIXShell() string {
	if p := os.Getenv("PANEL_BASH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	var candidates []string
	for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")} {
		if base == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(base, "Git", "bin", "bash.exe"),
			filepath.Join(base, "Programs", "Git", "bin", "bash.exe"))
	}
	for _, name := range []string{"bash", "sh"} {
		if p, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, p)
		}
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(filepath.ToSlash(p)), "/windows/system32/") {
			continue
		}
		return p
	}
	return ""
}

// TestAgentQueueReconstruction checks the offline reader that rebuilds the
// uploader payload from the Agent SQLite file, without any remote access: the
// samples come back in seq order (not in row order), the millisecond
// timestamps map back to UTC exactly as internal/agent/state does it, the
// digest is the one the uploader sends and the panel stores, and a batch whose
// rows cannot be rebuilt is an error instead of a silent zero. The stability
// barrier of the outage case is covered too: it must ignore the retry counter
// but never a changed payload.
func TestAgentQueueReconstruction(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "agent.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open the synthetic agent database: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE batches (batch_id TEXT PRIMARY KEY, created_at INTEGER NOT NULL, sample_count INTEGER NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', state TEXT NOT NULL)`,
		`CREATE TABLE samples (batch_id TEXT NOT NULL, seq INTEGER NOT NULL, node_id TEXT NOT NULL, member_id TEXT NOT NULL,
			core_generation TEXT NOT NULL, started_at INTEGER NOT NULL, ended_at INTEGER NOT NULL, upload INTEGER NOT NULL,
			download INTEGER NOT NULL, PRIMARY KEY (batch_id, seq))`,
		`INSERT INTO batches(batch_id, created_at, sample_count, attempts, state) VALUES ('b-open', 1000, 2, 3, 'pending')`,
		`INSERT INTO batches(batch_id, created_at, sample_count, attempts, state) VALUES ('b-done', 1000, 1, 0, 'acked')`,
		// Written in reverse seq order on purpose: the reader has to sort by seq.
		`INSERT INTO samples VALUES ('b-open', 1, 'nd_1', 'mbr_other', 'gen-1', 2000, 2500, 5, 7)`,
		`INSERT INTO samples VALUES ('b-open', 0, 'nd_1', 'mbr_active', 'gen-1', 1000, 1500, 100, 200)`,
		`INSERT INTO samples VALUES ('b-done', 0, 'nd_1', 'mbr_active', 'gen-1', 1000, 1500, 1, 1)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			t.Fatalf("prepare the synthetic agent database: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the synthetic agent database: %v", err)
	}

	queue, err := parseAgentQueue(dbPath, "mbr_active")
	if err != nil {
		t.Fatalf("the synthetic queue could not be read: %v", err)
	}
	if len(queue) != 1 {
		t.Fatalf("the reader returned %d batch(es), expected only the pending one: %v", len(queue), lcBatchIDs(queue))
	}
	batch, ok := queue["b-open"]
	if !ok {
		t.Fatalf("the pending batch is missing from the reader output: %v", lcBatchIDs(queue))
	}
	if batch.Samples != 2 || batch.Bytes != 312 || batch.MemberBytes != 300 || batch.Attempts != 3 {
		t.Fatalf("reconstructed accounting is wrong: %+v", batch)
	}
	// The digest is the canonical one of the uploader payload in seq order.
	ordered := []contracts.UsageSample{
		{NodeID: "nd_1", MemberID: "mbr_active", CoreGeneration: "gen-1",
			StartedAt: time.UnixMilli(1000).UTC(), EndedAt: time.UnixMilli(1500).UTC(), UploadBytes: 100, DownloadBytes: 200},
		{NodeID: "nd_1", MemberID: "mbr_other", CoreGeneration: "gen-1",
			StartedAt: time.UnixMilli(2000).UTC(), EndedAt: time.UnixMilli(2500).UTC(), UploadBytes: 5, DownloadBytes: 7},
	}
	want := usagecalc.PayloadSHA256(contracts.UsageBatch{
		SchemaVersion: contracts.SchemaVersion, BatchID: "b-open", Samples: ordered})
	if batch.SHA != want {
		t.Fatalf("reconstructed digest %s, the uploader payload hashes to %s", batch.SHA, want)
	}
	swapped := usagecalc.PayloadSHA256(contracts.UsageBatch{
		SchemaVersion: contracts.SchemaVersion, BatchID: "b-open",
		Samples: []contracts.UsageSample{ordered[1], ordered[0]}})
	if swapped == batch.SHA {
		t.Fatal("the sample order does not change the digest: the reader cannot be proven to sort by seq")
	}
	// Without a member filter no per member bytes are accounted.
	neutral, err := parseAgentQueue(dbPath, "")
	if err != nil {
		t.Fatalf("the synthetic queue could not be read without a member filter: %v", err)
	}
	if neutral["b-open"].MemberBytes != 0 || neutral["b-open"].SHA != want {
		t.Fatalf("the member filter leaked into the neutral read: %+v", neutral["b-open"])
	}

	// A header that disagrees with the stored rows would upload a different
	// payload than the one accounted here, so it must fail loudly.
	db, err = sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("reopen the synthetic agent database: %v", err)
	}
	if _, err := db.Exec(`UPDATE batches SET sample_count=3 WHERE batch_id='b-open'`); err != nil {
		db.Close()
		t.Fatalf("break the synthetic batch header: %v", err)
	}
	db.Close()
	if _, err := parseAgentQueue(dbPath, ""); err == nil {
		t.Fatal("a batch whose header disagrees with its sample rows must be rejected")
	}
	if _, err := parseAgentQueue(filepath.Join(dir, "missing.db"), ""); err == nil {
		t.Fatal("a missing agent database must be reported, never read as an empty queue")
	}

	// The stability barrier ignores the retry counter but not the payload.
	stable := map[string]lcBatch{"b1": {ID: "b1", Samples: 1, Bytes: 10, MemberBytes: 10, SHA: strings.Repeat("a", 64)}}
	retried := map[string]lcBatch{"b1": {ID: "b1", Samples: 1, Bytes: 10, MemberBytes: 10, Attempts: 9, SHA: strings.Repeat("a", 64)}}
	if !lcSameBatches(stable, retried) {
		t.Fatal("a growing retry counter must not break the stability barrier")
	}
	rehashed := map[string]lcBatch{"b1": {ID: "b1", Samples: 1, Bytes: 10, MemberBytes: 10, SHA: strings.Repeat("b", 64)}}
	if lcSameBatches(stable, rehashed) {
		t.Fatal("a different payload digest must break the stability barrier")
	}
	if lcSameBatches(stable, map[string]lcBatch{}) {
		t.Fatal("an empty snapshot must not be read as a stable one")
	}
	if changed := lcCarried(stable, retried); len(changed) != 0 {
		t.Fatalf("a retried batch must survive the switch unchanged, got %v", changed)
	}
	if changed := lcCarried(stable, map[string]lcBatch{}); len(changed) != 1 {
		t.Fatalf("a vanished batch must be reported, got %v", changed)
	}
	if changed := lcCarried(stable, rehashed); len(changed) != 1 {
		t.Fatalf("a re-hashed batch must be reported, got %v", changed)
	}
	if ids := lcBatchIDs(map[string]lcBatch{"b": {}, "a": {}}); len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("batch ids are not listed in a stable order: %v", ids)
	}
}

// TestDockerLifecycleDiagnose is a read-only diagnostic entry for a broken
// acceptance fixture: it prints only non-secret facts (revision pointers, node
// flags, the JSON type of the rejected key, panel task/item states, receipt error
// text and container facts) and never changes state, so it is safe to run against
// the current failing stack. It never creates the fixture either: when the fixture
// file is absent it says so and stops instead of adding objects to a broken stack.
func TestDockerLifecycleDiagnose(t *testing.T) {
	d := dockerSetup(t)
	f, path, ok := d.loadLifecycleFixture()
	if !ok {
		t.Logf("no existing lifecycle fixture at %s: nothing was created and nothing was changed", path)
		d.lcLogSafeState("diagnose without a lifecycle fixture")
		t.Logf("already running containers: %s", strings.TrimSpace(d.lifecycle("status")))
		return
	}
	t.Logf("fixture: node=%s group=%s subscription=%s active_member=%s revoked_member=%s",
		f.Node, f.Group, f.Subscription, f.Active, f.Revoked)
	if state, err := d.lcNodeOrError(f.Node); err != nil {
		t.Logf("lifecycle node could not be read: %v", err)
	} else {
		t.Logf("lifecycle node: archived=%t enabled=%t port=%d keys=%v fallbacks_present=%t fallbacks_json_type=%s",
			state.Archived, state.Enabled, state.Port, state.SettingsKeys, state.HasFallbacks, state.FallbacksType)
	}
	// The listener fact has to come from inside the owned container: the host
	// loopback port is forwarded by the SSH tunnel either way.
	for _, container := range []string{"agent1", "agent2"} {
		count, err := d.lcListenerCount(container, lifecycleNodePort)
		if err != nil {
			t.Logf("listener probe %s:%d failed: %v", container, lifecycleNodePort, err)
			continue
		}
		t.Logf("listener probe %s:%d count=%d", container, lifecycleNodePort, count)
	}
	d.lcLogSafeState("diagnose")
	t.Logf("already running containers: %s", strings.TrimSpace(d.lifecycle("status")))
	t.Logf("state volume digests (no content): %s", strings.TrimSpace(d.lifecycle("state-summary")))
	status, state, postgres, redis := d.readyz()
	t.Logf("panel readiness: http=%d state=%s postgres=%s redis=%s", status, state, postgres, redis)
}

// TestDockerLifecycleRecover restores a safe acceptance fixture after a failed
// case without deleting the owned Agent state volumes: it deactivates both
// lifecycle members, archives the lifecycle node through the protected
// DELETE /api/v1/admin/nodes/{id} (usage history, audit rows and ledger entries
// stay) and restarts the owned Agent containers, which drops any in-memory
// failure backoff (supervisor.go:592/1101). Afterwards it proves that the
// lifecycle port is really free so the client matrix and the version switches can
// run. The case is idempotent: an already archived node is neither patched again
// nor archived twice, and it never adds a fixture object.
func TestDockerLifecycleRecover(t *testing.T) {
	d := dockerSetup(t)
	f, path, ok := d.loadLifecycleFixture()
	if !ok {
		t.Fatalf("no existing lifecycle fixture at %s: run the lifecycle case once to create it, or the recovery cannot know which node/members to repair", path)
	}
	d.remote("agent1")
	d.remote("agent2")
	d.remote("start agent1")
	d.remote("start agent2")
	t.Logf("recovery fixture: node=%s active_member=%s revoked_member=%s", f.Node, f.Active, f.Revoked)

	// 1. Repair the panel state. Disabling the node is not enough: BuildSnapshot
	//    still emits an instance for every non archived node - a disabled node
	//    simply has no clients, so its core keeps LISTENing on 20105 (that is what
	//    the earlier recovery log proved). Only the archive removes the instance,
	//    so only the archive frees the port.
	d.patchMember(f.Active, map[string]any{"active": false})
	d.patchMember(f.Revoked, map[string]any{"active": false})
	if err := lcArchiveNode(t, d, f.Node); err != nil {
		d.lcLogSafeState("recover: the lifecycle node could not be archived")
		t.Fatalf("archive the lifecycle node %s: %v", f.Node, err)
	}
	d.memberPatch(map[string]any{"active": true, "proxy_enabled": true})
	if state, err := d.lcNodeOrError(f.Node); err != nil {
		t.Fatalf("the lifecycle node state could not be read after the recovery: %v", err)
	} else if !state.Archived || state.Enabled {
		t.Fatalf("the lifecycle node is not archived after the recovery (archived=%t enabled=%t)", state.Archived, state.Enabled)
	}
	t.Log("panel state repaired: lifecycle node archived, lifecycle members inactive")

	// 2. Clear an Agent-side failure backoff by restarting the owned containers.
	//    The SQLite state volume is untouched: no identity, queue or ledger change.
	d.remote("restart agent1")
	d.remote("restart agent2")
	converged, restarted := lcConverge(t, d, 120*time.Second)
	if !converged {
		d.lcLogSafeState("recover: no convergence")
		t.Fatalf("the owned Agents did not reach the desired revision after the recovery restart")
	}
	if restarted {
		t.Log("the Agents needed the explicit recovery restart (a rejected document had re-armed their failure backoff)")
	}

	// 3. The lifecycle port must be free again for the client matrix. A TCP
	//    handshake against 127.0.0.1 is useless here: the SSH tunnel forwards
	//    that port locally even when nothing serves it on the host. Only the
	//    kernel table inside the owned Agent container answers the question.
	ok, count, err := lcWaitNoListener(t, d, "agent1", lifecycleNodePort, 60*time.Second)
	if err != nil {
		d.lcLogSafeState("recover: listener probe failed")
		t.Fatalf("the listener probe inside the owned agent1 container failed: %v", err)
	}
	if !ok {
		d.lcLogSafeState("recover: node still listening")
		t.Fatalf("agent1 still LISTENs on %d (%d socket(s)) after the recovery: the client matrix would collide",
			lifecycleNodePort, count)
	}
	// Panel bookkeeping is reported, not used as a pass criterion: the panel does
	// not stop-mark an instance whose node was archived (it only stops applying
	// it), so the kernel table is the authoritative answer to "is the port free".
	if running := d.lcInt(`select count(*) from core_instances where node_id=$1 and state='running'`, f.Node); running != 0 {
		t.Logf("note: no LISTEN socket remains on %d but the panel still reports %d running core instance(s) for the archived node",
			lifecycleNodePort, running)
	}
	t.Logf("agent1 holds no LISTEN socket on %d after the recovery", lifecycleNodePort)
	t.Log("recovery complete: lifecycle node archived, members inactive, Agents converged, lifecycle port free (state volume untouched)")
}
