package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/period"
	"proxypanel/internal/panel/secrets"
)

// Regression tests for the four backend defects fixed in this change:
//
//  1. a resource change and the revision that publishes it must commit in the
//     same transaction (no failable post-commit bump);
//  2. a member cycle must roll forward and publish the results;
//  3. a node created after its agent registered must still be dispatched;
//  4. internal authoritative queries must not be truncated at 200 rows.
//
// They need a real PostgreSQL instance because every guarantee is about
// transaction behaviour. They are skipped unless PANEL_TEST_DATABASE_URL points
// at the documented isolated instance, and each run creates and drops its own
// schema.

func regTestDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PANEL_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("PANEL_TEST_DATABASE_URL is not set; skipping store PostgreSQL regression tests")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("PANEL_TEST_DATABASE_URL is not a valid DSN: %v", err)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != 15432 || cfg.Database != "proxypanel" {
		t.Fatalf("refusing to run against %s:%d/%s; only 127.0.0.1:15432/proxypanel is allowed",
			cfg.Host, cfg.Port, cfg.Database)
	}
	return dsn
}

func regMigrationsDir(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"../../../migrations", "migrations"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Fatalf("migrations directory not found")
	return ""
}

func regRandomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// newRegStore opens a store on its own schema and drops it afterwards.
func newRegStore(t *testing.T) *Store {
	t.Helper()
	dsn := regTestDSN(t)
	ctx := context.Background()
	schema := "ppreg_" + strings.ToLower(regRandomHex(t, 6))

	adminConn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	if _, err := adminConn.Exec(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		_ = adminConn.Close(ctx)
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = adminConn.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
		_ = adminConn.Close(cleanupCtx)
	})

	key, err := secrets.ParseKey(strings.Repeat("store-regression-master-key-", 2))
	if err != nil {
		t.Fatalf("master key: %v", err)
	}
	st, err := Open(ctx, Options{DatabaseURL: dsn, MasterKey: key, MigrationsDir: regMigrationsDir(t), SearchPath: schema})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

func regServer(t *testing.T, st *Store, name string) *domain.Server {
	t.Helper()
	v, err := st.CreateServer(context.Background(), &domain.Server{
		ID: ids.New(ids.Server), Name: name, Address: "127.0.0.1", Currency: "CNY",
	})
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	return v
}

// regAgent inserts a registered agent for a server (the registration flow itself
// is covered by the API tests).
func regAgent(t *testing.T, st *Store, serverID string) *domain.Agent {
	t.Helper()
	id := ids.New(ids.Agent)
	token := secrets.NewToken(24)
	if _, err := st.DB.ExecContext(context.Background(), `INSERT INTO agents
		(id, server_id, name, version, token_hash, token_prefix) VALUES ($1,$2,$3,'test',$4,$5)`,
		id, serverID, "agent-"+serverID, secrets.HashToken(token), token[:8]); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	a, err := st.GetAgent(context.Background(), id)
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}
	return a
}

func regNode(t *testing.T, st *Store, serverID, name string, port int) *domain.Node {
	t.Helper()
	n, err := st.CreateNode(context.Background(), &domain.Node{
		ID: ids.New(ids.Node), ServerID: serverID, Name: name, Enabled: true, Host: "127.0.0.1",
		Port: port, Listen: "0.0.0.0", Protocol: "vless",
		StreamSettings: domain.NewObject(), ProtocolSettings: domain.NewObject(),
		ClientSettings: domain.NewObject(),
	}, ids.New(ids.CoreInstance))
	if err != nil {
		t.Fatalf("create node %s: %v", name, err)
	}
	return n
}

func regSubscription(t *testing.T, st *Store, name string, quota *int64, groupIDs []string) *domain.Subscription {
	t.Helper()
	v, err := st.CreateSubscription(context.Background(), &domain.Subscription{
		ID: ids.New(ids.Subscription), Name: name, Timezone: "UTC", AnchorDay: 1,
		AllowNewBindings: true, AccessEnabled: true, QuotaBytes: quota, NodeGroupIDs: groupIDs,
	})
	if err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	return v
}

func regMember(t *testing.T, st *Store, username string, sub *domain.Subscription, cycle period.Boundary) *domain.Member {
	t.Helper()
	start, end := cycle.Start, cycle.End
	hash, err := secrets.HashPassword("member-password-1")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	m, err := st.CreateMember(context.Background(), &domain.Member{
		ID: ids.New(ids.Member), Username: username, PasswordHash: hash, Role: "member",
		Active: true, ProxyEnabled: true, SubscriptionID: &sub.ID,
		PeriodStart: &start, PeriodEnd: &end, PeriodTimezone: "UTC", PeriodAnchorDay: 1,
	})
	if err != nil {
		t.Fatalf("create member %s: %v", username, err)
	}
	return m
}

func regCredential(t *testing.T, st *Store, memberID, nodeID string) {
	t.Helper()
	enc, err := st.encryptSecret("11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatalf("encrypt credential: %v", err)
	}
	if _, err := st.DB.ExecContext(context.Background(), `INSERT INTO credentials
		(id, member_id, node_id, kind, secret_enc, state) VALUES ($1,$2,$3,'uuid',$4,'active')`,
		ids.New(ids.Credential), memberID, nodeID, enc); err != nil {
		t.Fatalf("insert credential: %v", err)
	}
}

// regUsage inserts one closed-cycle ledger row so the member starts over quota.
func regUsage(t *testing.T, st *Store, memberID, nodeID string, window period.Boundary, bytes int64) {
	t.Helper()
	if _, err := st.DB.ExecContext(context.Background(), `INSERT INTO usage_entries
		(agent_id, batch_id, member_id, node_id, credential_id, period_start, period_end,
		 up_bytes, down_bytes, sample_started_at, sample_ended_at)
		VALUES ('seed','seed-'||$5::text,$1,$2,'',$3,$4,$6,0,$3,$4)`,
		memberID, nodeID, window.Start, window.End, ids.New("seed"), bytes); err != nil {
		t.Fatalf("insert usage row: %v", err)
	}
}

// regFixture is one node/group/subscription/member set with an expired cycle.
type regFixture struct {
	st     *Store
	server *domain.Server
	agent  *domain.Agent
	node   *domain.Node
	group  *domain.NodeGroup
	sub    *domain.Subscription
	member *domain.Member
	prev   period.Boundary
	cur    period.Boundary
}

func newRegFixture(t *testing.T, quota *int64) *regFixture {
	t.Helper()
	st := newRegStore(t)
	ctx := context.Background()
	now := st.now()

	f := &regFixture{st: st}
	f.server = regServer(t, st, "server-a")
	f.agent = regAgent(t, st, f.server.ID)
	f.node = regNode(t, st, f.server.ID, "node-a", 10443)

	group, err := st.CreateNodeGroup(ctx, &domain.NodeGroup{
		ID: ids.New(ids.NodeGroup), Name: "group-a", NodeIDs: []string{f.node.ID},
	})
	if err != nil {
		t.Fatalf("create node group: %v", err)
	}
	f.group = group
	f.sub = regSubscription(t, st, "sub-a", quota, []string{group.ID})
	// The expired cycle must be the window *before* the current one. Using
	// period.WindowBefore(now, ...) here would end exactly at now and start on the
	// same anchor instant as the current cycle, so the "advanced" member would keep
	// the current period_start and the ledger assertions below would be vacuous.
	f.cur = period.Containing(1, time.UTC, now)
	f.prev = period.WindowBefore(f.cur.Start, 1, time.UTC)
	f.member = regMember(t, st, "user-a", f.sub, f.prev)
	regCredential(t, st, f.member.ID, f.node.ID)
	return f
}

// (1) A node change and the revision that publishes it commit together, and a
// rejected change publishes nothing.
func TestNodeMutationPublishesRevisionInSameCommit(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()
	server := regServer(t, st, "server-a")
	agent := regAgent(t, st, server.ID)

	node := regNode(t, st, server.ID, "node-a", 10443)

	// (3) The node created after the agent registered is linked to it, so a
	// revision scoped to the node can be dispatched at all.
	var linked int
	if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM core_instances
		WHERE node_id=$1 AND agent_id=$2`, node.ID, agent.ID).Scan(&linked); err != nil {
		t.Fatalf("count instance link: %v", err)
	}
	if linked != 1 {
		t.Fatalf("core instance of %s is not linked to the registered agent", node.Name)
	}

	control, err := st.GetControlState(ctx)
	if err != nil {
		t.Fatalf("control state: %v", err)
	}
	created := mustAgent(t, st, agent.ID)
	if created.DesiredRevision != control.RevisionCounter {
		t.Fatalf("after node create: agent desired revision %d, control %d",
			created.DesiredRevision, control.RevisionCounter)
	}

	// A rejected update (stale version) must not publish anything.
	before := control.RevisionCounter
	if _, err := st.UpdateNode(ctx, node.ID, 999, func(n *domain.Node) error {
		n.Enabled = false
		return nil
	}); err == nil {
		t.Fatal("expected a version conflict")
	}
	control, _ = st.GetControlState(ctx)
	if control.RevisionCounter != before {
		t.Fatal("a rejected change must not advance the revision")
	}

	// A committed update advances the revision and the agent pointer together.
	if _, err := st.UpdateNode(ctx, node.ID, 0, func(n *domain.Node) error {
		n.Enabled = false
		return nil
	}); err != nil {
		t.Fatalf("disable node: %v", err)
	}
	control, _ = st.GetControlState(ctx)
	if control.RevisionCounter <= before {
		t.Fatal("a committed node change must advance the revision")
	}
	updated := mustAgent(t, st, agent.ID)
	if updated.DesiredRevision != control.RevisionCounter {
		t.Fatalf("after node update: agent desired revision %d, control %d",
			updated.DesiredRevision, control.RevisionCounter)
	}
}

// (1) A member change, including credential rotation, publishes the revision in
// the same transaction.
func TestMemberAndCredentialChangesPublishRevision(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()
	server := regServer(t, st, "server-a")
	agent := regAgent(t, st, server.ID)
	node := regNode(t, st, server.ID, "node-a", 10443)

	control, _ := st.GetControlState(ctx)
	hash, err := secrets.HashPassword("member-password-1")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	member, err := st.CreateMember(ctx, &domain.Member{
		ID: ids.New(ids.Member), Username: "user-a", PasswordHash: hash, Role: "member",
		Active: true, ProxyEnabled: true,
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	afterCreate, _ := st.GetControlState(ctx)
	if afterCreate.RevisionCounter <= control.RevisionCounter {
		t.Fatal("creating a member must publish a revision")
	}
	if got := mustAgent(t, st, agent.ID).DesiredRevision; got != afterCreate.RevisionCounter {
		t.Fatalf("agent desired revision %d, control %d", got, afterCreate.RevisionCounter)
	}

	if _, err := st.RotateCredentials(ctx, member.ID, []string{node.ID},
		func(nodeID string) (string, string, string, error) {
			return ids.New(ids.Credential), ids.NewUUID(), "uuid", nil
		}); err != nil {
		t.Fatalf("rotate credentials: %v", err)
	}
	afterRotate, _ := st.GetControlState(ctx)
	if afterRotate.RevisionCounter <= afterCreate.RevisionCounter {
		t.Fatal("a credential rotation must publish a revision")
	}
	if got := mustAgent(t, st, agent.ID).DesiredRevision; got != afterRotate.RevisionCounter {
		t.Fatalf("agent desired revision %d, control %d", got, afterRotate.RevisionCounter)
	}
}

// (3) Moving a node to another server revokes on the previous server too.
func TestNodeMoveRevokesOnBothServers(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()
	serverA := regServer(t, st, "server-a")
	serverB := regServer(t, st, "server-b")
	agentA := regAgent(t, st, serverA.ID)
	agentB := regAgent(t, st, serverB.ID)
	node := regNode(t, st, serverA.ID, "node-a", 10443)

	if _, err := st.UpdateNode(ctx, node.ID, 0, func(n *domain.Node) error {
		n.ServerID = serverB.ID
		return nil
	}); err != nil {
		t.Fatalf("move node: %v", err)
	}
	control, _ := st.GetControlState(ctx)
	if got := mustAgent(t, st, agentA.ID).DesiredRevision; got != control.RevisionCounter {
		t.Fatalf("previous server agent desired revision %d, control %d", got, control.RevisionCounter)
	}
	if got := mustAgent(t, st, agentB.ID).DesiredRevision; got != control.RevisionCounter {
		t.Fatalf("new server agent desired revision %d, control %d", got, control.RevisionCounter)
	}
	var ciServer string
	if err := st.DB.QueryRowContext(ctx, `SELECT server_id FROM core_instances WHERE node_id=$1`, node.ID).Scan(&ciServer); err != nil {
		t.Fatalf("read core instance: %v", err)
	}
	if ciServer != serverB.ID {
		t.Fatalf("core instance still points at %s", ciServer)
	}
}

// (2) Reading a member whose cycle ended rolls the cycle forward, restores
// access, keeps the closed cycle's ledger and publishes the revision.
func TestEvaluateAccessAdvancesEndedCycle(t *testing.T) {
	quota := int64(1000)
	f := newRegFixture(t, &quota)
	ctx := context.Background()

	// The member consumed more than the quota during the closed cycle.
	regUsage(t, f.st, f.member.ID, f.node.ID, f.prev, 4000)
	if _, err := f.st.DB.ExecContext(ctx, `UPDATE members SET over_quota=true WHERE id=$1`, f.member.ID); err != nil {
		t.Fatalf("seed over quota: %v", err)
	}
	controlBefore, _ := f.st.GetControlState(ctx)

	m, err := f.st.GetMember(ctx, f.member.ID)
	if err != nil {
		t.Fatalf("get member: %v", err)
	}
	res, err := f.st.EvaluateAccessForMember(ctx, m)
	if err != nil {
		t.Fatalf("evaluate access: %v", err)
	}
	if res.Evidence.Member.PeriodStart == nil || !res.Evidence.Member.PeriodStart.Equal(f.cur.Start) {
		t.Fatalf("cycle was not rolled forward: %v", res.Evidence.Member.PeriodStart)
	}
	if !res.Evidence.Member.PeriodEnd.After(f.st.now()) {
		t.Fatal("the advanced cycle must end in the future")
	}
	if res.Evidence.UsedBytes != 0 {
		t.Fatalf("used bytes in the new cycle = %d, want 0", res.Evidence.UsedBytes)
	}
	if !res.Decision.Access {
		t.Fatalf("a new cycle must restore access, got %v", res.Decision.Reasons)
	}

	refreshed, err := f.st.GetMember(ctx, f.member.ID)
	if err != nil {
		t.Fatalf("reload member: %v", err)
	}
	if refreshed.OverQuota {
		t.Fatal("over_quota must be recomputed for the new cycle")
	}
	if refreshed.PeriodStart == nil || !refreshed.PeriodStart.Equal(f.cur.Start) {
		t.Fatalf("persisted period_start = %v, want %v", refreshed.PeriodStart, f.cur.Start)
	}

	// History is never rewritten: the closed cycle keeps its rows.
	var oldRows int
	if err := f.st.DB.QueryRowContext(ctx, `SELECT count(*) FROM usage_entries
		WHERE member_id=$1 AND period_start=$2`, f.member.ID, f.prev.Start).Scan(&oldRows); err != nil {
		t.Fatalf("count old rows: %v", err)
	}
	if oldRows != 1 {
		t.Fatalf("closed cycle rows = %d, want 1", oldRows)
	}

	controlAfter, _ := f.st.GetControlState(ctx)
	if controlAfter.RevisionCounter <= controlBefore.RevisionCounter {
		t.Fatal("advancing the cycle must publish a revision")
	}
	if got := mustAgent(t, f.st, f.agent.ID).DesiredRevision; got != controlAfter.RevisionCounter {
		t.Fatalf("agent desired revision %d, control %d", got, controlAfter.RevisionCounter)
	}
}

// (2) The snapshot both advances the cycle and reports the refreshed revision,
// so an agent cannot answer 204 for a state it never received.
func TestSnapshotAdvancesCycleAndReturnsFreshRevision(t *testing.T) {
	quota := int64(1000)
	f := newRegFixture(t, &quota)
	ctx := context.Background()

	regUsage(t, f.st, f.member.ID, f.node.ID, f.prev, 4000)
	if _, err := f.st.DB.ExecContext(ctx, `UPDATE members SET over_quota=true WHERE id=$1`, f.member.ID); err != nil {
		t.Fatalf("seed over quota: %v", err)
	}
	stale := mustAgent(t, f.st, f.agent.ID)
	// The revision is captured as a value: BuildSnapshot is allowed to refresh the
	// caller's view, so comparing two struct fields afterwards would be vacuous.
	before := stale.DesiredRevision

	snap, err := f.st.BuildSnapshot(ctx, stale)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	fresh := mustAgent(t, f.st, f.agent.ID)
	if fresh.DesiredRevision <= before {
		t.Fatalf("the build must have advanced the revision: before %d, after %d", before, fresh.DesiredRevision)
	}
	if snap.Revision != fresh.DesiredRevision {
		t.Fatalf("snapshot revision %d, latest desired revision %d: the agent would answer 204",
			snap.Revision, fresh.DesiredRevision)
	}
	if len(snap.Instances) != 1 {
		t.Fatalf("instances = %d, want 1", len(snap.Instances))
	}
	if len(snap.Instances[0].Users) != 1 {
		t.Fatalf("users = %d, want the restored member", len(snap.Instances[0].Users))
	}
	if snap.Instances[0].Users[0].MemberID != f.member.ID {
		t.Fatalf("unexpected user %s", snap.Instances[0].Users[0].MemberID)
	}
}

// (2) The exported tick advances expired cycles once and is idempotent.
func TestAdvancePeriodsTick(t *testing.T) {
	quota := int64(1000)
	f := newRegFixture(t, &quota)
	ctx := context.Background()

	// A second member whose cycle is still running must not be touched.
	running := regMember(t, f.st, "user-running", f.sub, f.cur)

	advanced, err := f.st.AdvancePeriods(ctx, f.st.now())
	if err != nil {
		t.Fatalf("advance periods: %v", err)
	}
	if advanced != 1 {
		t.Fatalf("advanced %d members, want 1", advanced)
	}
	again, err := f.st.AdvancePeriods(ctx, f.st.now())
	if err != nil {
		t.Fatalf("advance periods twice: %v", err)
	}
	if again != 0 {
		t.Fatalf("second tick advanced %d members, want 0", again)
	}
	moved, err := f.st.GetMember(ctx, f.member.ID)
	if err != nil {
		t.Fatalf("reload member: %v", err)
	}
	if moved.PeriodStart == nil || !moved.PeriodStart.Equal(f.cur.Start) {
		t.Fatalf("period_start = %v, want %v", moved.PeriodStart, f.cur.Start)
	}
	untouched, err := f.st.GetMember(ctx, running.ID)
	if err != nil {
		t.Fatalf("reload running member: %v", err)
	}
	if untouched.Version != running.Version {
		t.Fatal("a running cycle must not be rewritten")
	}
}

// (2) A subscription rule edited while the cycle runs keeps the running cycle
// and takes effect at the next boundary.
func TestSubscriptionRuleEditSchedulesImmediatelyForNextBoundary(t *testing.T) {
	quota := int64(1000)
	f := newRegFixture(t, &quota)
	ctx := context.Background()
	// Start with a genuinely running cycle, not the expired fixture cycle.
	if _, err := f.st.UpdateMember(ctx, f.member.ID, 0, func(m *domain.Member) error {
		m.PeriodStart, m.PeriodEnd = &f.cur.Start, &f.cur.End
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	regUsage(t, f.st, f.member.ID, f.node.ID, f.cur, 100)
	if _, err := f.st.UpdateSubscription(ctx, f.sub.ID, 0, func(v *domain.Subscription) error {
		v.AnchorDay, v.Timezone = 15, "Asia/Tokyo"
		return nil
	}); err != nil {
		t.Fatalf("update subscription rule: %v", err)
	}
	// No tick or access read is allowed to create the pending rule for us.
	moved, err := f.st.GetMember(ctx, f.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.PeriodStart == nil || !moved.PeriodStart.Equal(f.cur.Start) ||
		moved.PeriodEnd == nil || !moved.PeriodEnd.Equal(f.cur.End) ||
		moved.PeriodAnchorDay != 1 || moved.PeriodTimezone != "UTC" {
		t.Fatalf("the edit changed the running cycle: %+v", moved)
	}
	if p := moved.PendingPeriodRule; p == nil || p.AnchorDay != 15 || p.Timezone != "Asia/Tokyo" || p.SubmittedAt == nil {
		t.Fatalf("the edit must immediately persist the next-cycle rule: %+v", p)
	}
	// Marked clock injection: apply at the FIRST old-rule boundary exactly.
	f.st.Now = func() time.Time { return f.cur.End }
	if n, err := f.st.AdvancePeriods(ctx, f.cur.End); err != nil || n != 1 {
		t.Fatalf("advance at the first boundary = %d, %v", n, err)
	}
	settled, err := f.st.GetMember(ctx, f.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.PeriodAnchorDay != 15 || settled.PeriodTimezone != "Asia/Tokyo" || settled.PendingPeriodRule != nil {
		t.Fatalf("the first boundary did not consume the edited rule: %+v", settled)
	}
	loc, _ := period.LoadTimezone("Asia/Tokyo")
	want := period.Containing(15, loc, f.cur.End)
	if settled.PeriodStart == nil || !settled.PeriodStart.Equal(want.Start) ||
		settled.PeriodEnd == nil || !settled.PeriodEnd.Equal(want.End) {
		t.Fatalf("new cycle = %v..%v, want %+v", settled.PeriodStart, settled.PeriodEnd, want)
	}
	if used, err := f.st.UsedBytes(ctx, f.member.ID, &f.cur.Start); err != nil || used != 100 {
		t.Fatalf("closed-cycle usage = %d, %v; want the preserved 100", used, err)
	}
	if n, err := f.st.AdvancePeriods(ctx, f.cur.End); err != nil || n != 0 {
		t.Fatalf("second tick at the same boundary = %d, %v", n, err)
	}
}

// (2) Usage submitted after a rollover is booked into the new cycle instead of
// the previous period_start, and it does not revoke the member.
func TestUsageBatchBooksIntoAdvancedCycle(t *testing.T) {
	quota := int64(1 << 20)
	f := newRegFixture(t, &quota)
	ctx := context.Background()
	now := f.st.now()

	batch := contracts.UsageBatch{
		SchemaVersion: contracts.SchemaVersion,
		BatchID:       "batch-1",
		Samples: []contracts.UsageSample{{
			NodeID: f.node.ID, MemberID: f.member.ID,
			StartedAt: now.Add(-5 * time.Second), EndedAt: now,
			UploadBytes: 500, DownloadBytes: 500,
		}},
	}
	status, err := f.st.SubmitUsageBatch(ctx, f.agent, batch)
	if err != nil {
		t.Fatalf("submit usage: %v", err)
	}
	if status != "accepted" {
		t.Fatalf("status = %q, want accepted", status)
	}

	var rows int
	if err := f.st.DB.QueryRowContext(ctx, `SELECT count(*) FROM usage_entries
		WHERE member_id=$1 AND period_start=$2`, f.member.ID, f.cur.Start).Scan(&rows); err != nil {
		t.Fatalf("count new cycle rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("rows in the current cycle = %d, want 1", rows)
	}
	moved, err := f.st.GetMember(ctx, f.member.ID)
	if err != nil {
		t.Fatalf("reload member: %v", err)
	}
	if moved.PeriodStart == nil || !moved.PeriodStart.Equal(f.cur.Start) {
		t.Fatalf("period_start = %v, want %v", moved.PeriodStart, f.cur.Start)
	}
	res, err := f.st.EvaluateAccessForMember(ctx, moved)
	if err != nil {
		t.Fatalf("evaluate access: %v", err)
	}
	if !res.Decision.Access {
		t.Fatalf("a member well below quota must keep access: %v", res.Decision.Reasons)
	}
	if res.Evidence.UsedBytes != 1000 {
		t.Fatalf("used bytes = %d, want 1000", res.Evidence.UsedBytes)
	}
}

// seedBulkNodesAndMembers creates count nodes, one node group, one subscription
// and count members with one credential each in a single transaction, and returns
// the id of the last-sorted node. Doing this through the per object API cost one
// commit (one network round trip) per row, which dominated the test runtime on the
// isolated database and hid the query cost the test is about.
func seedBulkNodesAndMembers(t *testing.T, st *Store, serverID, agentID string, count int) (lastNodeID, subID string) {
	t.Helper()
	ctx := context.Background()
	cycle := period.Containing(1, time.UTC, st.now())
	secretEnc, err := st.encryptSecret("11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatalf("encrypt credential: %v", err)
	}
	hash, err := secrets.HashPassword("member-password-1")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	groupID := ids.New(ids.NodeGroup)
	subID = ids.New(ids.Subscription)
	lastNodeID = fmt.Sprintf("nd_%03d", count)
	err = st.inTx(ctx, func(tx *sql.Tx) error {
		// The last node sorts last so a truncated listing drops it first.
		if _, err := tx.ExecContext(ctx, `INSERT INTO nodes (id, server_id, name, enabled, host, port, listen, protocol)
			SELECT 'nd_'||lpad(g::text,3,'0'), $1,
			       CASE WHEN g = $2 THEN 'zz-last-node' ELSE 'node-'||lpad(g::text,3,'0') END,
			       true, '127.0.0.1', 10000 + g, '0.0.0.0', 'vless'
			FROM generate_series(1, $2::int) AS g`, serverID, count); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO core_instances (id, server_id, node_id, agent_id, state)
			SELECT 'ci_'||id, $1, id, $2, 'stopped' FROM nodes WHERE server_id=$1`, serverID, agentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO node_groups (id, name, description)
			VALUES ($1,'group-all','')`, groupID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO node_group_members (group_id, node_id, position)
			SELECT $1, id, (row_number() OVER (ORDER BY lower(name)) - 1)::int FROM nodes WHERE server_id=$2`,
			groupID, serverID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO subscriptions
			(id, name, timezone, anchor_day, allow_new_bindings, access_enabled)
			VALUES ($1,'sub-all','UTC',1,true,true)`, subID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO subscription_node_groups (subscription_id, node_group_id, position)
			VALUES ($1,$2,0)`, subID, groupID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO members (id, username, password_hash, role, active,
			proxy_enabled, subscription_id, period_start, period_end, period_timezone, period_anchor_day)
			SELECT 'mbr_'||lpad(g::text,3,'0'), 'user-'||lpad(g::text,3,'0'), $1, 'member', true, true,
			       $2, $3, $4, 'UTC', 1
			FROM generate_series(1, $5::int) AS g`, hash, subID, cycle.Start, cycle.End, count); err != nil {
			return err
		}
		// One credential per member on the last node: the snapshot must still carry
		// every node instance and every member.
		_, err := tx.ExecContext(ctx, `INSERT INTO credentials (id, member_id, node_id, kind, secret_enc, state)
			SELECT 'crd_'||id, id, $1, 'uuid', $2, 'active' FROM members WHERE subscription_id=$3`,
			lastNodeID, secretEnc, subID)
		return err
	})
	if err != nil {
		t.Fatalf("seed bulk fixture: %v", err)
	}
	return lastNodeID, subID
}

// (4) Internal authoritative queries must not be truncated at the public 200 row
// page limit, while the public listings keep their cap.
func TestSnapshotAndEvidenceAreNotTruncatedBeyond200(t *testing.T) {
	const count = 205
	st := newRegStore(t)
	ctx := context.Background()
	now := st.now()

	server := regServer(t, st, "server-a")
	agent := regAgent(t, st, server.ID)
	lastNodeID, _ := seedBulkNodesAndMembers(t, st, server.ID, agent.ID, count)

	snap, err := st.BuildSnapshot(ctx, agent)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if len(snap.Instances) != count {
		t.Fatalf("snapshot instances = %d, want %d (internal node listing truncated)", len(snap.Instances), count)
	}
	var lastInstance *contracts.Instance
	for i := range snap.Instances {
		if snap.Instances[i].NodeID == lastNodeID {
			lastInstance = &snap.Instances[i]
			break
		}
	}
	if lastInstance == nil {
		t.Fatal("the last node is missing from the snapshot")
	}
	if len(lastInstance.Users) != count {
		t.Fatalf("users on the last node = %d, want %d (internal member listing truncated)",
			len(lastInstance.Users), count)
	}
	if snap.ExpectedUserCount != count {
		t.Fatalf("expected_user_count = %d, want %d", snap.ExpectedUserCount, count)
	}
	for _, u := range lastInstance.Users {
		if u.MemberID == "" {
			t.Fatal("empty member id in snapshot users")
		}
	}
	seenLast := false
	for _, member := range mustMembers(t, st) {
		if member.Username == fmt.Sprintf("user-%03d", count) {
			seenLast = true
		}
	}
	if !seenLast {
		t.Fatal("the last member (beyond the 200 row page) is missing")
	}

	// The authorization evidence must also see every node.
	m, err := st.GetMember(ctx, lastInstance.Users[len(lastInstance.Users)-1].MemberID)
	if err != nil {
		t.Fatalf("get member: %v", err)
	}
	ev, err := st.Evidence(ctx, m)
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if len(ev.Nodes) != count {
		t.Fatalf("evidence nodes = %d, want %d", len(ev.Nodes), count)
	}

	// The bulk inputs used by the snapshot must agree with the single member path
	// (same ledger aggregate, same authorization set), otherwise the performance
	// fix would have changed decisions.
	batch, err := st.LoadEvidenceBatch(ctx, []domain.Member{*m})
	if err != nil {
		t.Fatalf("load evidence batch: %v", err)
	}
	bulk := batch.Evidence(m)
	if bulk.UsedBytes != ev.UsedBytes || bulk.AdjustBytes != ev.AdjustBytes {
		t.Fatalf("bulk evidence bytes = %d/%d, single = %d/%d",
			bulk.UsedBytes, bulk.AdjustBytes, ev.UsedBytes, ev.AdjustBytes)
	}
	bulkDecision := bulk.Decision(now)
	singleDecision := ev.Decision(now)
	if bulkDecision.Access != singleDecision.Access || bulkDecision.ProxyAccess != singleDecision.ProxyAccess {
		t.Fatalf("bulk decision %v/%s, single %v/%s", bulkDecision.Access, bulkDecision.ProxyAccess,
			singleDecision.Access, singleDecision.ProxyAccess)
	}
	if len(bulkDecision.AllowedNodeIDs) != len(singleDecision.AllowedNodeIDs) {
		t.Fatalf("bulk allowed nodes = %d, single = %d",
			len(bulkDecision.AllowedNodeIDs), len(singleDecision.AllowedNodeIDs))
	}
	allowed := map[string]bool{}
	for _, id := range singleDecision.AllowedNodeIDs {
		allowed[id] = true
	}
	for _, id := range bulkDecision.AllowedNodeIDs {
		if !allowed[id] {
			t.Fatalf("bulk decision allows node %s that the single path does not", id)
		}
	}

	// Public pagination keeps its documented cap.
	publicNodes, err := st.ListNodes(ctx, server.ID, 200)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(publicNodes) != 200 {
		t.Fatalf("public node page = %d, want the 200 cap", len(publicNodes))
	}
	publicMembers, err := st.ListMembers(ctx, 200, "", true)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(publicMembers) != 200 {
		t.Fatalf("public member page = %d, want the 200 cap", len(publicMembers))
	}
}

func mustAgent(t *testing.T, st *Store, id string) *domain.Agent {
	t.Helper()
	a, err := st.GetAgent(context.Background(), id)
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	return a
}

func mustMembers(t *testing.T, st *Store) []domain.Member {
	t.Helper()
	items, err := st.listAllMembers(context.Background(), "")
	if err != nil {
		t.Fatalf("list all members: %v", err)
	}
	return items
}

func TestSubscriptionRuleRepeatedEditsReplaceAndClearPending(t *testing.T) {
	f := newRegFixture(t, nil)
	ctx := context.Background()
	if _, err := f.st.UpdateMember(ctx, f.member.ID, 0, func(m *domain.Member) error {
		m.PeriodStart, m.PeriodEnd = &f.cur.Start, &f.cur.End
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, rule := range []struct {
		anchor   int
		timezone string
	}{{15, "Asia/Tokyo"}, {7, "Europe/London"}, {1, "UTC"}} {
		if _, err := f.st.UpdateSubscription(ctx, f.sub.ID, 0, func(sub *domain.Subscription) error {
			sub.AnchorDay, sub.Timezone = rule.anchor, rule.timezone
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		m, err := f.st.GetMember(ctx, f.member.ID)
		if err != nil {
			t.Fatal(err)
		}
		if m.PeriodStart == nil || !m.PeriodStart.Equal(f.cur.Start) ||
			m.PeriodEnd == nil || !m.PeriodEnd.Equal(f.cur.End) || m.PeriodAnchorDay != 1 || m.PeriodTimezone != "UTC" {
			t.Fatalf("repeat edit moved the running cycle: %+v", m)
		}
		if rule.anchor == 1 && rule.timezone == "UTC" {
			if m.PendingPeriodRule != nil {
				t.Fatalf("reverting to the running rule must clear the stale pending rule: %+v", m.PendingPeriodRule)
			}
		} else if p := m.PendingPeriodRule; p == nil || p.AnchorDay != rule.anchor || p.Timezone != rule.timezone {
			t.Fatalf("latest edit did not replace pending: got %+v, want %+v", p, rule)
		}
	}
}
