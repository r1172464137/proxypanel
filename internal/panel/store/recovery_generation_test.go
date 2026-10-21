package store

import (
	"context"
	"testing"
)

func TestRecoveryGenerationIsFreshExactJSONInteger(t *testing.T) {
	previous := int64(1)
	for i := 0; i < 128; i++ {
		next, err := newControlGeneration(previous)
		if err != nil {
			t.Fatal(err)
		}
		if next <= 0 || next > (1<<53)-1 || next == previous || int64(float64(next)) != next {
			t.Fatalf("invalid generation %d after %d", next, previous)
		}
		previous = next
	}
}

func TestRecoveryGenerationTransaction(t *testing.T) {
	st := newRegStore(t)
	ctx := context.Background()
	server := regServer(t, st, "recovery")
	agent := regAgent(t, st, server.ID)
	before, err := st.GetControlState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.ExecContext(ctx, `CREATE FUNCTION reject_recovery_agent() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected recovery failure'; END $$;
	CREATE TRIGGER reject_recovery_agent BEFORE UPDATE ON agents FOR EACH ROW EXECUTE FUNCTION reject_recovery_agent()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnterRecoveryMode(ctx); err == nil {
		t.Fatal("expected injected transaction failure")
	}
	after, err := st.GetControlState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.ControlGeneration != before.ControlGeneration || after.RecoveryMode != before.RecoveryMode {
		t.Fatal("failed recovery partially committed")
	}
	if _, err := st.DB.ExecContext(ctx, `DROP TRIGGER reject_recovery_agent ON agents`); err != nil {
		t.Fatal(err)
	}
	after, err = st.EnterRecoveryMode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !after.RecoveryMode || after.ControlGeneration == before.ControlGeneration {
		t.Fatal("recovery did not create a fresh epoch")
	}
	var generation int64
	if err := st.DB.QueryRowContext(ctx, `SELECT control_generation FROM agents WHERE id=$1`, agent.ID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != after.ControlGeneration {
		t.Fatal("agent and control generations diverged")
	}
}
