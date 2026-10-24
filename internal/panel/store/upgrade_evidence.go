package store

import (
	"context"
	"database/sql"

	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"proxypanel/internal/contracts"
)

const UpgradeEvidenceFormat = "proxypanel.upgrade-evidence/v1"
const upgradeAgentEvidenceFormat = "proxypanel.agent-state-evidence/v1"
const upgradeManifestLimit = 8 << 20

// UpgradeEvidence contains assertions and file-integrity facts, NOT verified
// history, proof of a stopped agent, or permission to perform an upgrade.
// An empty path means no materials, not readiness. No database is consulted.
type UpgradeEvidence struct {
	FormatVersion    string               `json:"format_version"`
	AgentExports     []AgentEvidenceInput `json:"agent_exports"`
	PeriodAssertions []PeriodAssertion    `json:"period_assertions"`
	OwnerAssertions  []OwnerAssertion     `json:"owner_assertions"`
	ManifestSHA256   string               `json:"-"`
}

type AgentEvidenceInput struct {
	AgentID               string `json:"agent_id"`
	File                  string `json:"file"`
	SHA256                string `json:"sha256"`
	ObservedAgentVersion  string `json:"observed_agent_version"`
	StoppedEvidenceFile   string `json:"stopped_evidence_file"`
	StoppedEvidenceSHA256 string `json:"stopped_evidence_sha256"`
	// Opt-in to the strict administrator-reviewed stop record in the existing attachment.
	StopReviewFormat string                  `json:"stop_review_format,omitempty"`
	StopReview       *UpgradeStopReview      `json:"-"`
	Export           *UpgradeAgentStateInput `json:"-"`
}

// UpgradeAgentStateInput is the complete sanitized offline AgentStateEvidence
// shape. It deliberately does not change or extend any HTTP contract. A zero
// PendingApplyIntentCount is not proof of completion; unsupported tracking is
// retained as a fact for the caller's subsequent checks.
type UpgradeAgentStateInput struct {
	FormatVersion                string                 `json:"format_version"`
	CapturedAt                   time.Time              `json:"captured_at"`
	DatabaseSHA256               string                 `json:"database_sha256"`
	StateSchemaVersion           string                 `json:"state_schema_version"`
	AgentID                      string                 `json:"agent_id"`
	ControlGeneration            string                 `json:"control_generation"`
	AppliedRevision              int64                  `json:"applied_revision"`
	PendingBatches               []contracts.UsageBatch `json:"pending_batches"`
	PendingReportCount           int64                  `json:"pending_report_count"`
	PendingApplyIntentCount      int64                  `json:"pending_apply_intent_count"`
	ApplyIntentTrackingSupported bool                   `json:"apply_intent_tracking_supported"`
}

type PeriodAssertion struct {
	MemberID         string    `json:"member_id"`
	Start            time.Time `json:"start"`
	End              time.Time `json:"end"`
	Timezone         string    `json:"timezone"`
	AnchorDay        int       `json:"anchor_day"`
	ProvenanceFile   string    `json:"provenance_file"`
	ProvenanceSHA256 string    `json:"provenance_sha256"`
}

type OwnerAssertion struct {
	NodeID           string     `json:"node_id"`
	ServerID         string     `json:"server_id"`
	AgentID          string     `json:"agent_id"`
	ValidFrom        time.Time  `json:"valid_from"`
	ValidTo          *time.Time `json:"valid_to"`
	ReleasedRevision *int64     `json:"released_revision"`
	ProvenanceFile   string     `json:"provenance_file"`
	ProvenanceSHA256 string     `json:"provenance_sha256"`
}

// LoadUpgradeEvidence only checks material self-consistency. File hashes do not
// establish that a stopped-evidence assertion or any historical claim is true.
func LoadUpgradeEvidence(path string) (*UpgradeEvidence, error) {
	if path == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	var files []upgradeFileFingerprint
	read := func(name string, limit int64) ([]byte, error) {
		data, fingerprint, err := readUpgradeFile(name, limit)
		if err == nil {
			files = append(files, fingerprint)
		}
		return data, err
	}
	raw, err := read(abs, upgradeManifestLimit)
	if err != nil {
		return nil, fmt.Errorf("upgrade manifest: %w", err)
	}
	var evidence UpgradeEvidence
	if err := decodeUpgradeJSON(raw, &evidence); err != nil {
		return nil, fmt.Errorf("upgrade manifest: %w", err)
	}
	if evidence.FormatVersion != UpgradeEvidenceFormat {
		return nil, fmt.Errorf("unsupported upgrade evidence format %q", evidence.FormatVersion)
	}
	digest := sha256.Sum256(raw)
	evidence.ManifestSHA256 = hex.EncodeToString(digest[:])
	reference := func(name, expected string) ([]byte, error) {
		if name == "" || filepath.IsAbs(name) || !filepath.IsLocal(name) || strings.Contains(name, "\\") {
			return nil, fmt.Errorf("invalid relative evidence path %q", name)
		}
		for _, part := range strings.Split(filepath.ToSlash(name), "/") {
			if part == ".." {
				return nil, fmt.Errorf("parent traversal in evidence path %q", name)
			}
		}
		want, err := hex.DecodeString(expected)
		if err != nil || len(want) != sha256.Size {
			return nil, fmt.Errorf("invalid SHA256 for %q", name)
		}
		data, err := read(filepath.Join(filepath.Dir(abs), name), upgradeManifestLimit)
		if err != nil {
			return nil, fmt.Errorf("evidence %q: %w", name, err)
		}
		got := sha256.Sum256(data)
		if !bytes.Equal(want, got[:]) {
			return nil, fmt.Errorf("SHA256 mismatch for %q", name)
		}
		return data, nil
	}
	for i := range evidence.AgentExports {
		a := &evidence.AgentExports[i]
		if strings.TrimSpace(a.AgentID) == "" || strings.TrimSpace(a.ObservedAgentVersion) == "" {
			return nil, fmt.Errorf("agent_exports[%d]: agent ID and observed version required", i)
		}
		data, err := reference(a.File, a.SHA256)
		if err != nil {
			return nil, err
		}
		a.Export = new(UpgradeAgentStateInput)
		if err := decodeUpgradeJSON(data, a.Export); err != nil {
			return nil, fmt.Errorf("agent export %q: %w", a.File, err)
		}
		if a.Export.FormatVersion != upgradeAgentEvidenceFormat {
			return nil, fmt.Errorf("unsupported agent evidence format %q", a.Export.FormatVersion)
		}
		if a.Export.AgentID != a.AgentID {
			return nil, fmt.Errorf("agent ID mismatch for %q", a.File)
		}
		stop, err := reference(a.StoppedEvidenceFile, a.StoppedEvidenceSHA256)
		if err != nil {
			return nil, err
		}
		if a.StopReviewFormat != "" {
			if a.StopReviewFormat != UpgradeStopReviewFormat {
				return nil, fmt.Errorf("unsupported stop review format %q", a.StopReviewFormat)
			}
			a.StopReview = new(UpgradeStopReview)
			if err := decodeUpgradeJSON(stop, a.StopReview); err != nil {
				return nil, fmt.Errorf("administrator stop review: %w", err)
			}
			for _, record := range a.StopReview.Records {
				if _, err := reference(record.File, record.SHA256); err != nil {
					return nil, err
				}
			}
		}
	}
	for i, p := range evidence.PeriodAssertions {
		if strings.TrimSpace(p.MemberID) == "" || p.Start.IsZero() || !p.Start.Before(p.End) || p.AnchorDay < 1 || p.AnchorDay > 31 {
			return nil, fmt.Errorf("period_assertions[%d]: invalid member, interval or anchor", i)
		}
		if p.Timezone != "UTC" && (!strings.Contains(p.Timezone, "/") || p.Timezone == "Local") {
			return nil, fmt.Errorf("period_assertions[%d]: explicit IANA timezone or UTC required", i)
		}
		if _, err := time.LoadLocation(p.Timezone); err != nil {
			return nil, fmt.Errorf("period_assertions[%d]: %w", i, err)
		}
		if _, err := reference(p.ProvenanceFile, p.ProvenanceSHA256); err != nil {
			return nil, err
		}
	}
	for i, o := range evidence.OwnerAssertions {
		if strings.TrimSpace(o.NodeID) == "" || strings.TrimSpace(o.ServerID) == "" || strings.TrimSpace(o.AgentID) == "" || o.ValidFrom.IsZero() ||
			(o.ValidTo != nil && !o.ValidFrom.Before(*o.ValidTo)) || (o.ReleasedRevision != nil && *o.ReleasedRevision < 0) {
			return nil, fmt.Errorf("owner_assertions[%d]: invalid identity, interval or revision", i)
		}
		if _, err := reference(o.ProvenanceFile, o.ProvenanceSHA256); err != nil {
			return nil, err
		}
	}
	// Detect replacement/modification not only during each read, but also while
	// other materials were being loaded. Any I/O failure remains an error.
	for _, fingerprint := range files {
		file, err := openUpgradeFile(fingerprint.path)
		if err != nil {
			return nil, err
		}
		info, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || closeErr != nil {
			return nil, errors.Join(statErr, closeErr)
		}
		if !sameUpgradeFile(fingerprint.info, info) {
			return nil, fmt.Errorf("evidence changed: %q", fingerprint.path)
		}
	}
	return &evidence, nil
}

func decodeUpgradeJSON(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON content")
		}
		return err
	}
	// encoding/json otherwise accepts duplicate keys and case-insensitive
	// field aliases; neither is part of this fixed evidence format.
	return checkUpgradeJSONKeys(json.NewDecoder(bytes.NewReader(data)), reflect.TypeOf(out).Elem())
}

func checkUpgradeJSONKeys(decoder *json.Decoder, shape reflect.Type) error {
	for shape.Kind() == reflect.Pointer {
		shape = shape.Elem()
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		fields := map[string]reflect.Type{}
		if shape.Kind() == reflect.Struct {
			for i := 0; i < shape.NumField(); i++ {
				field := shape.Field(i)
				key := strings.Split(field.Tag.Get("json"), ",")[0]
				if key != "" && key != "-" {
					fields[key] = field.Type
				}
			}
		}
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key := keyToken.(string)
			field, ok := fields[key]
			if !ok || seen[key] {
				return fmt.Errorf("unknown or duplicate JSON field %q", key)
			}
			seen[key] = true
			if err := checkUpgradeJSONKeys(decoder, field); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case json.Delim('['):
		for decoder.More() {
			if err := checkUpgradeJSONKeys(decoder, shape.Elem()); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	}
	return nil
}

type upgradeFileFingerprint struct {
	path string
	info os.FileInfo
}

// lockUpgradeTables prevents the checked old facts changing through the import.
// The random-schema test harness and production both use current_schema only.
func lockUpgradeTables(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT tablename FROM pg_tables WHERE schemaname=current_schema() ORDER BY tablename`)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		if _, err = tx.ExecContext(ctx, `LOCK TABLE `+pgx.Identifier{name}.Sanitize()+` IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return err
		}
	}
	return nil
}

// importUpgradeEvidenceTx is called only after 0005 DDL, in that same file's
// transaction and before its success registration. It never updates old rows.
func (s *Store) importUpgradeEvidenceTx(ctx context.Context, tx *sql.Tx, proof *UpgradeReadiness) error {
	if proof == nil || proof.Status != "ready" {
		return errors.New("ready proof required")
	}
	for _, p := range proof.periods {
		source := "proven_ledger"
		if p.ProvenanceFile == "" {
			source = "proven_current"
		}
		if err := recordMemberPeriodTx(ctx, tx, PeriodRecord{MemberID: p.MemberID, PeriodStart: p.Start, PeriodEnd: p.End, AnchorDay: p.AnchorDay, Timezone: p.Timezone, Source: source}); err != nil {
			return err
		}
	}
	for _, o := range proof.owners {
		var confirmed any
		if o.ValidTo != nil {
			confirmed = *o.ValidTo
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO node_server_history (id,node_id,server_id,agent_id,valid_from,valid_to,released_revision,release_confirmed_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, newID("own"), o.NodeID, o.ServerID, o.AgentID, o.ValidFrom, o.ValidTo, o.ReleasedRevision, confirmed); err != nil {
			return err
		}
	}
	detail, err := json.Marshal(map[string]any{"evidence_sha256": proof.EvidenceSHA256, "database_sha256": proof.DatabaseSHA256, "period_count": len(proof.periods), "owner_count": len(proof.owners), "pending_batches": proof.PendingBatches, "duplicate_batches": proof.DuplicateBatches, "stop_verification": proof.StopVerification})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_logs (id,action,target_type,detail) VALUES ($1,'upgrade.evidence.import','migration',$2)`, newID("aud"), detail)
	return err
}

func readUpgradeFile(path string, limit int64) ([]byte, upgradeFileFingerprint, error) {
	var fingerprint upgradeFileFingerprint
	file, err := openUpgradeFile(path)
	if err != nil {
		return nil, fingerprint, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return nil, fingerprint, err
	}
	var reader io.Reader = file
	if limit > 0 {
		reader = io.LimitReader(file, limit+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fingerprint, err
	}
	if limit > 0 && int64(len(data)) > limit {
		return nil, fingerprint, fmt.Errorf("manifest exceeds %d bytes", limit)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, fingerprint, err
	}
	if !sameUpgradeFile(before, after) {
		return nil, fingerprint, fmt.Errorf("evidence changed during read: %q", path)
	}
	return data, upgradeFileFingerprint{path: path, info: after}, nil
}
