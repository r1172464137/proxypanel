package state

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func upgradeFixture(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.SaveIdentity(context.Background(), Identity{
		AgentID: "agt-evidence", Token: "private-token-do-not-export",
		ControlGeneration: "generation-evidence", AppliedRevision: 7,
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUpgradeEvidencePreservesInputAndExportsWholeQueue(t *testing.T) {
	s := upgradeFixture(t)
	ctx := context.Background()
	for _, query := range []string{
		`INSERT INTO batches(batch_id,created_at,sample_count,last_error) VALUES('batch-evidence',1,1,'private-batch-error')`,
		`INSERT INTO samples(batch_id,seq,node_id,member_id,core_generation,started_at,ended_at,upload,download) VALUES('batch-evidence',0,'node-evidence','member-evidence','core-evidence',1000,2000,123,456)`,
		`INSERT INTO instances(node_id,config,users,error,updated_at) VALUES('node-evidence','private-config-do-not-export','private-users-do-not-export','private-instance-error',1)`,
		`INSERT INTO reports(id,kind,payload,created_at,last_error) VALUES('report-evidence','applied','private-report-payload',1,'private-report-error')`,
	} {
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadUpgradeEvidence(ctx, s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if got.ApplyIntentTrackingSupported {
		t.Fatal("legacy state cannot prove absence of interrupted applies")
	}
	if got.AgentID != "agt-evidence" || got.AppliedRevision != 7 || got.PendingReportCount != 1 || len(got.PendingBatches) != 1 {
		t.Fatalf("wrong evidence identity or queue counts: %+v", got)
	}
	batch := got.PendingBatches[0]
	if batch.BatchID != "batch-evidence" || len(batch.Samples) != 1 {
		t.Fatal("export lost a batch or sample")
	}
	sample := batch.Samples[0]
	if sample.NodeID != "node-evidence" || sample.MemberID != "member-evidence" || sample.CoreGeneration != "core-evidence" ||
		sample.StartedAt.UnixMilli() != 1000 || sample.EndedAt.UnixMilli() != 2000 || sample.UploadBytes != 123 || sample.DownloadBytes != 456 {
		t.Fatalf("export changed persisted sample: %+v", sample)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-") {
		t.Fatal("export contains token, config, users, report or error payload")
	}
	after, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("inspection changed input database contents")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(s.Path() + suffix); !os.IsNotExist(err) {
			t.Fatalf("inspection created a sidecar %s: %v", suffix, err)
		}
	}
}

func TestUpgradeEvidenceRejectsUncheckpointedWAL(t *testing.T) {
	s := upgradeFixture(t)
	if _, err := ReadUpgradeEvidence(context.Background(), s.Dir()); err == nil || !strings.Contains(err.Error(), "recovery log") {
		t.Fatalf("expected explicit recovery-log refusal, got %v", err)
	}
}

func TestUpgradeEvidenceRejectsUnsupportedState(t *testing.T) {
	for _, tc := range []struct {
		name, query, reason string
	}{
		{"new-version", `UPDATE meta SET value='2' WHERE key='schema_version'`, "schema version"},
		{"hidden-table", `CREATE TABLE sqliteX_hidden_queue (payload TEXT)`, "unknown tables"},
		{"new-intent-key", `INSERT INTO meta(key,value) VALUES('pending_apply_intent','private-intent')`, "unknown intent"},
		{"new-column", `ALTER TABLE batches ADD COLUMN hidden_payload BLOB`, "unknown data"},
		{"orphan-sample", `INSERT INTO samples(batch_id,seq,node_id,member_id,core_generation,started_at,ended_at,upload,download) VALUES('missing',0,'n','m','g',1,2,1,2)`, "orphaned samples"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := upgradeFixture(t)
			if _, err := s.db.ExecContext(context.Background(), tc.query); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadUpgradeEvidence(context.Background(), s.Dir()); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("expected %s refusal, got %v", tc.reason, err)
			}
		})
	}
}

func TestUpgradeEvidenceRejectsLinkedDatabase(t *testing.T) {
	s := upgradeFixture(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(s.Path(), filepath.Join(dir, fileName)); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	if _, err := ReadUpgradeEvidence(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("expected linked database refusal, got %v", err)
	}
}
