package store

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	agentstate "proxypanel/internal/agent/state"
)

// All workload is synthetic and isolated. The OS observations below are real:
// two exact test-owned PIDs, no daemon/systemctl/lab process is ever touched.
func ownedStopProcess(t *testing.T) (ProcessStopReview, func() time.Time) {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "120")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := false
	t.Cleanup(func() {
		if !done {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	stat, err := os.ReadFile("/proc/" + itoaStop(cmd.Process.Pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(stat[strings.LastIndex(string(stat), ")")+1:]))
	exe, err := os.ReadFile("/proc/" + itoaStop(cmd.Process.Pid) + "/exe")
	if err != nil {
		t.Fatal(err)
	}
	p := ProcessStopReview{PID: cmd.Process.Pid, BootID: strings.TrimSpace(string(boot)), StartIdentity: fields[19], ExecutableSHA256: upgradeHash(exe), Observation: "observed-exit", RecordIndex: 0}
	return p, func() time.Time {
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		err := cmd.Wait()
		done = true
		if err == nil || cmd.ProcessState == nil || !cmd.ProcessState.Exited() && cmd.ProcessState.Sys() == nil {
			t.Fatal("no wait observation")
		}
		if _, err := os.Stat("/proc/" + itoaStop(cmd.Process.Pid)); !os.IsNotExist(err) {
			t.Fatal("owned PID still present", err)
		}
		return time.Now().UTC()
	}
}
func itoaStop(v int) string { return strconv.Itoa(v) }

// A real checkpointed Agent state export (legacy, no intent journal) and real
// exit observations exercise the administrator-review trust boundary. The
// accounting workload is intentionally empty, not a claim about a live lab.
func stopFixture(t *testing.T, st *Store) (*UpgradeEvidence, *UpgradeStopReview, string) {
	t.Helper()
	upgradeSeedAgent(t, st)
	upgradeSeedMember(t, st)
	// Newer prefixes require persisted current-period history too. Seed the
	// same legitimate current interval before capturing the stop fingerprint.
	var history bool
	if err := st.DB.QueryRow(`SELECT to_regclass('member_periods') IS NOT NULL`).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if history {
		upgradeExec(t, st, `INSERT INTO member_periods(member_id,period_start,period_end,timezone,anchor_day,source) SELECT id,period_start,period_end,period_timezone,period_anchor_day,'proven_current' FROM members WHERE id='m'`)
	}
	upgradeExec(t, st, `INSERT INTO nodes(id,server_id,name,protocol,host,port) VALUES ('n','s','node','vless','127.0.0.1',12345)`)
	upgradeExec(t, st, `INSERT INTO core_instances(id,server_id,agent_id,node_id,generation,config_revision,state) VALUES ('c','s','a','n',7,0,'running')`)
	fingerprint := upgradeReady(t, st).DatabaseSHA256
	agent, stopAgent := ownedStopProcess(t)
	core, stopCore := ownedStopProcess(t)
	start := time.Now().UTC().Add(-time.Minute)
	traffic := time.Now().UTC()
	final := time.Now().UTC()
	core.ObservedAt = stopCore()
	agent.ObservedAt = stopAgent()
	dir := t.TempDir()
	stateDir := t.TempDir()
	local, err := agentstate.Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err = local.SaveIdentity(context.Background(), agentstate.Identity{AgentID: "a", Token: "test-only-not-exported", ControlGeneration: "1", AppliedRevision: 0}); err != nil {
		t.Fatal(err)
	}
	if err = local.Close(); err != nil {
		t.Fatal(err)
	}
	x, err := agentstate.ReadUpgradeEvidence(context.Background(), stateDir)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, v any) string {
		raw := upgradeMarshal(t, v)
		upgradeWrite(t, filepath.Join(dir, name), raw)
		return upgradeHash(raw)
	}
	exportHash := write("agent.json", x)
	recordHash := write("observations.json", map[string]any{"scope": "isolated synthetic workload; exact owned PIDs only; parent never restarts children", "agent": agent, "core": core, "final_accounting": "no traffic in synthetic workload"})
	owner := OwnerAssertion{NodeID: "n", ServerID: "s", AgentID: "a", ValidFrom: start, ProvenanceFile: "observations.json", ProvenanceSHA256: recordHash}
	review := &UpgradeStopReview{FormatVersion: UpgradeStopReviewFormat, Administrator: "test maintenance administrator", Confirmation: "administrator-reviewed", AgentID: "a", ServerID: "s", AgentVersion: "legacy", PostgreSQLSHA256: fingerprint, AgentExportSHA256: exportHash, StateDatabaseSHA256: x.DatabaseSHA256, ControlGeneration: 1, InventoryComplete: true, ExecutionReconciled: true, FinalAccountingVerified: true, ReportsAcknowledged: true, TrafficStoppedAt: traffic, FinalAccountingAt: final, Fence: RestartFenceReview{Mechanism: "test parent owns exact child inventory and has no restart loop", ServiceIdentity: "test-owned PID scope", EstablishedAt: start, ValidUntil: time.Now().UTC().Add(time.Hour)}, AgentProcess: agent, Instances: []InstanceStopReview{{InstanceID: "c", NodeID: "n", Generation: 7, ConfigRevision: 0, CoreGeneration: "synthetic-core-7", ValidFrom: start, Process: core}}, ReviewedAt: time.Now().UTC(), Records: []StopReviewRecord{{File: "observations.json", SHA256: recordHash}}}
	if err = st.DB.QueryRow(`SELECT control_generation,revision_counter FROM control_state`).Scan(&review.PostgreSQLControlGeneration, &review.PostgreSQLRevision); err != nil {
		t.Fatal(err)
	}
	manifest := &UpgradeEvidence{FormatVersion: UpgradeEvidenceFormat, OwnerAssertions: []OwnerAssertion{owner}, AgentExports: []AgentEvidenceInput{{AgentID: "a", ObservedAgentVersion: "legacy", File: "agent.json", SHA256: exportHash, StoppedEvidenceFile: "stop.json", StopReviewFormat: UpgradeStopReviewFormat}}}
	st.upgradeEvidenceFile = filepath.Join(dir, "manifest.json")
	saveStopFixture(t, manifest, review, dir)
	return manifest, review, dir
}
func saveStopFixture(t *testing.T, m *UpgradeEvidence, v *UpgradeStopReview, dir string) {
	raw := upgradeMarshal(t, v)
	upgradeWrite(t, filepath.Join(dir, "stop.json"), raw)
	m.AgentExports[0].StoppedEvidenceSHA256 = upgradeHash(raw)
	upgradeWrite(t, filepath.Join(dir, "manifest.json"), upgradeMarshal(t, m))
}
func assertNoStopMigration(t *testing.T, st *Store) {
	t.Helper()
	if err := st.Migrate(context.Background(), regMigrationsDir(t)); err == nil {
		t.Fatal("invalid review permitted migration")
	}
	var exists bool
	if err := st.DB.QueryRow(`SELECT to_regclass('member_periods') IS NOT NULL OR to_regclass('node_server_history') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("DDL leaked: %v %v", exists, err)
	}
	var count int
	if err := st.DB.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil || count != 4 {
		t.Fatal(count, err)
	}
}
func TestAdministratorStopReadyImport(t *testing.T) {
	st := upgradeTestStore(t, true)
	_, _, _ = stopFixture(t, st)
	r := upgradeReady(t, st)
	if r.Status != "ready" || !strings.Contains(r.StopVerification, "管理员核对") || upgradeHas(r, "AGENT_INTENT_UNPROVEN") {
		t.Fatalf("%+v", r)
	}
	if err := st.Migrate(context.Background(), regMigrationsDir(t)); err != nil {
		t.Fatal(err)
	}
	var periods, owners, audits, versions int
	for q, p := range map[string]*int{`SELECT count(*) FROM member_periods`: &periods, `SELECT count(*) FROM node_server_history`: &owners, `SELECT count(*) FROM audit_logs WHERE action='upgrade.evidence.import' AND detail::text LIKE '%administrator-reviewed%'`: &audits, `SELECT count(*) FROM schema_migrations`: &versions} {
		if err := st.DB.QueryRow(q).Scan(p); err != nil {
			t.Fatal(err)
		}
	}
	if periods != 1 || owners != 1 || audits != 1 || versions != countShippedMigrations(t) {
		t.Fatal(periods, owners, audits, versions, countShippedMigrations(t))
	}
	t.Log("real owned PID exits + immutable legacy Agent export -> 管理员核对 ready -> 0005 periods/owners/audit registered")
}
func TestAdministratorStopRejects(t *testing.T) {
	cases := map[string]func(*UpgradeEvidence, *UpgradeStopReview, string){
		"wrong-database":      func(m *UpgradeEvidence, v *UpgradeStopReview, d string) { v.PostgreSQLSHA256 = strings.Repeat("0", 64) },
		"wrong-server":        func(m *UpgradeEvidence, v *UpgradeStopReview, d string) { v.ServerID = "other" },
		"wrong-agent-version": func(m *UpgradeEvidence, v *UpgradeStopReview, d string) { v.AgentVersion = "other" },
		"wrong-state-hash": func(m *UpgradeEvidence, v *UpgradeStopReview, d string) {
			v.StateDatabaseSHA256 = strings.Repeat("0", 64)
		},
		"pid-reused-as-core":          func(m *UpgradeEvidence, v *UpgradeStopReview, d string) { v.Instances[0].Process = v.AgentProcess },
		"no-process-start-identity":   func(m *UpgradeEvidence, v *UpgradeStopReview, d string) { v.Instances[0].Process.StartIdentity = "" },
		"no-fence":                    func(m *UpgradeEvidence, v *UpgradeStopReview, d string) { v.Fence.Mechanism = "" },
		"no-execution-reconciliation": func(m *UpgradeEvidence, v *UpgradeStopReview, d string) { v.ExecutionReconciled = false },
		"not-stopped": func(m *UpgradeEvidence, v *UpgradeStopReview, d string) {
			v.Instances[0].Process.Observation = "still-running"
		},
		"missing-instance":    func(m *UpgradeEvidence, v *UpgradeStopReview, d string) { v.Instances = nil },
		"wrong-generation":    func(m *UpgradeEvidence, v *UpgradeStopReview, d string) { v.Instances[0].Generation++ },
		"wrong-pg-generation": func(m *UpgradeEvidence, v *UpgradeStopReview, d string) { v.PostgreSQLControlGeneration++ },
		"wrong-revision":      func(m *UpgradeEvidence, v *UpgradeStopReview, d string) { v.Instances[0].ConfigRevision++ },
		"wrong-hash": func(m *UpgradeEvidence, v *UpgradeStopReview, d string) {
			v.AgentExportSHA256 = strings.Repeat("0", 64)
		},
		"capture-before-stop": func(m *UpgradeEvidence, v *UpgradeStopReview, d string) {
			v.AgentProcess.ObservedAt = time.Now().Add(time.Hour)
		},
		"sample-after-stop": func(m *UpgradeEvidence, v *UpgradeStopReview, d string) {
			v.FinalAccountingAt = v.AgentProcess.ObservedAt.Add(time.Second)
		},
		"expired-fence": func(m *UpgradeEvidence, v *UpgradeStopReview, d string) {
			v.Fence.ValidUntil = time.Now().Add(-time.Second)
		},
		"incomplete-inventory":   func(m *UpgradeEvidence, v *UpgradeStopReview, d string) { v.InventoryComplete = false },
		"unacknowledged-reports": func(m *UpgradeEvidence, v *UpgradeStopReview, d string) { v.ReportsAcknowledged = false },
		"sampling-failed":        func(m *UpgradeEvidence, v *UpgradeStopReview, d string) { v.FinalAccountingVerified = false },
		"wrong-window": func(m *UpgradeEvidence, v *UpgradeStopReview, d string) {
			v.Instances[0].ValidFrom = v.Instances[0].ValidFrom.Add(time.Second)
		},
		"no-admin-review": func(m *UpgradeEvidence, v *UpgradeStopReview, d string) {
			v.Confirmation = "tool-independently-observed"
		},
		"changed-record": func(m *UpgradeEvidence, v *UpgradeStopReview, d string) {
			upgradeWrite(t, filepath.Join(d, "observations.json"), []byte("changed"))
		},
		"pending-report": func(m *UpgradeEvidence, v *UpgradeStopReview, d string) {
			raw, err := os.ReadFile(filepath.Join(d, "agent.json"))
			if err != nil {
				t.Fatal(err)
			}
			var x UpgradeAgentStateInput
			if err = json.Unmarshal(raw, &x); err != nil {
				t.Fatal(err)
			}
			x.PendingReportCount = 1
			raw = upgradeMarshal(t, x)
			upgradeWrite(t, filepath.Join(d, "agent.json"), raw)
			m.AgentExports[0].SHA256 = upgradeHash(raw)
			v.AgentExportSHA256 = upgradeHash(raw)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			st := upgradeTestStore(t, true)
			m, v, d := stopFixture(t, st)
			mutate(m, v, d)
			saveStopFixture(t, m, v, d)
			if r := upgradeReady(t, st); r.Status != "blocked" {
				t.Fatalf("%+v", r)
			}
			assertNoStopMigration(t, st)
		})
	}
}
func TestAdministratorStopImportRollback(t *testing.T) {
	st := upgradeTestStore(t, true)
	// Install the failure before capturing the database fingerprint.
	upgradeExec(t, st, `CREATE FUNCTION reject_review_import() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'stop review injected failure'; END $$`)
	upgradeExec(t, st, `CREATE TRIGGER reject_review_import BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_review_import()`)
	stopFixture(t, st)
	if r := upgradeReady(t, st); r.Status != "ready" {
		t.Fatalf("%+v", r)
	}
	assertNoStopMigration(t, st)
}
