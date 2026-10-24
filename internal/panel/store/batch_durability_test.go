package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/period"
)

func durabilityRequest(t *testing.T, f *retryFixture) BatchSubmitRequest {
	t.Helper()
	var req BatchSubmitRequest
	if err := f.st.DB.QueryRow(`SELECT preview_id,idempotency_key FROM batches WHERE id=$1`, f.batchID).Scan(&req.PreviewID, &req.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	req.Confirm = true
	return req
}

func durabilityCode(t *testing.T, err error, code string) {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}

func TestBatchDurabilityReplayAcrossCleanup(t *testing.T) {
	f := newRetryFixture(t)
	st, ctx := f.st, context.Background()
	req := durabilityRequest(t, f)
	// The first response can be lost after commit. Replay must use the committed
	// submission receipt, not the now-timed-out mutable batch history.
	var raw []byte
	if err := st.DB.QueryRow(`SELECT result FROM batch_idempotency WHERE batch_id=$1`, f.batchID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var original BatchSubmitResult
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	replay, err := st.SubmitBatch(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replay {
		t.Fatal("lost response was not replayed")
	}
	replay.Replay = false
	if !reflect.DeepEqual(*replay, original) {
		t.Fatalf("replay changed submission result: got %+v, want %+v", replay, original)
	}
	version := retryScalarInt(t, st, `SELECT version FROM members WHERE id=$1`, f.member.ID)
	rev := retryScalarInt(t, st, `SELECT revision_counter FROM control_state WHERE id=1`)
	upgradeExec(t, st, `UPDATE batches SET completed_at=now()-interval '40 days' WHERE id=$1`, f.batchID)
	var stats CleanupStats
	err = st.InMaintenanceTx(ctx, DefaultMaintenanceBounds(), func(ctx context.Context, m *MaintenanceTx) error {
		var err error
		stats, err = m.Cleanup(ctx, 7, time.Now().UTC())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Batches != 1 || stats.BatchItems < 1 {
		t.Fatalf("cleanup did not remove history: %+v", stats)
	}
	if n := retryScalarInt(t, st, `SELECT count(*) FROM batches WHERE id=$1`, f.batchID); n != 0 {
		t.Fatal("batch survived cleanup")
	}
	if n := retryScalarInt(t, st, `SELECT count(*) FROM batch_idempotency WHERE batch_id=$1`, f.batchID); n != 1 {
		t.Fatal("permanent receipt lost")
	}
	replay, err = st.SubmitBatch(ctx, req)
	if err != nil || !replay.Replay {
		t.Fatalf("cross-retention replay: %+v %v", replay, err)
	}
	replay.Replay = false
	if !reflect.DeepEqual(*replay, original) {
		t.Fatal("cross-retention result differs from committed response")
	}
	for _, change := range []func(*BatchSubmitRequest){
		func(r *BatchSubmitRequest) { r.PreviewID = "different-preview" },
		func(r *BatchSubmitRequest) { r.Confirm = false },
		func(r *BatchSubmitRequest) { r.RulesVersion++ },
		func(r *BatchSubmitRequest) { r.MemberSetDigest = "different-digest" },
	} {
		altered := req
		change(&altered)
		_, err := st.SubmitBatch(ctx, altered)
		durabilityCode(t, err, domain.CodeIdempotencyKeyReused)
	}
	if retryScalarInt(t, st, `SELECT version FROM members WHERE id=$1`, f.member.ID) != version || retryScalarInt(t, st, `SELECT revision_counter FROM control_state WHERE id=1`) != rev {
		t.Fatal("replay re-applied business change or republished authorization")
	}
	// A permanent receipt proves the retained task's parent; actual orphans are
	// still blockers and the normal migration proof has not been bypassed.
	ready := upgradeReady(t, st)
	if upgradeHas(ready, "TASK_ORPHAN") {
		t.Fatal("cleaned, durably proven task was misclassified as orphan")
	}
	upgradeExec(t, st, `INSERT INTO tasks(id,kind,batch_id) VALUES ('actual-orphan','authorization.sync','never-existed')`)
	if !upgradeHas(upgradeReady(t, st), "TASK_ORPHAN") {
		t.Fatal("unproven task orphan was accepted")
	}
}

func TestBatchDurabilityRetryRevisionIsolation(t *testing.T) {
	f := newRetryFixture(t)
	st, ctx := f.st, context.Background()
	task, err := st.RetryTask(ctx, f.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != f.taskID || task.Revision == nil || *task.Revision <= f.revision || task.Attempts != 1 {
		t.Fatalf("retry identity/revision/attempts: %+v", task)
	}
	rev := *task.Revision
	if got := retryScalarInt(t, st, `SELECT revision FROM batch_items WHERE id=$1`, f.itemID); got != rev {
		t.Fatalf("item revision=%d, task revision=%d", got, rev)
	}
	for _, state := range []string{"failed", "applied", "applied_with_errors"} {
		if err := st.RecordApplyReport(ctx, f.agent.ID, contracts.ApplyReport{Revision: f.revision, State: state}); err != nil {
			t.Fatal(err)
		}
		if got := retryScalarString(t, st, `SELECT state FROM tasks WHERE id=$1`, f.taskID); got != "pending" {
			t.Fatalf("old %s receipt completed retried task: %s", state, got)
		}
		if got := retryScalarString(t, st, `SELECT state FROM batch_items WHERE id=$1`, f.itemID); got != "pending" {
			t.Fatalf("old receipt completed new item: %s", got)
		}
	}
	durabilityCode(t, st.RecordApplyReport(ctx, f.agent.ID, contracts.ApplyReport{Revision: rev + 1, State: "applied"}), domain.CodeRevisionStale)
	upgradeExec(t, st, `INSERT INTO tasks(id,kind,agent_id,revision) VALUES ('unrelated','other.operation',$1,$2)`, f.agent.ID, rev)
	other := regAgent(t, st, regServer(t, st, "unrelated-server").ID)
	upgradeExec(t, st, `UPDATE agents SET desired_revision=$2 WHERE id=$1`, other.ID, rev)
	if err := st.RecordApplyReport(ctx, other.ID, contracts.ApplyReport{Revision: rev, State: "applied"}); err != nil {
		t.Fatal(err)
	}
	if retryScalarString(t, st, `SELECT state FROM tasks WHERE id=$1`, f.taskID) != "pending" {
		t.Fatal("other agent completed task")
	}
	if err := st.RecordApplyReport(ctx, f.agent.ID, contracts.ApplyReport{Revision: rev, State: "applied_with_errors"}); err != nil {
		t.Fatal(err)
	}
	if retryScalarString(t, st, `SELECT state FROM tasks WHERE id=$1`, f.taskID) != "failed" || retryScalarString(t, st, `SELECT state FROM batch_items WHERE id=$1`, f.itemID) != "failed" {
		t.Fatal("partial failure was reported as success")
	}
	task, err = st.RetryTask(ctx, f.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Attempts != 2 || task.Revision == nil || *task.Revision <= rev {
		t.Fatalf("second retry: %+v", task)
	}
	if err := st.RecordApplyReport(ctx, f.agent.ID, contracts.ApplyReport{Revision: rev, State: "applied"}); err != nil {
		t.Fatal(err)
	}
	if retryScalarString(t, st, `SELECT state FROM tasks WHERE id=$1`, f.taskID) != "pending" {
		t.Fatal("first retry receipt completed second retry")
	}
	if err := st.RecordApplyReport(ctx, f.agent.ID, contracts.ApplyReport{Revision: *task.Revision, State: "applied"}); err != nil {
		t.Fatal(err)
	}
	if retryScalarString(t, st, `SELECT state FROM tasks WHERE id=$1`, f.taskID) != "succeeded" {
		t.Fatal("matching publication did not complete")
	}
	if retryScalarString(t, st, `SELECT state FROM tasks WHERE id='unrelated'`) != "pending" {
		t.Fatal("unrelated task kind completed")
	}
}

func TestBatchDurabilityOneTaskPerAgent(t *testing.T) {
	f := newRetryFixture(t)
	st, ctx := f.st, context.Background()
	subID := *f.member.SubscriptionID
	sub, err := st.GetSubscription(ctx, subID)
	if err != nil {
		t.Fatal(err)
	}
	second := regMember(t, st, "durability-second", sub, period.Containing(1, time.UTC, st.now()))
	values := domain.NewObject()
	_ = values.Set("active", true)
	p, err := st.CreatePreview(ctx, PreviewRequest{Operation: OpSetActive, MemberIDs: []string{f.member.ID, second.ID}, Values: values})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.SubmitBatch(ctx, BatchSubmitRequest{PreviewID: p.ID, Confirm: true, IdempotencyKey: "multi-member"})
	if err != nil {
		t.Fatal(err)
	}
	if got := retryScalarInt(t, st, `SELECT count(*) FROM tasks WHERE batch_id=$1`, b.Batch.ID); got != 1 {
		t.Fatalf("tasks=%d, want one per agent", got)
	}
	if got := retryScalarInt(t, st, `SELECT count(*) FROM batch_items i JOIN tasks t ON t.id=i.task_id AND t.batch_id=i.batch_id AND t.agent_id=i.agent_id AND t.revision=i.revision WHERE i.batch_id=$1`, b.Batch.ID); got != 2 {
		t.Fatalf("only %d of two items mapped to the published task", got)
	}
}

func TestBatchDurabilitySubmissionRollback(t *testing.T) {
	f := newRetryFixture(t)
	st, ctx := f.st, context.Background()
	values := domain.NewObject()
	_ = values.Set("active", true)
	p, err := st.CreatePreview(ctx, PreviewRequest{Operation: OpSetActive, MemberIDs: []string{f.member.ID}, Values: values})
	if err != nil {
		t.Fatal(err)
	}
	req := BatchSubmitRequest{PreviewID: p.ID, Confirm: true, IdempotencyKey: "response-commit"}
	version := retryScalarInt(t, st, `SELECT version FROM members WHERE id=$1`, f.member.ID)
	rev := retryScalarInt(t, st, `SELECT revision_counter FROM control_state WHERE id=1`)
	batches := retryScalarInt(t, st, `SELECT count(*) FROM batches`)
	upgradeExec(t, st, `CREATE FUNCTION reject_durable_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected durability failure'; END $$;
 CREATE TRIGGER reject_durable_receipt BEFORE INSERT ON batch_idempotency FOR EACH ROW EXECUTE FUNCTION reject_durable_receipt()`)
	if _, err := st.SubmitBatch(ctx, req); err == nil {
		t.Fatal("injected receipt failure committed")
	}
	if retryScalarInt(t, st, `SELECT version FROM members WHERE id=$1`, f.member.ID) != version || retryScalarInt(t, st, `SELECT revision_counter FROM control_state WHERE id=1`) != rev || retryScalarInt(t, st, `SELECT count(*) FROM batches`) != batches {
		t.Fatal("failed submission left partial business/publication state")
	}
	if retryScalarInt(t, st, `SELECT count(*) FROM previews WHERE id=$1 AND consumed_at IS NULL`, p.ID) != 1 {
		t.Fatal("failed submission consumed preview")
	}
	if retryScalarInt(t, st, `SELECT count(*) FROM batch_idempotency WHERE idempotency_key=$1`, req.IdempotencyKey) != 0 {
		t.Fatal("failed submission reserved key")
	}
	upgradeExec(t, st, `DROP TRIGGER reject_durable_receipt ON batch_idempotency`)
	first, err := st.SubmitBatch(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := st.SubmitBatch(ctx, req)
	if err != nil || !replay.Replay || first.Batch.ID != replay.Batch.ID {
		t.Fatalf("retry after rollback/lost response: %+v %v", replay, err)
	}
}

func durabilityLegacyStore(t *testing.T) *Store {
	t.Helper()
	st := upgradeTestStore(t, false)
	dir := t.TempDir()
	for _, name := range []string{"0001_init.sql", "0002_batch_preview.sql", "0003_maintenance.sql", "0004_node_external_port.sql", "0005_period_and_owner_history.sql"} {
		raw, err := os.ReadFile(filepath.Join(regMigrationsDir(t), name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Migrate(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	st.migrationsDir = regMigrationsDir(t)
	return st
}

func durabilityLegacyFacts(t *testing.T, st *Store) {
	t.Helper()
	upgradeExec(t, st, `INSERT INTO batches(id,preview_id,operation,state,idempotency_key,request_fingerprint,completed_at)
 VALUES ('known','old-preview','set_active','completed','old-key',$1,now()),
 ('ambiguous','ambiguous-preview','set_active','completed','ambiguous-key','old-fingerprint',now()),
 ('mixed','mixed-preview','set_active','completed','mixed-key','mixed-fingerprint',now())`, requestFingerprint("old-preview", true))
	upgradeExec(t, st, `INSERT INTO tasks(id,kind,agent_id,batch_id,state) VALUES
 ('known-task','authorization.sync','legacy-agent','known','failed'),
 ('ambiguous-1','authorization.sync','legacy-agent','ambiguous','failed'),
 ('ambiguous-2','authorization.sync','legacy-agent','ambiguous','failed'),
 ('mixed-task','authorization.sync','legacy-agent','mixed','failed')`)
	upgradeExec(t, st, `INSERT INTO batch_items(id,batch_id,agent_id,task_id,revision,state) VALUES
 ('known-1','known','legacy-agent','',7,'failed'),('known-2','known','legacy-agent','known-task',7,'failed'),
 ('ambiguous-item','ambiguous','legacy-agent','',8,'failed'),
 ('mixed-1','mixed','legacy-agent','mixed-task',8,'failed'),('mixed-2','mixed','legacy-agent','mixed-task',9,'failed')`)
}

func TestBatchDurabilityLegacyBackfill(t *testing.T) {
	st := durabilityLegacyStore(t)
	durabilityLegacyFacts(t, st)
	ctx := context.Background()
	if r := upgradeReady(t, st); r.Status != "ready" {
		t.Fatalf("known legacy prefix should pass existing proof: %+v", r)
	}
	controlBefore := retryScalarString(t, st, `SELECT to_jsonb(c)::text FROM control_state c WHERE id=1`)
	if err := st.Migrate(ctx, regMigrationsDir(t)); err != nil {
		t.Fatal(err)
	}
	if retryScalarInt(t, st, `SELECT revision FROM tasks WHERE id='known-task'`) != 7 {
		t.Fatal("unanimous publication not backfilled")
	}
	if retryScalarString(t, st, `SELECT task_id FROM batch_items WHERE id='known-1'`) != "known-task" {
		t.Fatal("unique mapping was not repaired")
	}
	if retryScalarString(t, st, `SELECT task_id FROM batch_items WHERE id='ambiguous-item'`) != "" {
		t.Fatal("ambiguous mapping guessed")
	}
	if retryScalarInt(t, st, `SELECT count(*) FROM tasks WHERE batch_id IN ('ambiguous','mixed') AND revision IS NULL`) != 3 {
		t.Fatal("unknown or mixed publication guessed")
	}
	if retryScalarInt(t, st, `SELECT count(*) FROM batch_durability_diagnostics WHERE subject_type='task'`) != 3 {
		t.Fatal("unknown tasks lack diagnostics")
	}
	_, err := st.RetryTask(ctx, "mixed-task")
	durabilityCode(t, err, domain.CodeConflict)
	if retryScalarString(t, st, `SELECT to_jsonb(c)::text FROM control_state c WHERE id=1`) != controlBefore {
		t.Fatal("migration/blocked retry republished authorization")
	}
	if retryScalarInt(t, st, `SELECT count(*) FROM usage_entries`) != 0 || retryScalarInt(t, st, `SELECT count(*) FROM credentials`) != 0 {
		t.Fatal("migration manufactured ledger/authorization")
	}
	req := BatchSubmitRequest{PreviewID: "old-preview", Confirm: true, IdempotencyKey: "old-key"}
	first, err := st.SubmitBatch(ctx, req)
	if err != nil || !first.Replay {
		t.Fatalf("legacy replay: %+v %v", first, err)
	}
	upgradeExec(t, st, `DELETE FROM batch_items WHERE batch_id='known'; DELETE FROM batches WHERE id='known'`)
	replay, err := st.SubmitBatch(ctx, req)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("legacy replay after history removal: %+v %v", replay, err)
	}
	req.PreviewID = "other-preview"
	_, err = st.SubmitBatch(ctx, req)
	durabilityCode(t, err, domain.CodeIdempotencyKeyReused)
	if retryScalarInt(t, st, `SELECT count(*) FROM schema_migrations`) != 6 {
		t.Fatal("migration prefix registration mismatch")
	}
}

func TestBatchDurabilityMigrationRollback(t *testing.T) {
	st := durabilityLegacyStore(t)
	durabilityLegacyFacts(t, st)
	upgradeExec(t, st, `CREATE FUNCTION reject_six() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.version='0006_batch_task_durability.sql' THEN RAISE EXCEPTION 'injected migration registration failure'; END IF;
 RETURN NEW; END $$;
 CREATE TRIGGER reject_six BEFORE INSERT ON schema_migrations FOR EACH ROW EXECUTE FUNCTION reject_six()`)
	if err := st.Migrate(context.Background(), regMigrationsDir(t)); err == nil {
		t.Fatal("migration should fail registration")
	}
	if retryScalarInt(t, st, `SELECT count(*) FROM schema_migrations`) != 5 {
		t.Fatal("prior successful migration prefix was lost or failed file registered")
	}
	if retryScalarInt(t, st, `SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('batch_idempotency','batch_durability_diagnostics')`) != 0 {
		t.Fatal("failed file left DDL")
	}
	if retryScalarInt(t, st, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='tasks' AND column_name='revision'`) != 0 {
		t.Fatal("failed file left task column")
	}
	if retryScalarString(t, st, `SELECT task_id FROM batch_items WHERE id='known-1'`) != "" {
		t.Fatal("failed migration left a partial repair")
	}
	upgradeExec(t, st, `DROP TRIGGER reject_six ON schema_migrations`)
	if err := st.Migrate(context.Background(), regMigrationsDir(t)); err != nil {
		t.Fatal(err)
	}
	if retryScalarInt(t, st, `SELECT revision FROM tasks WHERE id='known-task'`) != 7 {
		t.Fatal("migration cannot resume after rollback")
	}
}

func TestBatchDurabilityConcurrentAndRecovery(t *testing.T) {
	f := newRetryFixture(t)
	st, ctx := f.st, context.Background()
	values := domain.NewObject()
	_ = values.Set("active", true)
	p, err := st.CreatePreview(ctx, PreviewRequest{Operation: OpSetActive, MemberIDs: []string{f.member.ID}, Values: values})
	if err != nil {
		t.Fatal(err)
	}
	req := BatchSubmitRequest{PreviewID: p.ID, Confirm: true, IdempotencyKey: "concurrent-submit"}
	type outcome struct {
		result *BatchSubmitResult
		err    error
	}
	results := make(chan outcome, 8)
	for i := 0; i < cap(results); i++ {
		go func() { result, err := st.SubmitBatch(ctx, req); results <- outcome{result, err} }()
	}
	fresh, batchID := 0, ""
	for i := 0; i < cap(results); i++ {
		o := <-results
		if o.err != nil {
			t.Fatal(o.err)
		}
		if !o.result.Replay {
			fresh++
		}
		if batchID == "" {
			batchID = o.result.Batch.ID
		}
		if o.result.Batch.ID != batchID {
			t.Fatal("concurrent duplicate created another batch")
		}
	}
	if fresh != 1 || retryScalarInt(t, st, `SELECT count(*) FROM batch_idempotency WHERE idempotency_key=$1`, req.IdempotencyKey) != 1 {
		t.Fatalf("concurrent submit applied %d times, want exactly once", fresh)
	}
	if _, err := st.EnterRecoveryMode(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = st.SubmitBatch(ctx, req)
	durabilityCode(t, err, domain.CodeRecoveryMode)
	_, err = st.CreatePreview(ctx, PreviewRequest{Operation: OpSetActive, MemberIDs: []string{f.member.ID}, Values: values})
	durabilityCode(t, err, domain.CodeRecoveryMode)
	_, err = st.RetryTask(ctx, f.taskID)
	durabilityCode(t, err, domain.CodeRecoveryMode)
	_, _, err = st.SweepTimeouts(ctx, st.now())
	durabilityCode(t, err, domain.CodeRecoveryMode)
	called := false
	err = st.InMaintenanceTx(ctx, DefaultMaintenanceBounds(), func(context.Context, *MaintenanceTx) error { called = true; return nil })
	durabilityCode(t, err, domain.CodeRecoveryMode)
	if called {
		t.Fatal("maintenance acquired object/advisory work before recovery gate")
	}
}

func TestBatchDurabilityLegacyOrphanGate(t *testing.T) {
	st := durabilityLegacyStore(t)
	durabilityLegacyFacts(t, st)
	// Even a seemingly repairable orphan must not bypass the existing upgrade
	// proof. This task ID is absent; administrators must reconcile old facts.
	upgradeExec(t, st, `UPDATE batch_items SET task_id='missing-task' WHERE id='known-1'`)
	if r := upgradeReady(t, st); r.Status != "blocked" || !upgradeHas(r, "ITEM_TASK_ORPHAN") {
		t.Fatalf("orphan proof unexpectedly accepted: %+v", r)
	}
	if err := st.Migrate(context.Background(), regMigrationsDir(t)); err == nil {
		t.Fatal("migration bypassed legacy orphan gate")
	}
	if retryScalarInt(t, st, `SELECT count(*) FROM schema_migrations`) != 5 {
		t.Fatal("blocked migration changed applied prefix")
	}
	if retryScalarString(t, st, `SELECT task_id FROM batch_items WHERE id='known-1'`) != "missing-task" {
		t.Fatal("preflight silently repaired facts")
	}
}
