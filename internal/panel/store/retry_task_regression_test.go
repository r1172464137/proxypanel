package store

// Regression coverage for the production RetryTask defect: the retry only
// re-queued the task row, never re-opened its batch items and never published a
// revision, so a retried dispatch could never be receipted by an agent and the
// batch stayed "timeout" forever.
//
// Every guarantee below is about transaction behaviour, so these tests need a
// real PostgreSQL instance. They reuse the store harness (newRegStore) and are
// skipped unless PANEL_TEST_DATABASE_URL points at the documented isolated
// instance; each run creates and drops its own schema.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/period"
)

// retryFixture is one registered agent with one node, one authorized member and
// one *already submitted* revoke batch whose dispatch was swept to timeout
// through the production sweep. The business change therefore happened exactly
// once, before any retry.
type retryFixture struct {
	st       *Store
	server   *domain.Server
	node     *domain.Node
	agent    *domain.Agent
	member   *domain.Member
	batchID  string
	taskID   string
	itemID   string
	revision int64
}

func retryScalarInt(t *testing.T, st *Store, query string, args ...any) int64 {
	t.Helper()
	var v int64
	if err := st.DB.QueryRowContext(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return v
}

func retryScalarString(t *testing.T, st *Store, query string, args ...any) string {
	t.Helper()
	var v string
	if err := st.DB.QueryRowContext(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return v
}

func newRetryFixture(t *testing.T) *retryFixture {
	t.Helper()
	st := newRegStore(t)
	ctx := context.Background()

	f := &retryFixture{st: st}
	f.server = regServer(t, st, "retry-server")
	f.agent = regAgent(t, st, f.server.ID)
	f.node = regNode(t, st, f.server.ID, "retry-node", 12443)
	group, err := st.CreateNodeGroup(ctx, &domain.NodeGroup{
		ID: ids.New(ids.NodeGroup), Name: "retry-group", NodeIDs: []string{f.node.ID},
	})
	if err != nil {
		t.Fatalf("create node group: %v", err)
	}
	sub := regSubscription(t, st, "retry-sub", nil, []string{group.ID})
	f.member = regMember(t, st, "retry-user", sub, period.Containing(1, time.UTC, st.now()))
	regCredential(t, st, f.member.ID, f.node.ID)

	// One real business change through the production batch path.
	values := domain.NewObject()
	if err := values.Set("active", false); err != nil {
		t.Fatalf("values: %v", err)
	}
	preview, err := st.CreatePreview(ctx, PreviewRequest{
		Operation: OpSetActive, MemberIDs: []string{f.member.ID}, Values: values,
		Reason: "retry regression", CreatedBy: "regression",
	})
	if err != nil {
		t.Fatalf("create preview: %v", err)
	}
	submitted, err := st.SubmitBatch(ctx, BatchSubmitRequest{
		PreviewID: preview.ID, IdempotencyKey: "retry-" + regRandomHex(t, 6),
		Confirm: true, CreatedBy: "regression",
	})
	if err != nil {
		t.Fatalf("submit batch: %v", err)
	}
	f.batchID = submitted.Batch.ID

	f.taskID = retryScalarString(t, st, `SELECT id FROM tasks WHERE batch_id=$1 ORDER BY id LIMIT 1`, f.batchID)
	f.itemID = retryScalarString(t, st, `SELECT id FROM batch_items WHERE task_id=$1`, f.taskID)
	f.revision = retryScalarInt(t, st, `SELECT revision FROM batch_items WHERE id=$1`, f.itemID)
	if f.revision <= 0 {
		t.Fatalf("the dispatched item carries revision %d, expected a published revision", f.revision)
	}

	// Sweep the dispatch to timeout through the production sweeper: the agent
	// heartbeat and the task availability must both be stale for that.
	if _, err := st.DB.ExecContext(ctx, `UPDATE agents SET last_heartbeat_at = now() - interval '10 minutes'
		WHERE id=$1`, f.agent.ID); err != nil {
		t.Fatalf("backdate agent heartbeat: %v", err)
	}
	if _, err := st.DB.ExecContext(ctx, `UPDATE tasks SET available_at = now() - interval '10 minutes'
		WHERE batch_id=$1`, f.batchID); err != nil {
		t.Fatalf("backdate task: %v", err)
	}
	timedOut, _, err := st.SweepTimeouts(ctx, st.now())
	if err != nil {
		t.Fatalf("sweep timeouts: %v", err)
	}
	if timedOut < 1 {
		t.Fatalf("the injected stale dispatch was not swept (timed out %d)", timedOut)
	}
	if state := retryScalarString(t, st, `SELECT state FROM tasks WHERE id=$1`, f.taskID); state != "timeout" {
		t.Fatalf("task state after the sweep is %q, expected timeout", state)
	}
	if state := retryScalarString(t, st, `SELECT state FROM batch_items WHERE id=$1`, f.itemID); state != "timeout" {
		t.Fatalf("batch item state after the sweep is %q, expected timeout", state)
	}
	return f
}

// TestRetryTaskReopensDispatchAndPublishesCurrentRevision is the core regression:
// one retry re-opens the same task and the same batch item, stamps them with a
// newly published revision that the agent can really apply and receipt, and does
// not apply the business change a second time.
func TestRetryTaskReopensDispatchAndPublishesCurrentRevision(t *testing.T) {
	f := newRetryFixture(t)
	st := f.st
	ctx := context.Background()

	controlBefore, err := st.GetControlState(ctx)
	if err != nil {
		t.Fatalf("control state: %v", err)
	}
	agentBefore := mustAgent(t, st, f.agent.ID)
	memberVersionBefore := retryScalarInt(t, st, `SELECT version FROM members WHERE id=$1`, f.member.ID)
	memberActiveBefore := retryScalarInt(t, st, `SELECT CASE WHEN active THEN 1 ELSE 0 END FROM members WHERE id=$1`, f.member.ID)
	activeCredentialsBefore := retryScalarInt(t, st, `SELECT count(*) FROM credentials WHERE member_id=$1 AND state='active'`, f.member.ID)
	batchesBefore := retryScalarInt(t, st, `SELECT count(*) FROM batches`)
	itemsBefore := retryScalarInt(t, st, `SELECT count(*) FROM batch_items WHERE batch_id=$1`, f.batchID)
	tasksBefore := retryScalarInt(t, st, `SELECT count(*) FROM tasks WHERE batch_id=$1`, f.batchID)
	usageBefore := retryScalarInt(t, st, `SELECT count(*) FROM usage_entries`)
	attemptsBefore := retryScalarInt(t, st, `SELECT attempts FROM tasks WHERE id=$1`, f.taskID)

	retried, err := st.RetryTask(ctx, f.taskID)
	if err != nil {
		t.Fatalf("retry the timed out task: %v", err)
	}
	if retried.ID != f.taskID {
		t.Fatalf("the retry returned task %s, expected the same task %s", retried.ID, f.taskID)
	}
	if retried.State != "pending" {
		t.Fatalf("the retried task is %q, expected pending", retried.State)
	}
	if int64(retried.Attempts) != attemptsBefore+1 {
		t.Fatalf("attempts went %d -> %d, expected exactly one more", attemptsBefore, retried.Attempts)
	}
	if retried.LastError != "" {
		t.Fatalf("the retried task kept the previous error %q", retried.LastError)
	}

	// A new revision is published to exactly the owning agent, in the same commit.
	controlAfter, err := st.GetControlState(ctx)
	if err != nil {
		t.Fatalf("control state: %v", err)
	}
	agentAfter := mustAgent(t, st, f.agent.ID)
	if controlAfter.RevisionCounter != controlBefore.RevisionCounter+1 {
		t.Fatalf("the retry advanced the control revision %d -> %d, expected exactly one step",
			controlBefore.RevisionCounter, controlAfter.RevisionCounter)
	}
	if agentAfter.DesiredRevision != controlAfter.RevisionCounter {
		t.Fatalf("the agent desired revision is %d, the published revision is %d",
			agentAfter.DesiredRevision, controlAfter.RevisionCounter)
	}
	if agentAfter.AppliedRevision != agentBefore.AppliedRevision {
		t.Fatalf("the retry moved applied_revision on its own: %d -> %d (only a real agent receipt may do that)",
			agentBefore.AppliedRevision, agentAfter.AppliedRevision)
	}

	// The original batch item is re-opened on the *new* revision, so the sweep
	// cannot supersede it before the agent answers.
	itemRevision := retryScalarInt(t, st, `SELECT revision FROM batch_items WHERE id=$1`, f.itemID)
	if itemRevision != controlAfter.RevisionCounter {
		t.Fatalf("the reopened item carries revision %d, expected the new revision %d",
			itemRevision, controlAfter.RevisionCounter)
	}
	if itemRevision <= f.revision {
		t.Fatalf("the reopened item kept the old revision %d", itemRevision)
	}
	if state := retryScalarString(t, st, `SELECT state FROM batch_items WHERE id=$1`, f.itemID); state != "pending" {
		t.Fatalf("the batch item is %q after the retry, expected pending", state)
	}
	if itemErr := retryScalarString(t, st, `SELECT error FROM batch_items WHERE id=$1`, f.itemID); itemErr != "" {
		t.Fatalf("the reopened item kept the error %q", itemErr)
	}
	if state := retryScalarString(t, st, `SELECT state FROM batches WHERE id=$1`, f.batchID); state != "applied" {
		t.Fatalf("the batch is %q after the retry, expected applied", state)
	}
	if completed := retryScalarInt(t, st, `SELECT count(*) FROM batches WHERE id=$1 AND completed_at IS NOT NULL`, f.batchID); completed != 0 {
		t.Fatal("the reopened batch kept completed_at set")
	}

	// The business change is not applied a second time.
	if got := retryScalarInt(t, st, `SELECT version FROM members WHERE id=$1`, f.member.ID); got != memberVersionBefore {
		t.Fatalf("the retry re-applied the business change: member version %d -> %d", memberVersionBefore, got)
	}
	if got := retryScalarInt(t, st, `SELECT CASE WHEN active THEN 1 ELSE 0 END FROM members WHERE id=$1`, f.member.ID); got != memberActiveBefore {
		t.Fatalf("the retry changed the member activation state: %d -> %d", memberActiveBefore, got)
	}
	if got := retryScalarInt(t, st, `SELECT count(*) FROM credentials WHERE member_id=$1 AND state='active'`, f.member.ID); got != activeCredentialsBefore {
		t.Fatalf("the retry changed the active credential count: %d -> %d", activeCredentialsBefore, got)
	}
	if got := retryScalarInt(t, st, `SELECT count(*) FROM batches`); got != batchesBefore {
		t.Fatalf("the retry created a batch: %d -> %d", batchesBefore, got)
	}
	if got := retryScalarInt(t, st, `SELECT count(*) FROM batch_items WHERE batch_id=$1`, f.batchID); got != itemsBefore {
		t.Fatalf("the retry created a batch item: %d -> %d", itemsBefore, got)
	}
	if got := retryScalarInt(t, st, `SELECT count(*) FROM tasks WHERE batch_id=$1`, f.batchID); got != tasksBefore {
		t.Fatalf("the retry created a task: %d -> %d", tasksBefore, got)
	}
	if got := retryScalarInt(t, st, `SELECT count(*) FROM usage_entries`); got != usageBefore {
		t.Fatalf("the retry touched the ledger: %d -> %d rows", usageBefore, got)
	}
	if items := retryScalarString(t, st, `SELECT string_agg(id, ',') FROM batch_items WHERE task_id=$1`, f.taskID); items != f.itemID {
		t.Fatalf("the retried task owns items %q, expected only the original %q", items, f.itemID)
	}

	// The sweeper must neither supersede nor re-timeout the reopened dispatch.
	if _, superseded, err := st.SweepTimeouts(ctx, st.now()); err != nil {
		t.Fatalf("sweep after the retry: %v", err)
	} else if superseded != 0 {
		t.Fatalf("the sweep superseded %d reopened item(s)", superseded)
	}
	if _, _, err := st.SweepTimeouts(ctx, st.now()); err != nil {
		t.Fatalf("second sweep after the retry: %v", err)
	}
	if state := retryScalarString(t, st, `SELECT state FROM tasks WHERE id=$1`, f.taskID); state != "pending" {
		t.Fatalf("the sweep moved the retried task to %q", state)
	}

	// The new revision carries the *current* authorization: the member was
	// revoked by the original batch, so the snapshot must not contain it.
	snapshot, err := st.BuildSnapshot(ctx, mustAgent(t, st, f.agent.ID))
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if snapshot.Revision != controlAfter.RevisionCounter {
		t.Fatalf("the snapshot revision is %d, expected the retried revision %d",
			snapshot.Revision, controlAfter.RevisionCounter)
	}
	for _, instance := range snapshot.Instances {
		for _, user := range instance.Users {
			if user.MemberID == f.member.ID {
				t.Fatalf("the retried revision re-authorized the revoked member %s on node %s",
					f.member.ID, instance.NodeID)
			}
		}
	}
	if snapshot.ExpectedUserCount != 0 {
		t.Fatalf("the retried revision authorizes %d user(s), expected none", snapshot.ExpectedUserCount)
	}

	// A real agent receipt closes the loop: the task and the original item reach
	// the same terminal success.
	if err := st.RecordApplyReport(ctx, f.agent.ID, contracts.ApplyReport{
		ControlGeneration: snapshot.ControlGeneration, Revision: snapshot.Revision,
		State: "applied", AppliedAt: st.now(),
	}); err != nil {
		t.Fatalf("record the agent receipt: %v", err)
	}
	if applied := mustAgent(t, st, f.agent.ID).AppliedRevision; applied != snapshot.Revision {
		t.Fatalf("agent applied revision is %d after the receipt, expected %d", applied, snapshot.Revision)
	}
	if state := retryScalarString(t, st, `SELECT state FROM tasks WHERE id=$1`, f.taskID); state != "succeeded" {
		t.Fatalf("task state is %q after the receipt, expected succeeded", state)
	}
	if state := retryScalarString(t, st, `SELECT state FROM batch_items WHERE id=$1`, f.itemID); state != "succeeded" {
		t.Fatalf("batch item state is %q after the receipt, expected succeeded", state)
	}
	// The batch closes at the next sweep, exactly like a fresh dispatch: the
	// receipt only advances the task and the items.
	if _, _, err := st.SweepTimeouts(ctx, st.now()); err != nil {
		t.Fatalf("sweep after the receipt: %v", err)
	}
	if state := retryScalarString(t, st, `SELECT state FROM batches WHERE id=$1`, f.batchID); state != "completed" {
		t.Fatalf("the batch is %q after its last item was receipted, expected completed", state)
	}
}

// TestRetryTaskRefusals pins the repeat-call and terminal semantics: a succeeded
// task is never re-opened, a queued retry cannot publish a second revision and a
// task without attempts left is refused.
func TestRetryTaskRefusals(t *testing.T) {
	f := newRetryFixture(t)
	st := f.st
	ctx := context.Background()

	conflictCode := func(t *testing.T, err error, want string) {
		t.Helper()
		if err == nil {
			t.Fatal("expected an error")
		}
		var de *domain.Error
		if !errors.As(err, &de) {
			t.Fatalf("error %v is not a domain error", err)
		}
		if de.Code != want {
			t.Fatalf("error code is %q (%v), expected %q", de.Code, err, want)
		}
	}

	// Unknown task id.
	if _, err := st.RetryTask(ctx, "tsk_missing"); err != nil {
		conflictCode(t, err, domain.CodeNotFound)
	}

	// A queued retry must not publish a second revision.
	if _, err := st.RetryTask(ctx, f.taskID); err != nil {
		t.Fatalf("first retry: %v", err)
	}
	afterFirst, err := st.GetControlState(ctx)
	if err != nil {
		t.Fatalf("control state: %v", err)
	}
	if _, err := st.RetryTask(ctx, f.taskID); err == nil {
		t.Fatal("a second retry of a queued task must be refused")
	} else {
		conflictCode(t, err, domain.CodeConflict)
	}
	afterSecond, _ := st.GetControlState(ctx)
	if afterSecond.RevisionCounter != afterFirst.RevisionCounter {
		t.Fatalf("the refused retry still advanced the revision %d -> %d",
			afterFirst.RevisionCounter, afterSecond.RevisionCounter)
	}
	if state := retryScalarString(t, st, `SELECT state FROM tasks WHERE id=$1`, f.taskID); state != "pending" {
		t.Fatalf("task state is %q after the refused retry, expected pending", state)
	}

	// Drive the retried dispatch to success, then prove success is terminal.
	snapshot, err := st.BuildSnapshot(ctx, mustAgent(t, st, f.agent.ID))
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if err := st.RecordApplyReport(ctx, f.agent.ID, contracts.ApplyReport{
		ControlGeneration: snapshot.ControlGeneration, Revision: snapshot.Revision,
		State: "applied", AppliedAt: st.now(),
	}); err != nil {
		t.Fatalf("agent receipt: %v", err)
	}
	if state := retryScalarString(t, st, `SELECT state FROM tasks WHERE id=$1`, f.taskID); state != "succeeded" {
		t.Fatalf("task state is %q after the receipt, expected succeeded", state)
	}
	terminalBefore, _ := st.GetControlState(ctx)
	if _, err := st.RetryTask(ctx, f.taskID); err == nil {
		t.Fatal("a succeeded task must never be re-opened")
	} else {
		conflictCode(t, err, domain.CodeConflict)
	}
	terminalAfter, _ := st.GetControlState(ctx)
	if terminalAfter.RevisionCounter != terminalBefore.RevisionCounter {
		t.Fatal("refusing a terminal task must not publish a revision")
	}
	if state := retryScalarString(t, st, `SELECT state FROM tasks WHERE id=$1`, f.taskID); state != "succeeded" {
		t.Fatalf("the terminal task changed to %q", state)
	}
	if state := retryScalarString(t, st, `SELECT state FROM batch_items WHERE id=$1`, f.itemID); state != "succeeded" {
		t.Fatalf("the succeeded item changed to %q", state)
	}

	// Attempt protection: a timeout task that spent its attempts is refused.
	exhausted := newRetryFixture(t)
	if _, err := exhausted.st.DB.ExecContext(ctx, `UPDATE tasks SET state='timeout', attempts=max_attempts
		WHERE id=$1`, exhausted.taskID); err != nil {
		t.Fatalf("exhaust attempts: %v", err)
	}
	exhaustedControl, _ := exhausted.st.GetControlState(ctx)
	if _, err := exhausted.st.RetryTask(ctx, exhausted.taskID); err == nil {
		t.Fatal("a task that spent its attempts must be refused")
	} else {
		conflictCode(t, err, domain.CodeConflict)
	}
	afterExhausted, _ := exhausted.st.GetControlState(ctx)
	if afterExhausted.RevisionCounter != exhaustedControl.RevisionCounter {
		t.Fatal("refusing an exhausted task must not publish a revision")
	}
	if state := retryScalarString(t, exhausted.st, `SELECT state FROM tasks WHERE id=$1`, exhausted.taskID); state != "timeout" {
		t.Fatalf("the exhausted task changed to %q", state)
	}
}

// TestRetryTaskIsAtomicOnFailure injects a database failure into the last step of
// the retry (the batch item reopen) and proves nothing was partially committed:
// the task, the item, the published revision and the agent pointer all stay as
// they were, and a later retry still works.
func TestRetryTaskIsAtomicOnFailure(t *testing.T) {
	f := newRetryFixture(t)
	st := f.st
	ctx := context.Background()

	controlBefore, err := st.GetControlState(ctx)
	if err != nil {
		t.Fatalf("control state: %v", err)
	}
	desiredBefore := mustAgent(t, st, f.agent.ID).DesiredRevision
	attemptsBefore := retryScalarInt(t, st, `SELECT attempts FROM tasks WHERE id=$1`, f.taskID)
	taskErrorBefore := retryScalarString(t, st, `SELECT last_error FROM tasks WHERE id=$1`, f.taskID)

	if _, err := st.DB.ExecContext(ctx, `CREATE FUNCTION retry_injected_failure() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected batch_items failure'; END $$`); err != nil {
		t.Fatalf("create the failure trigger function: %v", err)
	}
	if _, err := st.DB.ExecContext(ctx, `CREATE TRIGGER retry_injected_failure BEFORE UPDATE ON batch_items
		FOR EACH ROW EXECUTE FUNCTION retry_injected_failure()`); err != nil {
		t.Fatalf("create the failure trigger: %v", err)
	}
	if _, err := st.RetryTask(ctx, f.taskID); err == nil {
		t.Fatal("the retry must fail when a later step of its transaction fails")
	}

	controlAfter, err := st.GetControlState(ctx)
	if err != nil {
		t.Fatalf("control state: %v", err)
	}
	if controlAfter.RevisionCounter != controlBefore.RevisionCounter {
		t.Fatalf("a failed retry advanced the revision %d -> %d",
			controlBefore.RevisionCounter, controlAfter.RevisionCounter)
	}
	if got := mustAgent(t, st, f.agent.ID).DesiredRevision; got != desiredBefore {
		t.Fatalf("a failed retry moved the agent desired revision %d -> %d", desiredBefore, got)
	}
	if state := retryScalarString(t, st, `SELECT state FROM tasks WHERE id=$1`, f.taskID); state != "timeout" {
		t.Fatalf("a failed retry left the task in %q, expected timeout", state)
	}
	if got := retryScalarInt(t, st, `SELECT attempts FROM tasks WHERE id=$1`, f.taskID); got != attemptsBefore {
		t.Fatalf("a failed retry spent an attempt: %d -> %d", attemptsBefore, got)
	}
	if got := retryScalarString(t, st, `SELECT last_error FROM tasks WHERE id=$1`, f.taskID); got != taskErrorBefore {
		t.Fatalf("a failed retry changed the task error %q -> %q", taskErrorBefore, got)
	}
	if state := retryScalarString(t, st, `SELECT state FROM batch_items WHERE id=$1`, f.itemID); state != "timeout" {
		t.Fatalf("a failed retry left the item in %q, expected timeout", state)
	}
	if got := retryScalarInt(t, st, `SELECT revision FROM batch_items WHERE id=$1`, f.itemID); got != f.revision {
		t.Fatalf("a failed retry re-stamped the item revision %d -> %d", f.revision, got)
	}
	if state := retryScalarString(t, st, `SELECT state FROM batches WHERE id=$1`, f.batchID); state != "completed" {
		t.Fatalf("a failed retry changed the batch state to %q", state)
	}

	if _, err := st.DB.ExecContext(ctx, `DROP TRIGGER retry_injected_failure ON batch_items`); err != nil {
		t.Fatalf("drop the failure trigger: %v", err)
	}
	if _, err := st.DB.ExecContext(ctx, `DROP FUNCTION retry_injected_failure()`); err != nil {
		t.Fatalf("drop the failure trigger function: %v", err)
	}

	retried, err := st.RetryTask(ctx, f.taskID)
	if err != nil {
		t.Fatalf("retry after removing the injected failure: %v", err)
	}
	if retried.State != "pending" || int64(retried.Attempts) != attemptsBefore+1 {
		t.Fatalf("the retry after the injection left state=%q attempts=%d", retried.State, retried.Attempts)
	}
	if state := retryScalarString(t, st, `SELECT state FROM batch_items WHERE id=$1`, f.itemID); state != "pending" {
		t.Fatalf("the item is %q after the successful retry, expected pending", state)
	}
	if !strings.HasPrefix(retryScalarString(t, st, `SELECT state FROM batches WHERE id=$1`, f.batchID), "applied") {
		t.Fatalf("the batch state is %q after the successful retry",
			retryScalarString(t, st, `SELECT state FROM batches WHERE id=$1`, f.batchID))
	}
}
