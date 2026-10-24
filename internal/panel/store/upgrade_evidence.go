package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
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
	AgentID               string                  `json:"agent_id"`
	File                  string                  `json:"file"`
	SHA256                string                  `json:"sha256"`
	ObservedAgentVersion  string                  `json:"observed_agent_version"`
	StoppedEvidenceFile   string                  `json:"stopped_evidence_file"`
	StoppedEvidenceSHA256 string                  `json:"stopped_evidence_sha256"`
	Export                *UpgradeAgentStateInput `json:"-"`
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
		data, err := read(filepath.Join(filepath.Dir(abs), name), 0)
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
		if _, err := reference(a.StoppedEvidenceFile, a.StoppedEvidenceSHA256); err != nil {
			return nil, err
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

func sameUpgradeFile(a, b os.FileInfo) bool {
	if !os.SameFile(a, b) || a.Mode() != b.Mode() || a.Size() != b.Size() || !a.ModTime().Equal(b.ModTime()) {
		return false
	}
	// Include change time, but not access time (our own read can update it).
	left, leftOK := a.Sys().(*syscall.Stat_t)
	right, rightOK := b.Sys().(*syscall.Stat_t)
	return leftOK && rightOK && left.Ctim == right.Ctim
}

// Walk pinned roots rather than following a path between Lstat and Open.
// Each component must be a real directory and the leaf a regular file.
// Root confinement also prevents a racing symlink from escaping its parent.
func openUpgradeFile(path string) (*os.File, error) {
	volume := filepath.VolumeName(path)
	root, err := os.OpenRoot(volume + string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(path, volume), string(filepath.Separator)), string(filepath.Separator))
	for i, part := range parts {
		before, err := root.Lstat(part)
		if err != nil {
			return nil, err
		}
		if i == len(parts)-1 {
			if !before.Mode().IsRegular() {
				return nil, fmt.Errorf("not a regular evidence file: %q", path)
			}
			// No-follow closes the Lstat/Open race; nonblocking prevents a
			// raced FIFO from hanging before its descriptor can be checked.
			file, err := root.OpenFile(part, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if err != nil {
				return nil, err
			}
			after, err := file.Stat()
			if err != nil || !sameUpgradeFile(before, after) {
				_ = file.Close()
				return nil, fmt.Errorf("evidence changed while opening %q: %v", path, err)
			}
			return file, nil
		}
		if !before.IsDir() {
			return nil, fmt.Errorf("not a real evidence directory: %q", part)
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			return nil, err
		}
		after, err := next.Stat(".")
		entry, entryErr := root.Lstat(part)
		if err != nil || entryErr != nil || !sameUpgradeFile(before, after) || !sameUpgradeFile(before, entry) {
			_ = next.Close()
			return nil, fmt.Errorf("evidence directory changed: %q", part)
		}
		_ = root.Close()
		root = next
	}
	return nil, errors.New("missing evidence filename")
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
