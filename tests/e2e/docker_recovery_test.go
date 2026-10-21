package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite"
	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/secrets"
	"proxypanel/internal/panel/store"
	"proxypanel/internal/panel/usagecalc"
)

// TestDockerRecovery is a destructive-to-TEST-FIXTURE, one-shot A14 exercise.
// Run ALONE with PANEL_DOCKER_TEST=1, PANEL_DOCKER_RECOVERY_TEST=1,
// PANEL_DOCKER_ROOT and XRAY_BIN. Parent installs deploy/docker-recovery.sh as
// recovery.sh beside control.sh and forwards HTTPS18444/PG25433 in addition to
// existing forwards. Requires no existing restore/recovery resources. Never
// delete them to make a failed run pass: retain and investigate the evidence.
// The third Agent is a full stopped-state TEST COPY, not a fresh registration.
func TestDockerRecovery(t *testing.T) {
	if os.Getenv("PANEL_DOCKER_RECOVERY_TEST") != "1" {
		t.Skip("explicit one-shot isolated recovery acceptance required")
	}
	if os.Getenv("XRAY_BIN") == "" {
		t.Fatal("XRAY_BIN required for real recovery proxy assertions")
	}
	d := dockerSetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	d.remote("agent1")
	d.remote("agent2")
	d.recoveryRemote("preflight")
	evidence := map[string]any{
		"status": "incomplete", "criterion": "A14/criterion9",
		"scope":             "isolated custom pg_dump restore; source is NOT destroyed; not production RPO/RTO",
		"agent_copy":        "ppaccept-20261021-agent1 -> ppaccept-20261021-recovery-agent (full stopped volume)",
		"source_pg_forward": 25432, "restore_pg_forward": 25433,
		"unsampled_core_tail": "not quantified; no zero-loss claim; no WAL/PITR configured by this test",
		"resources":           "all restore/copy volumes and evidence retained; recovery panel/Agent and the independent restore DB are stopped by cleanup",
	}
	evidencePath := filepath.Join(d.private, "recovery-evidence.json")
	// A failure never leaves an old successful report for this invocation.
	saveEvidence := func() {
		b, err := json.MarshalIndent(evidence, "", "  ")
		if err == nil {
			err = os.WriteFile(evidencePath, append(b, '\n'), 0600)
		}
		if err != nil {
			t.Error("write sanitized recovery evidence failed")
		}
	}
	saveEvidence()
	defer func() {
		if t.Failed() {
			evidence["status"] = "incomplete"
		}
		evidence["test_failed"] = t.Failed()
		saveEvidence()
	}()
	defer func() {
		// Stop the copy before releasing node1's existing loopback port to source.
		d.recoveryRemote("stop")
		d.remote("start panel")
		d.remote("start agent1")
		d.remote("start agent2")
	}()
	d.remote("start agent1")
	d.remote("start agent2")
	d.memberPatch(map[string]any{"active": true, "proxy_enabled": true, "expires_at": nil})
	d.applied()
	cfg := d.export("xray")
	// Pin all measured payloads to original Agent1's already-tunneled port.
	var node1 any
	for _, v := range cfg["outbounds"].([]any) {
		out, ok := v.(map[string]any)
		if !ok {
			continue
		}
		settings, ok := out["settings"].(map[string]any)
		if !ok {
			continue
		}
		next, ok := settings["vnext"].([]any)
		if !ok || len(next) == 0 {
			continue
		}
		server, ok := next[0].(map[string]any)
		if ok && server["port"] == float64(20101) {
			node1 = v
			break
		}
	}
	if node1 == nil {
		t.Fatal("fixture export lacks tunneled Agent1 VLESS port 20101")
	}
	cfg["outbounds"] = []any{node1}
	client, tr, _ := dockerProxy(t, cfg)
	fetch := func() bool { tr.CloseIdleConnections(); return dockerFetch(client) }
	before := d.ledger()
	waitFor(t, 25*time.Second, "real proxy before backup", fetch)
	waitFor(t, 25*time.Second, "pre-backup payload durably ACKed", func() bool { return d.ledger() >= before+69632 })
	time.Sleep(3 * time.Second) // no more traffic; allow upload ACK deletion
	d.remote("stop agent2")
	baseline := d.drainedSourceState("backup")
	if len(baseline.Queued) != 0 {
		t.Fatal("pre-backup Agent1 ACK queue did not drain")
	}
	backupLedger := recoveryLedger(t, d.db, d.fixture.Member)
	if backupLedger.total() <= before {
		t.Fatal("backup has no new measured usage")
	}
	backupStart := time.Now().UTC()
	d.remote("backup") // actual custom-format pg_dump, with Agents quiesced
	backupEnd := time.Now().UTC()
	assertRecoveryLedger(t, backupLedger, recoveryLedger(t, d.db, d.fixture.Member), "source changed across backup")
	evidence["backup_started_at"] = backupStart
	evidence["backup_completed_at"] = backupEnd
	evidence["backup_ledger_bytes"] = backupLedger.total()
	evidence["backup_ledger_sample_watermark"] = backupLedger.watermark()
	evidence["backup_batch_count"] = len(backupLedger)
	evidence["backup_batches"] = backupLedger

	// These ACKed batches are deliberately newer than the backup. They must NOT
	// be fabricated from a counter baseline or a queue that already deleted them.
	d.remote("start agent1")
	waitFor(t, 25*time.Second, "real post-backup proxy", fetch)
	waitFor(t, 25*time.Second, "post-backup durable usage", func() bool { return d.ledger() >= backupLedger.total()+69632 })
	time.Sleep(3 * time.Second)
	ackedState := d.drainedSourceState("acked-gap")
	if len(ackedState.Queued) != 0 {
		t.Fatal("post-backup batches were not ACK-deleted")
	}
	sourceLedger := recoveryLedger(t, d.db, d.fixture.Member)
	lost := recoveryLedgerSet{}
	for id, b := range sourceLedger {
		if _, ok := backupLedger[id]; !ok {
			lost[id] = b
		}
	}
	if lost.total() < 69632 || !sourceLedger.watermark().After(backupLedger.watermark()) {
		t.Fatal("missing measurable post-backup ACK-deleted loss window")
	}
	for id := range lost {
		if !strings.HasPrefix(id, baseline.AgentID+"/") {
			t.Fatal("post-backup traffic escaped dedicated Agent1")
		}
	}

	// Deliver a short REAL expiry after the backup, then interrupt only the
	// dedicated source panel. Cached authorization produces real queued payloads.
	// Natural local expiry makes the copied last-trusted authorization stricter
	// than the backup, without clearing its queue, baseline or original volume.
	d.remote("start agent1")
	d.remote("start agent2")
	// The VPS clock is not the Windows clock: deliver and judge the expiry with
	// the PostgreSQL server clock, exactly as the offline-expiry acceptance does.
	expiryBase, err := dockerServerNow(d.db)
	if err != nil {
		t.Fatalf("source PostgreSQL expiry clock query failed: %v", err)
	}
	expiry := expiryBase.Add(60 * time.Second).Truncate(time.Second)
	d.memberPatch(map[string]any{"expires_at": expiry.Format(time.RFC3339)})
	d.applied()
	if now, clockErr := dockerServerNow(d.db); clockErr != nil || !now.Before(expiry) {
		t.Fatalf("expiry setup consumed its delivery window (server_now=%v expiry=%s err=%v)", now, expiry.Format(time.RFC3339Nano), clockErr)
	}
	d.remote("stop agent2")
	d.remote("stop panel")
	// The offline window starts here. The Agent and PostgreSQL share the VPS
	// clock, so this server-clock barrier tells later apart whether the durable
	// queue really holds payloads sampled while the panel was unreachable.
	offlineSince, barrierErr := dockerServerNow(d.db)
	if barrierErr != nil {
		t.Fatalf("source PostgreSQL offline-window clock query failed: %v", barrierErr)
	}
	if !fetch() {
		t.Fatal("cached real proxy failed before delivered expiry")
	}
	time.Sleep(3 * time.Second)
	// A failed fetch is NOT the completion signal: it also happens while the
	// expiry rebuild has the old core stopped and the zero-user core is still
	// starting. Wait for the durable completion the Agent publishes itself
	// (persisted users already filtered, expiry trigger cleared) before copying
	// the stopped state, and prove the access denial separately afterwards.
	var lastStatus string
	statusDeadline := time.Now().Add(150 * time.Second)
	for {
		st, serr := d.agentStatus()
		if serr != nil {
			lastStatus = "state-report unavailable (rebuild the acceptance image with the status channel)"
		} else {
			lastStatus = st.summary()
			if st.tightened() {
				break
			}
		}
		if time.Now().After(statusDeadline) {
			t.Fatalf("the source Agent never published a persisted local expiry before the state copy (%s)", lastStatus)
		}
		time.Sleep(2 * time.Second)
	}
	evidence["source_status_before_copy"] = lastStatus
	waitFor(t, 45*time.Second, "denied access after the persisted local expiry", func() bool { return !fetch() })
	if now, clockErr := dockerServerNow(d.db); clockErr != nil {
		t.Fatalf("source PostgreSQL expiry clock query failed: %v", clockErr)
	} else if now.Before(expiry) {
		t.Fatalf("proxy rejected before its delivered expiry according to PostgreSQL: now=%s expiry=%s", now.Format(time.RFC3339Nano), expiry.Format(time.RFC3339Nano))
	}
	// One failed 200ms probe is not proof either: the denial must persist, so a
	// single transient client/proxy error can never let the copy happen early.
	for probe := 0; probe < 3; probe++ {
		if fetch() {
			t.Fatal("cached proxy was allowed again after the delivered expiry")
		}
		time.Sleep(300 * time.Millisecond)
	}
	d.remote("stop agent1")
	retained := d.recoveryState("source", "retained", d.fixture.Member)
	offlineQueued := retained.queuedAfter(offlineSince)
	// Only sanitized counters/state codes: never the token, a config or any
	// credential. These are recorded BEFORE the assertion so a failed run still
	// leaves the numbers in the sanitized evidence file for the operator.
	retainedSummary := retained.summary() + fmt.Sprintf(" offline_queue_batches=%d", offlineQueued)
	evidence["source_retained_diagnostics"] = retainedSummary
	saveEvidence()
	if retained.MemberUsers != 0 {
		t.Fatalf("natural local expiry did not persist the stricter authorization in the copied state: %s", retainedSummary)
	}
	if !retained.AllExpiryCleared() {
		t.Fatalf("the copied state still carries a user expiry trigger: %s", retainedSummary)
	}
	if retained.queuedBytes() < 69632 || offlineQueued == 0 {
		t.Fatalf("the panel-offline window produced no durable queued usage: %s", retainedSummary)
	}
	// The Agent never deletes counter baselines: they are written per core
	// generation and must survive an Agent restart (AC-REPORT-06), so a locally
	// enforced expiry must not silently clear them either.
	if retained.Baselines == 0 || retained.BaselineGenerations == 0 {
		t.Fatalf("counter baselines were lost across the local expiry: %s", retainedSummary)
	}
	if retained.AgentID != baseline.AgentID || retained.Generation != baseline.Generation || retained.Token == "" {
		t.Fatalf("source identity changed before recovery: %s", retainedSummary)
	}
	for id := range retained.Queued {
		if _, ok := sourceLedger[id]; ok {
			t.Fatalf("retained batch unexpectedly already ACKed: %s", retainedSummary)
		}
	}
	d.recoveryRemote("copy-agent")
	d.remote("start panel")
	waitRecoveryReady(t, d.api)
	d.api.login("admin", d.password)
	var revokeDBTime time.Time
	if d.db.QueryRow(ctx, `select now()`).Scan(&revokeDBTime) != nil {
		t.Fatal("source DB clock query")
	}
	d.memberPatch(map[string]any{"active": false, "proxy_enabled": false, "expires_at": nil})
	var auditID, actorID string
	var revokedAt time.Time
	if d.db.QueryRow(ctx, `select id,actor_id,created_at from audit_logs where action='member.update' and target_id=$1 and created_at >= $2 order by created_at desc limit 1`, d.fixture.Member, revokeDBTime).Scan(&auditID, &actorID, &revokedAt) != nil {
		t.Fatal("post-backup revocation has no operator audit")
	}
	var sourceActive, sourceProxy bool
	if d.db.QueryRow(ctx, `select active,proxy_enabled from members where id=$1`, d.fixture.Member).Scan(&sourceActive, &sourceProxy) != nil || sourceActive || sourceProxy {
		t.Fatal("source audited revocation did not persist")
	}
	assertRecoveryLedger(t, sourceLedger, recoveryLedger(t, d.db, d.fixture.Member), "source ledger changed while Agents stopped")
	evidence["source_revocation_audit_id"] = auditID
	evidence["source_revoked_at"] = revokedAt
	evidence["natural_expiry_at"] = expiry
	evidence["ack_deleted_gap_bytes"] = lost.total()
	evidence["ack_deleted_gap_batches"] = len(lost)
	evidence["retained_queue_bytes"] = retained.queuedBytes()
	evidence["retained_queue_batches"] = len(retained.Queued)
	evidence["retained_offline_queue_batches"] = offlineQueued
	evidence["retained_baselines"] = retained.Baselines
	evidence["retained_baseline_generations"] = retained.BaselineGenerations
	evidence["ack_deleted_batches"] = lost
	evidence["retained_queue"] = retained.Queued
	evidence["copied_agent_id"] = retained.AgentID

	// No restored panel exists yet. First open only the hard-coded independent
	// loopback target with the ORIGINAL master key, then atomically gate it.
	restoreStart := time.Now().UTC()
	evidence["restore_invoked_at"] = restoreStart
	d.remote("restore")
	restoredAt := time.Now().UTC()
	var private map[string]string
	raw, err := os.ReadFile(filepath.Join(d.private, "test.json"))
	if err != nil || json.Unmarshal(raw, &private) != nil {
		t.Fatal("read private runtime recovery fixture")
	}
	key, err := secrets.ParseKey(private["master_key"])
	if err != nil {
		t.Fatal("parse original runtime master key")
	}
	u := &url.URL{Scheme: "postgres", User: url.UserPassword("acceptance", private["pg_password"]), Host: "127.0.0.1:25433", Path: "/acceptance", RawQuery: "sslmode=disable"}
	st, err := store.Open(ctx, store.Options{DatabaseURL: u.String(), MasterKey: key, MigrationsDir: filepath.Join(d.root, "migrations"), MaxConns: 2, ConnectTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal("open isolated restore store failed (details suppressed)")
	}
	defer st.Close()
	old, err := st.GetControlState(ctx)
	if err != nil {
		t.Fatal("read restored control state")
	}
	gated, err := st.EnterRecoveryMode(ctx)
	if err != nil || !gated.RecoveryMode || gated.ControlGeneration <= 0 || gated.ControlGeneration > (1<<53)-1 || gated.ControlGeneration == old.ControlGeneration {
		t.Fatal("restore did not atomically enter a fresh exact-integer recovery generation")
	}
	generation := strconv.FormatInt(gated.ControlGeneration, 10)
	// Compare EVERY backed-up Agent epoch, not merely the panel's old epoch.
	var oldGenerations []int64
	rows, err := d.db.Query(ctx, `select control_generation from agents where not archived`)
	if err != nil {
		t.Fatal("read all source Agent generations")
	}
	for rows.Next() {
		var g int64
		if rows.Scan(&g) != nil {
			t.Fatal("scan source generation")
		}
		oldGenerations = append(oldGenerations, g)
	}
	rows.Close()
	if rows.Err() != nil || len(oldGenerations) != 2 {
		t.Fatal("unexpected source Agent population")
	}
	for _, g := range oldGenerations {
		if g == gated.ControlGeneration {
			t.Fatal("new generation collides with a live Agent")
		}
	}
	secondLocal := d.recoveryState("source2", "agent2-generation", d.fixture.Member)
	if secondLocal.Generation == "" || generation == secondLocal.Generation {
		t.Fatal("new generation collides with actual Agent2 local state")
	}
	evidence["old_agent_local_generations"] = []string{retained.Generation, secondLocal.Generation}
	if generation == retained.Generation {
		t.Fatal("new generation collides with copied local identity")
	}
	restoreDB, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal("connect independent restore pool")
	}
	defer restoreDB.Close()
	assertRecoveryLedger(t, backupLedger, recoveryLedger(t, restoreDB, d.fixture.Member), "restored backup watermark differs")
	creds, err := st.MemberCredentials(ctx, d.fixture.Member)
	if err != nil || len(creds) < 2 {
		t.Fatal("restored encrypted credentials unavailable")
	}
	for _, c := range creds {
		if c.Secret == "" {
			t.Fatal("original master key cannot decrypt restored credentials")
		}
	}
	stale, err := st.GetMember(ctx, d.fixture.Member)
	if err != nil || !stale.Active || !stale.ProxyEnabled || stale.ExpiresAt != nil {
		t.Fatal("backup does not contain the deliberately stale authorization")
	}
	evidence["restore_loaded_at"] = restoredAt
	evidence["recovery_gated_at"] = time.Now().UTC()
	evidence["backup_generation"] = old.ControlGeneration
	evidence["restored_generation"] = gated.ControlGeneration
	d.recoveryRemote("panel-up") // script independently checks persisted gate
	jar, _ := cookiejar.New(nil)
	api := &platformAPI{t: t, base: "https://127.0.0.1:18444", client: &http.Client{Transport: d.transport, Jar: jar, Timeout: 15 * time.Second}}
	waitRecoveryReady(t, api)
	api.login("admin", d.password)
	recoveryHTTPStatus(t, api, "GET", "/api/v1/agent/snapshot", nil, retained.Token, 409)
	recoveryHTTPStatus(t, api, "PATCH", "/api/v1/admin/members/"+d.fixture.Member, map[string]any{"version": stale.Version, "active": true}, "", 409)
	recoveryHTTPStatus(t, api, "POST", "/api/v1/admin/recovery-mode/exit", map[string]any{"confirm": "verified"}, "", 400)

	// Explicit operator TOOLKIT replay under the still-active gate; not an API
	// write bypass, and never a source DB update. Link the exact source audit.
	_, err = st.UpdateMember(ctx, stale.ID, stale.Version, func(m *domain.Member) error { m.Active = false; m.ProxyEnabled = false; m.ExpiresAt = nil; return nil })
	if err != nil {
		t.Fatal("operator revocation replay failed")
	}
	detail := domain.NewObject()
	_ = detail.Set("source_audit_id", auditID)
	_ = detail.Set("active", false)
	_ = detail.Set("proxy_enabled", false)
	_ = detail.Set("ack_deleted_gap_bytes", lost.total())
	_ = detail.Set("retained_queue_bytes", retained.queuedBytes())
	if st.RecordAudit(ctx, actorID, "admin", "recovery.replay_revocation", "member", stale.ID, detail, "127.0.0.1", "A14-20261021") != nil {
		t.Fatal("replay audit persistence failed")
	}
	var replayAudits int
	if st.DB.QueryRowContext(ctx, `select count(*) from audit_logs where action='recovery.replay_revocation' and target_id=$1 and detail->>'source_audit_id'=$2`, stale.ID, auditID).Scan(&replayAudits) != nil || replayAudits != 1 {
		t.Fatal("operator replay audit link missing")
	}
	a, err := st.GetAgent(ctx, retained.AgentID)
	if err != nil {
		t.Fatal("restored copied Agent registration missing")
	}
	if _, err = st.BuildSnapshot(ctx, a); err == nil {
		t.Fatal("replay accidentally removed snapshot gate")
	}
	api.request("POST", "/api/v1/admin/recovery-mode/exit", map[string]any{"confirm": "verified", "agent_revisions_checked": true, "ledger_checked": true})
	a, err = st.GetAgent(ctx, retained.AgentID)
	if err != nil {
		t.Fatal("read replayed Agent")
	}
	snap, err := st.BuildSnapshot(ctx, a)
	if err != nil || snap.ControlGeneration != generation || snap.ExpectedUserCount != 0 {
		t.Fatal("replayed authorization still contains users")
	}

	// First start deliberately WITHOUT accepting the new epoch. Real supervisor
	// must report coordination_required, retain its stricter empty permissions,
	// and replay only its surviving durable queue (not the deleted ACK gap).
	d.recoveryRemote("agent-start")
	waitFor(t, 45*time.Second, "real copied Agent generation refusal", func() bool {
		var n int
		if st.DB.QueryRowContext(ctx, `select count(*) from agent_receipts where agent_id=$1 and state='coordination_required' and payload->>'control_generation'=$2`, retained.AgentID, generation).Scan(&n) != nil {
			t.Fatal("query coordination receipt")
		}
		return n > 0
	})
	expected := recoveryLedgerSet{}
	for id, b := range backupLedger {
		expected[id] = b
	}
	for id, b := range retained.Queued {
		expected[id] = b
	}
	waitFor(t, 40*time.Second, "retained queue replayed by real Agent", func() bool {
		got := recoveryLedger(t, restoreDB, d.fixture.Member)
		if got.total() > expected.total() {
			t.Fatal("queue replay double-counted usage")
		}
		return reflect.DeepEqual(expected, got)
	})
	replayed := recoveryLedger(t, restoreDB, d.fixture.Member)
	assertRecoveryLedger(t, expected, replayed, "retained queue payload hash/ID/bytes/watermark differ")
	for id := range lost {
		if _, ok := replayed[id]; ok {
			t.Fatal("ACK-deleted gap was fabricated/replayed")
		}
	}
	// The uploader deletes a local batch only after the panel durably ACKed it.
	// Require a heartbeat newer than this barrier to prove the copied Agent's
	// queue, not merely the restored ledger, is empty before stopping it.
	var queueBarrier time.Time
	if st.DB.QueryRow(`select now()`).Scan(&queueBarrier) != nil {
		t.Fatal("read copied Agent queue barrier")
	}
	waitFor(t, 20*time.Second, "replayed queue ACK-deleted from copied Agent", func() bool {
		var depth int64
		var heartbeat time.Time
		if st.DB.QueryRow(`select queue_depth,last_heartbeat_at from agents where id=$1`, retained.AgentID).Scan(&depth, &heartbeat) != nil {
			return false
		}
		return heartbeat.After(queueBarrier) && depth == 0
	})
	waitRecoveryEmpty(t, st, retained.AgentID)
	if fetch() {
		t.Fatal("running unaccepted recovery Agent resurrected expired/revoked access")
	}
	d.recoveryRemote("agent-stop")
	refused := d.recoveryState("copy", "unaccepted", d.fixture.Member)
	if refused.AgentID != retained.AgentID || refused.Token != retained.Token || refused.Generation != retained.Generation || refused.PendingGeneration != generation || refused.MemberUsers != 0 || len(refused.Queued) != 0 {
		t.Fatal("real Agent accepted epoch implicitly, resurrected users, or failed ACK deletion")
	}
	d.recoveryRemote("accept " + generation)
	d.recoveryRemote("agent-start")
	waitFor(t, 45*time.Second, "explicitly accepted epoch applied by real Agent", func() bool {
		var applied, desired, g int64
		if st.DB.QueryRowContext(ctx, `select applied_revision,desired_revision,control_generation from agents where id=$1`, retained.AgentID).Scan(&applied, &desired, &g) != nil {
			t.Fatal("query accepted Agent watermark")
		}
		return applied == desired && applied > 0 && g == gated.ControlGeneration
	})
	// A denial alone could mean a dead Agent: require its fresh heartbeat with
	// a successfully applied zero-user core, as well as the real proxy denial.
	waitRecoveryEmpty(t, st, retained.AgentID)
	for n := 0; n < 3; n++ {
		if fetch() {
			t.Fatal("explicit acceptance resurrected revoked proxy credentials")
		}
		time.Sleep(time.Second)
	}
	d.recoveryRemote("agent-stop")
	accepted := d.recoveryState("copy", "accepted", d.fixture.Member)
	if accepted.AgentID != retained.AgentID || accepted.Token != retained.Token || accepted.Generation != generation || accepted.PendingGeneration != "" || accepted.AcceptedGeneration != "" || accepted.MemberUsers != 0 || len(accepted.Queued) != 0 {
		t.Fatal("accepted Agent durable state not reconciled")
	}
	d.recoveryRemote("agent-start") // a further real restart cannot restore old auth
	waitRecoveryEmpty(t, st, retained.AgentID)
	if fetch() {
		t.Fatal("recovery Agent restart revived revoked access")
	}
	assertRecoveryLedger(t, expected, recoveryLedger(t, restoreDB, d.fixture.Member), "replay/restart changed ledger or double-counted")
	assertRecoveryLedger(t, sourceLedger, recoveryLedger(t, d.db, d.fixture.Member), "restore wrote source ledger")
	originalPreserved := d.recoveryState("source", "original-preserved", d.fixture.Member)
	if !reflect.DeepEqual(retained, originalPreserved) {
		t.Fatal("restore modified or cleared the ORIGINAL Agent state")
	}
	if d.db.QueryRow(ctx, `select active,proxy_enabled from members where id=$1`, d.fixture.Member).Scan(&sourceActive, &sourceProxy) != nil || sourceActive || sourceProxy {
		t.Fatal("restore changed source revocation")
	}
	var sourceGeneration int64
	if d.db.QueryRow(ctx, `select control_generation from control_state where id=1 and not recovery_mode`).Scan(&sourceGeneration) != nil || sourceGeneration != old.ControlGeneration {
		t.Fatal("restore changed source control state")
	}
	verifiedAt := time.Now().UTC()
	evidence["verified_at"] = verifiedAt
	evidence["rto_seconds_restore_invocation_to_verified_agent_restart"] = verifiedAt.Sub(restoreStart).Seconds()
	evidence["dump_restore_seconds"] = restoredAt.Sub(restoreStart).Seconds()
	evidence["rpo_backup_age_at_restore_seconds_lower_bound"] = restoreStart.Sub(backupEnd).Seconds()
	evidence["rpo_backup_age_at_restore_seconds_upper_bound"] = restoreStart.Sub(backupStart).Seconds()
	evidence["recovered_batches"] = replayed
	evidence["rpo_definition"] = "quiesced backup snapshot lies in dump start/end bracket; ACK sample window is accounting loss, later replayed samples do not close that hole"
	evidence["rpo_seconds_last_source_acked_sample_minus_backup_sample"] = sourceLedger.watermark().Sub(backupLedger.watermark()).Seconds()
	evidence["source_acked_sample_watermark"] = sourceLedger.watermark()
	evidence["replayed_sample_watermark_not_contiguous"] = replayed.watermark()
	evidence["recovered_ledger_bytes"] = replayed.total()
	evidence["ack_deleted_gap_disposition"] = "unrecoverable from Agent queue; deliberately not imported from surviving source; needs newer PG backup/WAL or independent durable records"
	evidence["agent2_disposition"] = "original volume preserved; not pointed at restore or falsely marked coordinated; only Agent1 test copy exercised"
	evidence["status"] = "passed"
	t.Logf("A14 backup=%dB retained/replayed=%dB (%d offline batch(es)) ACK-deleted unrecoverable=%dB baselines=%d RPO-window=%.3fs exercise-RTO=%.3fs; sanitized evidence: %s", backupLedger.total(), retained.queuedBytes(), offlineQueued, lost.total(), retained.Baselines, sourceLedger.watermark().Sub(backupLedger.watermark()).Seconds(), verifiedAt.Sub(restoreStart).Seconds(), evidencePath)
}

type recoveryBatch struct {
	Bytes     int64
	Hash      string
	EndMillis int64
}
type recoveryLedgerSet map[string]recoveryBatch

func (s recoveryLedgerSet) total() int64 {
	var n int64
	for _, b := range s {
		n += b.Bytes
	}
	return n
}
func (s recoveryLedgerSet) watermark() time.Time {
	var n int64
	for _, b := range s {
		if b.EndMillis > n {
			n = b.EndMillis
		}
	}
	return time.UnixMilli(n).UTC()
}
func recoveryLedger(t *testing.T, db *pgxpool.Pool, member string) recoveryLedgerSet {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := db.Query(ctx, `select u.agent_id,u.batch_id,b.payload_sha256,sum(u.up_bytes+u.down_bytes),max(u.sample_ended_at) from usage_entries u join usage_batches b on b.agent_id=u.agent_id and b.batch_id=u.batch_id where u.member_id=$1 group by u.agent_id,u.batch_id,b.payload_sha256`, member)
	if err != nil {
		t.Fatal("query acceptance ledger")
	}
	defer rows.Close()
	out := recoveryLedgerSet{}
	for rows.Next() {
		var agent, batch string
		var b recoveryBatch
		var end time.Time
		if rows.Scan(&agent, &batch, &b.Hash, &b.Bytes, &end) != nil {
			t.Fatal("scan acceptance ledger")
		}
		b.EndMillis = end.UnixMilli()
		out[agent+"/"+batch] = b
	}
	if rows.Err() != nil {
		t.Fatal("read acceptance ledger")
	}
	return out
}
func assertRecoveryLedger(t *testing.T, want, got recoveryLedgerSet, message string) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatal(message)
	}
}

// recoveryInstanceState is the sanitized view of one persisted instance record:
// state code and counters only, never the config, the users blob or an error
// string.
type recoveryInstanceState struct {
	State             string
	AppliedUsers      int
	NextExpiryCleared bool
	HasError          bool
}

type recoveryLocalState struct {
	AgentID, Token, Generation, PendingGeneration, AcceptedGeneration string
	Queued                                                            recoveryLedgerSet
	MemberUsers, Baselines, BaselineGenerations, InstanceErrors       int
	Instances                                                         []recoveryInstanceState
}

func (s recoveryLocalState) queuedBytes() int64 {
	var n int64
	for _, b := range s.Queued {
		n += b.Bytes
	}
	return n
}

// queuedAfter counts durable pending batches whose last sample ended at or after
// the cutoff, i.e. payloads sampled while the panel was already unreachable.
func (s recoveryLocalState) queuedAfter(cutoff time.Time) int {
	n := 0
	for _, b := range s.Queued {
		if b.EndMillis >= cutoff.UnixMilli() {
			n++
		}
	}
	return n
}

// AllExpiryCleared reports that no persisted instance still carries a user
// expiry trigger, i.e. the locally enforced expiry was durably committed.
func (s recoveryLocalState) AllExpiryCleared() bool {
	if len(s.Instances) == 0 {
		return false
	}
	for _, inst := range s.Instances {
		if !inst.NextExpiryCleared {
			return false
		}
	}
	return true
}

// summary renders sanitized counters and state codes for failure messages and
// the evidence file. It never includes the token, a config or a credential.
func (s recoveryLocalState) summary() string {
	parts := make([]string, 0, len(s.Instances))
	for _, inst := range s.Instances {
		parts = append(parts, fmt.Sprintf("%s/users=%d/expiry_cleared=%t/error=%t", inst.State, inst.AppliedUsers, inst.NextExpiryCleared, inst.HasError))
	}
	return fmt.Sprintf("member_users=%d queued_bytes=%d queue_batches=%d baselines=%d baseline_generations=%d instance_errors=%d instances=[%s]",
		s.MemberUsers, s.queuedBytes(), len(s.Queued), s.Baselines, s.BaselineGenerations, s.InstanceErrors, strings.Join(parts, ","))
}

// agentStatus is the sanitized durable-state report the source Agent publishes
// beside its SQLite database. It carries state codes and counters only: the
// recovery acceptance never reads a live WAL database and never needs the token.
type agentStatus struct {
	Schema    int `json:"schema"`
	Instances []struct {
		State         string `json:"state"`
		Users         int    `json:"users"`
		AppliedUsers  int    `json:"applied_users"`
		ExpiryCleared bool   `json:"expiry_cleared"`
		HasError      bool   `json:"error"`
	} `json:"instances"`
}

// tightened reports that every persisted instance already dropped its expired
// authorization and cleared its expiry trigger, without a failed rebuild.
func (s agentStatus) tightened() bool {
	if s.Schema != 1 || len(s.Instances) == 0 {
		return false
	}
	for _, inst := range s.Instances {
		if inst.Users != 0 || !inst.ExpiryCleared || inst.HasError || inst.State == "failed" || inst.State == "rollback" {
			return false
		}
	}
	return true
}

// summary renders only sanitized counters and state codes.
func (s agentStatus) summary() string {
	parts := make([]string, 0, len(s.Instances))
	for _, inst := range s.Instances {
		parts = append(parts, fmt.Sprintf("%s/users=%d/applied=%d/expiry_cleared=%t/error=%t", inst.State, inst.Users, inst.AppliedUsers, inst.ExpiryCleared, inst.HasError))
	}
	return fmt.Sprintf("schema=%d instances=[%s]", s.Schema, strings.Join(parts, ","))
}

// agentStatus reads the published sanitized state report of the source Agent.
func (d *dockerTest) agentStatus() (agentStatus, error) {
	d.t.Helper()
	out, err := d.recoveryRemoteOutput("state-report")
	if err != nil {
		return agentStatus{}, err
	}
	var st agentStatus
	if json.Unmarshal([]byte(out), &st) != nil {
		return agentStatus{}, fmt.Errorf("decode sanitized state report")
	}
	return st, nil
}

// recoveryRemoteOutput runs a dedicated recovery action and returns its stdout.
// Only the sanitized state-report action may use it: remote diagnostics that
// could contain a DSN or private material are never echoed.
func (d *dockerTest) recoveryRemoteOutput(action string) (string, error) {
	d.t.Helper()
	if action != "state-report" {
		d.t.Fatalf("invalid dedicated read-only recovery action")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	a := append(d.sshArgs(), "root@192.168.5.203", "sh "+dockerRemote+"/recovery.sh "+action)
	b, err := exec.CommandContext(ctx, "ssh", a...).Output()
	if err != nil {
		return "", fmt.Errorf("dedicated read-only recovery action %q failed (output suppressed)", action)
	}
	return strings.TrimSpace(string(b)), nil
}
func (d *dockerTest) recoveryState(which, stage, member string) recoveryLocalState {
	d.t.Helper()
	action, remoteName := "export-source", "agent1"
	if which == "copy" {
		action, remoteName = "export-copy", "recovery-agent"
	} else if which == "source2" {
		action, remoteName = "export-source2", "agent2"
	} else if which != "source" {
		d.t.Fatal("invalid recovery state source")
	}
	d.recoveryRemote(action)
	path := filepath.Join(d.private, "recovery-"+stage+".db")
	a := append(d.sshArgs(), "root@192.168.5.203:"+dockerRemote+"/recovery/"+remoteName+".db", path)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if exec.CommandContext(ctx, "scp", a...).Run() != nil {
		d.t.Fatal("private stopped recovery state download failed")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		d.t.Fatal("open private stopped state")
	}
	defer db.Close()
	out := recoveryLocalState{Queued: recoveryLedgerSet{}}
	for k, dest := range map[string]*string{"agent_id": &out.AgentID, "token": &out.Token, "control_generation": &out.Generation, "pending_generation": &out.PendingGeneration, "accepted_generation": &out.AcceptedGeneration} {
		err := db.QueryRowContext(ctx, `select value from meta where key=?`, k).Scan(dest)
		if err != nil && err != sql.ErrNoRows {
			d.t.Fatal("read private state metadata")
		}
	}
	if db.QueryRowContext(ctx, `select count(*) from baseline`).Scan(&out.Baselines) != nil {
		d.t.Fatal("read counter baselines")
	}
	if db.QueryRowContext(ctx, `select count(distinct core_generation) from baseline`).Scan(&out.BaselineGenerations) != nil {
		d.t.Fatal("read counter baseline generations")
	}
	instanceRows, instanceErr := db.QueryContext(ctx, `select state, applied_user_count, next_expiry, coalesce(error,'') from instances order by node_id`)
	if instanceErr != nil {
		d.t.Fatal("read persisted instance state")
	}
	for instanceRows.Next() {
		var inst recoveryInstanceState
		var nextExpiry int64
		var errText string
		if instanceRows.Scan(&inst.State, &inst.AppliedUsers, &nextExpiry, &errText) != nil {
			d.t.Fatal("scan persisted instance state")
		}
		inst.NextExpiryCleared = nextExpiry == 0
		inst.HasError = strings.TrimSpace(errText) != ""
		if inst.HasError {
			out.InstanceErrors++
		}
		out.Instances = append(out.Instances, inst)
	}
	instanceRows.Close()
	if instanceRows.Err() != nil {
		d.t.Fatal("iterate persisted instance state")
	}
	rows, err := db.QueryContext(ctx, `select b.batch_id,coalesce(sum(s.upload+s.download),0) from batches b join samples s on s.batch_id=b.batch_id where b.state='pending' and s.member_id=? group by b.batch_id`, member)
	if err != nil {
		d.t.Fatal("read durable recovery queue")
	}
	for rows.Next() {
		var id string
		var n int64
		if rows.Scan(&id, &n) != nil {
			d.t.Fatal("scan durable queue")
		}
		out.Queued[out.AgentID+"/"+id] = recoveryBatch{Bytes: n}
	}
	rows.Close()
	if rows.Err() != nil {
		d.t.Fatal("iterate durable queue")
	}
	var batchCount int
	if db.QueryRowContext(ctx, `select count(*) from batches`).Scan(&batchCount) != nil || batchCount != len(out.Queued) {
		d.t.Fatal("unexpected non-fixture/quarantined batch in dedicated Agent queue")
	}
	// Reconstruct precisely the uploader's durable payload, not merely its sum.
	for id, total := range out.Queued {
		batchID := strings.TrimPrefix(id, out.AgentID+"/")
		batch := contracts.UsageBatch{SchemaVersion: contracts.SchemaVersion, BatchID: batchID}
		samples, err := db.QueryContext(ctx, `select node_id,member_id,core_generation,started_at,ended_at,upload,download from samples where batch_id=? order by seq`, batchID)
		if err != nil {
			d.t.Fatal("read durable payload")
		}
		for samples.Next() {
			var s contracts.UsageSample
			var start, end int64
			if samples.Scan(&s.NodeID, &s.MemberID, &s.CoreGeneration, &start, &end, &s.UploadBytes, &s.DownloadBytes) != nil || s.MemberID != member {
				d.t.Fatal("unexpected durable payload ownership")
			}
			s.StartedAt, s.EndedAt = time.UnixMilli(start).UTC(), time.UnixMilli(end).UTC()
			batch.Samples = append(batch.Samples, s)
			if end > total.EndMillis {
				total.EndMillis = end
			}
		}
		samples.Close()
		if samples.Err() != nil {
			d.t.Fatal("iterate durable payload")
		}
		total.Hash = usagecalc.PayloadSHA256(batch)
		out.Queued[id] = total
	}
	rows, err = db.QueryContext(ctx, `select users from instances`)
	if err != nil {
		d.t.Fatal("read persisted instance permissions")
	}
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			d.t.Fatal("scan instance permissions")
		}
		var users []contracts.AuthorizedUser
		if len(raw) > 0 && json.Unmarshal(raw, &users) != nil {
			d.t.Fatal("decode persisted permissions")
		}
		for _, u := range users {
			if u.MemberID == member {
				out.MemberUsers++
			}
		}
	}
	rows.Close()
	if rows.Err() != nil {
		d.t.Fatal("iterate persisted permissions")
	}
	return out
}

// drainedSourceState exports source Agent1 after a clean stop. If the final
// statistics interval left one pending batch, it restarts the SAME owned Agent
// so the real uploader can obtain its ACK and delete it, then stops it again.
// No proxy traffic is generated during this drain, so no new usage is created.
func (d *dockerTest) drainedSourceState(stage string) recoveryLocalState {
	d.t.Helper()
	d.remote("stop agent1")
	var state recoveryLocalState
	for attempt := 0; attempt < 3; attempt++ {
		state = d.recoveryState("source", stage, d.fixture.Member)
		if len(state.Queued) == 0 {
			return state
		}
		d.remote("start agent1")
		time.Sleep(5 * time.Second)
		d.remote("stop agent1")
	}
	d.t.Fatalf("%s: source Agent1 ACK queue did not drain", stage)
	return state
}
func (d *dockerTest) recoveryRemote(action string) {
	d.t.Helper()
	allowed := map[string]bool{"preflight": true, "export-source": true, "export-source2": true, "copy-agent": true, "panel-up": true, "agent-start": true, "agent-stop": true, "export-copy": true, "stop": true}
	if !allowed[action] {
		parts := strings.Split(action, " ")
		if len(parts) != 2 || parts[0] != "accept" {
			d.t.Fatal("invalid dedicated recovery action")
		}
		n, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || n <= 0 || n > (1<<53)-1 || strconv.FormatInt(n, 10) != parts[1] {
			d.t.Fatal("invalid accepted generation")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	a := append(d.sshArgs(), "root@192.168.5.203", "sh "+dockerRemote+"/recovery.sh "+action)
	// Never echo remote diagnostics that might contain a DSN or private material.
	if exec.CommandContext(ctx, "ssh", a...).Run() != nil {
		d.t.Fatalf("dedicated recovery action %q failed (private output suppressed)", action)
	}
}
func waitRecoveryEmpty(t *testing.T, st *store.Store, agentID string) {
	t.Helper()
	// Take the barrier on the same PostgreSQL clock as heartbeat persistence.
	var barrier time.Time
	if st.DB.QueryRow(`select now()`).Scan(&barrier) != nil {
		t.Fatal("read heartbeat barrier")
	}
	waitFor(t, 25*time.Second, "fresh recovered zero-user core heartbeat", func() bool {
		var n int
		if st.DB.QueryRow(`select count(*) from agents a, jsonb_array_elements(a.instances) i where a.id=$1 and a.last_heartbeat_at>$2 and i->>'state'='applied' and (i->>'applied_user_count')::int=0`, agentID, barrier).Scan(&n) != nil {
			t.Fatal("query live empty recovered core")
		}
		return n > 0
	})
}
func waitRecoveryReady(t *testing.T, api *platformAPI) {
	t.Helper()
	waitFor(t, 45*time.Second, "isolated HTTPS readiness", func() bool {
		r, err := api.client.Get(api.base + "/readyz")
		if err != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == 200
	})
}
func recoveryHTTPStatus(t *testing.T, api *platformAPI, method, path string, body any, bearer string, want int) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal("encode gate assertion")
	}
	req, err := http.NewRequest(method, api.base+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal("construct gate assertion")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", api.csrf)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	r, err := api.client.Do(req)
	if err != nil {
		t.Fatal("HTTPS gate assertion failed")
	}
	defer r.Body.Close()
	if want == http.StatusConflict {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&envelope) != nil || envelope.Error.Code != domain.CodeRecoveryMode {
			t.Fatal("409 was not the persisted recovery gate")
		}
	} else {
		io.Copy(io.Discard, io.LimitReader(r.Body, 64<<10))
	}
	if r.StatusCode != want {
		t.Fatal(fmt.Sprintf("recovery gate %s %s status=%d want=%d", method, path, r.StatusCode, want))
	}
}
