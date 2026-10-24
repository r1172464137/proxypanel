package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"proxypanel/internal/contracts"
)

type upgradeFixture struct {
	path     string
	dir      string
	manifest UpgradeEvidence
	export   UpgradeAgentStateInput
}

func upgradeHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func upgradeWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func upgradeMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func newUpgradeFixture(t *testing.T) *upgradeFixture {
	t.Helper()
	dir := t.TempDir()
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	export := UpgradeAgentStateInput{
		FormatVersion: upgradeAgentEvidenceFormat, CapturedAt: end,
		DatabaseSHA256: strings.Repeat("a", 64), StateSchemaVersion: "retained-state-schema",
		AgentID: "agent-1", ControlGeneration: "generation-original", AppliedRevision: 12,
		PendingBatches: []contracts.UsageBatch{
			{SchemaVersion: contracts.SchemaVersion, BatchID: "batch-z", Samples: []contracts.UsageSample{
				{NodeID: "node-z", MemberID: "member-z", CoreGeneration: "core-z", StartedAt: start, EndedAt: end, UploadBytes: 37, DownloadBytes: 41},
				{NodeID: "node-a", MemberID: "member-a", CoreGeneration: "core-a", StartedAt: start, EndedAt: end, UploadBytes: 43, DownloadBytes: 47},
			}},
			{SchemaVersion: contracts.SchemaVersion, BatchID: "batch-a", Samples: []contracts.UsageSample{}},
		},
		PendingReportCount: 3, PendingApplyIntentCount: 0, ApplyIntentTrackingSupported: false,
	}
	artifact := upgradeMarshal(t, export)
	upgradeWrite(t, filepath.Join(dir, "agent.json"), artifact)
	proof := []byte("assertion only; not machine-executed or proof of historical truth\n")
	upgradeWrite(t, filepath.Join(dir, "basis.txt"), proof)
	revision := int64(12)
	manifest := UpgradeEvidence{
		FormatVersion:    UpgradeEvidenceFormat,
		AgentExports:     []AgentEvidenceInput{{AgentID: "agent-1", File: "agent.json", SHA256: upgradeHash(artifact), ObservedAgentVersion: "observed-old-version", StoppedEvidenceFile: "basis.txt", StoppedEvidenceSHA256: upgradeHash(proof)}},
		PeriodAssertions: []PeriodAssertion{{MemberID: "member-z", Start: start, End: end, Timezone: "Asia/Shanghai", AnchorDay: 31, ProvenanceFile: "basis.txt", ProvenanceSHA256: upgradeHash(proof)}},
		OwnerAssertions:  []OwnerAssertion{{NodeID: "node-z", ServerID: "server-explicit", AgentID: "agent-1", ValidFrom: start, ValidTo: &end, ReleasedRevision: &revision, ProvenanceFile: "basis.txt", ProvenanceSHA256: upgradeHash(proof)}},
	}
	f := &upgradeFixture{path: filepath.Join(dir, "manifest.json"), dir: dir, manifest: manifest, export: export}
	f.save(t)
	return f
}

func (f *upgradeFixture) save(t *testing.T) { upgradeWrite(t, f.path, upgradeMarshal(t, f.manifest)) }

func (f *upgradeFixture) saveExport(t *testing.T) {
	raw := upgradeMarshal(t, f.export)
	upgradeWrite(t, filepath.Join(f.dir, "agent.json"), raw)
	f.manifest.AgentExports[0].SHA256 = upgradeHash(raw)
	f.save(t)
}

func TestLoadUpgradeEvidenceValidAndUnchanged(t *testing.T) {
	f := newUpgradeFixture(t)
	before := map[string][]byte{}
	for _, name := range []string{"manifest.json", "agent.json", "basis.txt"} {
		data, err := os.ReadFile(filepath.Join(f.dir, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = data
	}
	got, err := LoadUpgradeEvidence(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ManifestSHA256 != upgradeHash(before["manifest.json"]) {
		t.Fatal("manifest hash was not over original bytes")
	}
	if !reflect.DeepEqual(got.AgentExports[0].Export, &f.export) {
		t.Fatalf("offline payload changed: %#v", got.AgentExports[0].Export)
	}
	if got.AgentExports[0].Export.ApplyIntentTrackingSupported || got.AgentExports[0].Export.PendingApplyIntentCount != 0 {
		t.Fatal("legacy intent facts changed")
	}
	if got.OwnerAssertions[0].ServerID != "server-explicit" || got.AgentExports[0].ObservedAgentVersion != "observed-old-version" {
		t.Fatal("assertions changed")
	}
	encoded := upgradeMarshal(t, got)
	if bytes.Contains(encoded, []byte("ManifestSHA256")) || bytes.Contains(encoded, []byte("pending_batches")) {
		t.Fatal("retained facts leaked into manifest JSON")
	}
	for name, original := range before {
		after, err := os.ReadFile(filepath.Join(f.dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(original, after) {
			t.Fatalf("input changed: %s", name)
		}
	}
}

func TestLoadUpgradeEvidenceEmptyAndMissing(t *testing.T) {
	got, err := LoadUpgradeEvidence("")
	if got != nil || err != nil {
		t.Fatalf("empty: %v %v", got, err)
	}
	got, err = LoadUpgradeEvidence(filepath.Join(t.TempDir(), "missing.json"))
	if got != nil || err == nil {
		t.Fatal("missing material must be an error, not absence")
	}
	f := newUpgradeFixture(t)
	f.manifest.OwnerAssertions[0].ValidTo = nil
	f.manifest.OwnerAssertions[0].ReleasedRevision = nil
	f.manifest.PeriodAssertions[0].Timezone = "UTC"
	f.save(t)
	// An exact 8MiB manifest is allowed, including original whitespace.
	raw := upgradeMarshal(t, f.manifest)
	raw = append(raw, bytes.Repeat([]byte(" "), upgradeManifestLimit-len(raw))...)
	upgradeWrite(t, f.path, raw)
	if got, err := LoadUpgradeEvidence(f.path); err != nil || got.ManifestSHA256 != upgradeHash(raw) {
		t.Fatalf("exact-limit manifest: %v", err)
	}
	// Active/pending facts are still materials, not an eligibility decision.
	f.export.ApplyIntentTrackingSupported = true
	f.export.PendingApplyIntentCount = 2
	f.saveExport(t)
	got, err = LoadUpgradeEvidence(f.path)
	if err != nil || got.AgentExports[0].Export.PendingApplyIntentCount != 2 {
		t.Fatalf("pending intent facts were not retained: %v", err)
	}
}

func TestLoadUpgradeEvidenceRejectsBadMaterials(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *upgradeFixture)
	}{
		{"bad-export-hash", func(t *testing.T, f *upgradeFixture) { f.manifest.AgentExports[0].SHA256 = strings.Repeat("0", 64) }},
		{"bad-stopped-hash", func(t *testing.T, f *upgradeFixture) {
			f.manifest.AgentExports[0].StoppedEvidenceSHA256 = strings.Repeat("0", 64)
		}},
		{"bad-period-hash", func(t *testing.T, f *upgradeFixture) {
			f.manifest.PeriodAssertions[0].ProvenanceSHA256 = strings.Repeat("0", 64)
		}},
		{"bad-owner-hash", func(t *testing.T, f *upgradeFixture) {
			f.manifest.OwnerAssertions[0].ProvenanceSHA256 = strings.Repeat("0", 64)
		}},
		{"malformed-hash", func(t *testing.T, f *upgradeFixture) { f.manifest.AgentExports[0].SHA256 = "no" }},
		{"escape", func(t *testing.T, f *upgradeFixture) { f.manifest.AgentExports[0].File = "../outside.json" }},
		{"internal-dotdot", func(t *testing.T, f *upgradeFixture) { f.manifest.AgentExports[0].File = "a/../agent.json" }},
		{"absolute", func(t *testing.T, f *upgradeFixture) {
			f.manifest.AgentExports[0].File = filepath.Join(f.dir, "agent.json")
		}},
		{"symlink", func(t *testing.T, f *upgradeFixture) {
			if err := os.Symlink("agent.json", filepath.Join(f.dir, "link")); err != nil {
				t.Fatal(err)
			}
			f.manifest.AgentExports[0].File = "link"
		}},
		{"directory-symlink", func(t *testing.T, f *upgradeFixture) {
			if err := os.Symlink(f.dir, filepath.Join(f.dir, "link")); err != nil {
				t.Fatal(err)
			}
			f.manifest.AgentExports[0].File = "link/agent.json"
		}},
		{"directory", func(t *testing.T, f *upgradeFixture) { f.manifest.AgentExports[0].File = "." }},
		{"fifo", func(t *testing.T, f *upgradeFixture) {
			if err := syscall.Mkfifo(filepath.Join(f.dir, "fifo"), 0600); err != nil {
				t.Fatal(err)
			}
			f.manifest.AgentExports[0].File = "fifo"
		}},
		{"missing-reference", func(t *testing.T, f *upgradeFixture) { f.manifest.AgentExports[0].File = "absent" }},
		{"agent-ID", func(t *testing.T, f *upgradeFixture) { f.export.AgentID = "different"; f.saveExport(t) }},
		{"agent-format", func(t *testing.T, f *upgradeFixture) {
			f.export.FormatVersion = "proxypanel.agent-state-evidence/v9"
			f.saveExport(t)
		}},
		{"manifest-format", func(t *testing.T, f *upgradeFixture) { f.manifest.FormatVersion = "proxypanel.upgrade-evidence/v9" }},
		{"version-required", func(t *testing.T, f *upgradeFixture) { f.manifest.AgentExports[0].ObservedAgentVersion = "" }},
		{"period-range", func(t *testing.T, f *upgradeFixture) {
			f.manifest.PeriodAssertions[0].End = f.manifest.PeriodAssertions[0].Start
		}},
		{"anchor-zero", func(t *testing.T, f *upgradeFixture) { f.manifest.PeriodAssertions[0].AnchorDay = 0 }},
		{"anchor-32", func(t *testing.T, f *upgradeFixture) { f.manifest.PeriodAssertions[0].AnchorDay = 32 }},
		{"local-timezone", func(t *testing.T, f *upgradeFixture) { f.manifest.PeriodAssertions[0].Timezone = "Local" }},
		{"empty-timezone", func(t *testing.T, f *upgradeFixture) { f.manifest.PeriodAssertions[0].Timezone = "" }},
		{"unknown-timezone", func(t *testing.T, f *upgradeFixture) { f.manifest.PeriodAssertions[0].Timezone = "Invalid/Zone" }},
		{"owner-range", func(t *testing.T, f *upgradeFixture) {
			f.manifest.OwnerAssertions[0].ValidTo = &f.manifest.OwnerAssertions[0].ValidFrom
		}},
		{"owner-server-required", func(t *testing.T, f *upgradeFixture) { f.manifest.OwnerAssertions[0].ServerID = "" }},
		{"negative-revision", func(t *testing.T, f *upgradeFixture) {
			n := int64(-1)
			f.manifest.OwnerAssertions[0].ReleasedRevision = &n
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUpgradeFixture(t)
			tt.mutate(t, f)
			f.save(t)
			if got, err := LoadUpgradeEvidence(f.path); err == nil || got != nil {
				t.Fatalf("accepted bad material: %v %v", got, err)
			}
		})
	}
}

func TestLoadUpgradeEvidenceStrictJSONAndLimit(t *testing.T) {
	for _, name := range []string{"manifest-unknown", "manifest-duplicate", "manifest-case-alias", "manifest-trailing", "manifest-garbage", "manifest-limit", "agent-unknown", "agent-trailing", "sample-unknown"} {
		t.Run(name, func(t *testing.T) {
			f := newUpgradeFixture(t)
			path := f.path
			data := upgradeMarshal(t, f.manifest)
			switch name {
			case "manifest-unknown":
				data = append([]byte(`{"surprise":1,`), data[1:]...)
			case "manifest-duplicate":
				data = append([]byte(`{"format_version":"proxypanel.upgrade-evidence/v1",`), data[1:]...)
			case "manifest-case-alias":
				data = bytes.Replace(data, []byte(`"format_version"`), []byte(`"FORMAT_VERSION"`), 1)
			case "manifest-trailing":
				data = append(data, []byte(` {}`)...)
			case "manifest-garbage":
				data = append(data, 'x')
			case "manifest-limit":
				data = append(data, bytes.Repeat([]byte(" "), upgradeManifestLimit)...)
			default:
				path = filepath.Join(f.dir, "agent.json")
				data = upgradeMarshal(t, f.export)
				if name == "agent-unknown" {
					data = append([]byte(`{"surprise":1,`), data[1:]...)
				}
				if name == "agent-trailing" {
					data = append(data, []byte(` null`)...)
				}
				if name == "sample-unknown" {
					data = bytes.Replace(data, []byte(`"node_id":"node-z"`), []byte(`"node_id":"node-z","payload":"secret"`), 1)
				}
				f.manifest.AgentExports[0].SHA256 = upgradeHash(data)
				f.save(t)
			}
			upgradeWrite(t, path, data)
			if got, err := LoadUpgradeEvidence(f.path); err == nil || got != nil {
				t.Fatalf("accepted invalid JSON/size: %v %v", got, err)
			}
		})
	}
}

func TestUpgradeEvidenceFingerprintAndManifestSymlink(t *testing.T) {
	f := newUpgradeFixture(t)
	path := filepath.Join(f.dir, "basis.txt")
	_, fingerprint, err := readUpgradeFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	upgradeWrite(t, path, []byte("replacement"))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if sameUpgradeFile(fingerprint.info, info) {
		t.Fatal("replacement not detected")
	}
	link := filepath.Join(f.dir, "manifest-link.json")
	if err := os.Symlink(f.path, link); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadUpgradeEvidence(link); err == nil || got != nil {
		t.Fatal("accepted manifest symlink")
	}
}
