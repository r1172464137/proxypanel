// Package usagecalc validates agent usage batches and turns samples into ledger
// allocations. Rules: docs/specs/product.md §8.5 and plan §3 F09.
package usagecalc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/period"
)

// MeteringAlgorithmVersion is shared with the agent and recorded on ledger rows.
const MeteringAlgorithmVersion = "metre/v1"

// Direction documents the counter source: ingress per user, which avoids double
// counting at the exit.
const Direction = "ingress_user"

// Limits guard payload size and clock sanity. They are defaults overridable by
// settings; the values are recorded in the implementation report.
const (
	DefaultSkewTolerance = 5 * time.Minute
	DefaultMaxSamples    = 10000
	DefaultMaxBatchBytes = 1 << 20 // serialized JSON bytes, NOT metered traffic
	MaxSampleDuration    = 6 * time.Hour
)

// Reject describes one refused sample without exposing secrets.
type Reject struct {
	Index   int    `json:"index"`
	Code    string `json:"code"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Validator supplies ownership and history lookups; the store implements it
// with PostgreSQL. Historical credentials count: a member who was disabled or
// rotated still has valid history and must be credited.
type Validator struct {
	Now            time.Time
	SkewTolerance  time.Duration
	MaxSamples     int
	MaxBatchBytes  int64
	AgentServerID  string
	NodeServer     func(nodeID string) (serverID string, exists bool)
	NodeHasHistory func(nodeID, memberID string) bool
}

// PayloadSHA256 is the canonical fingerprint used for batch idempotency.
func PayloadSHA256(batch contracts.UsageBatch) string {
	sum := sha256.Sum256(canonicalJSON(batch))
	return hex.EncodeToString(sum[:])
}

func canonicalJSON(batch contracts.UsageBatch) []byte {
	// Contracts structs have fixed field order, so marshalling is canonical.
	// SchemaVersion is included: the same batch id with a different protocol
	// version is a different payload and must be rejected.
	raw, err := json.Marshal(struct {
		SchemaVersion string                  `json:"schema_version"`
		BatchID       string                  `json:"batch_id"`
		Samples       []contracts.UsageSample `json:"samples"`
	}{batch.SchemaVersion, batch.BatchID, batch.Samples})
	if err != nil {
		return []byte(batch.BatchID)
	}
	return raw
}

// Validate checks structural limits and per-sample ownership/values. The
// returned rejects are non-empty when the batch must not be accounted.
func Validate(batch contracts.UsageBatch, v Validator) []Reject {
	var rejects []Reject
	now := v.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	skew := v.SkewTolerance
	if skew <= 0 {
		skew = DefaultSkewTolerance
	}
	maxSamples := v.MaxSamples
	if maxSamples <= 0 {
		maxSamples = DefaultMaxSamples
	}
	maxBytes := v.MaxBatchBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBatchBytes
	}
	if raw, err := json.Marshal(batch); err != nil || int64(len(raw)) > maxBytes {
		rejects = append(rejects, Reject{Index: -1, Code: "VALIDATION_FAILED", Field: "payload", Message: "serialized usage payload exceeds the accepted limit"})
	}
	if batch.BatchID == "" {
		rejects = append(rejects, Reject{Index: -1, Code: "VALIDATION_FAILED", Field: "batch_id", Message: "batch_id is required"})
	}
	if len(batch.Samples) > maxSamples {
		rejects = append(rejects, Reject{Index: -1, Code: "VALIDATION_FAILED", Field: "samples", Message: fmt.Sprintf("batch has %d samples, limit is %d", len(batch.Samples), maxSamples)})
	}
	if batch.SchemaVersion != "" && batch.SchemaVersion != contracts.SchemaVersion {
		rejects = append(rejects, Reject{Index: -1, Code: "SNAPSHOT_SCHEMA_UNSUPPORTED", Field: "schema_version", Message: "unsupported schema version " + batch.SchemaVersion})
	}
	var total int64
	for i, s := range batch.Samples {
		add := func(code, field, msg string) {
			rejects = append(rejects, Reject{Index: i, Code: code, Field: field, Message: msg})
		}
		switch {
		case s.NodeID == "":
			add("VALIDATION_FAILED", "node_id", "node_id is required")
			continue
		case s.MemberID == "":
			add("VALIDATION_FAILED", "member_id", "member_id is required")
			continue
		case s.UploadBytes < 0 || s.DownloadBytes < 0:
			add("VALIDATION_FAILED", "bytes", "byte counters must not be negative")
			continue
		case !s.EndedAt.After(s.StartedAt):
			add("VALIDATION_FAILED", "sampling", "sample end must be after start")
			continue
		}
		if s.EndedAt.Sub(s.StartedAt) > MaxSampleDuration {
			add("VALIDATION_FAILED", "sampling", "sample interval is longer than the supported maximum")
			continue
		}
		if s.EndedAt.After(now.Add(skew)) {
			add("BATCH_CLOCK_SKEW", "sampling", "sample ends in the future beyond the clock tolerance")
			continue
		}
		if v.NodeServer != nil {
			serverID, exists := v.NodeServer(s.NodeID)
			if !exists {
				add("BATCH_OWNERSHIP_MISMATCH", "node_id", "node does not exist")
				continue
			}
			if v.AgentServerID != "" && serverID != v.AgentServerID {
				add("BATCH_OWNERSHIP_MISMATCH", "node_id", "node belongs to another server")
				continue
			}
		}
		if v.NodeHasHistory != nil && !v.NodeHasHistory(s.NodeID, s.MemberID) {
			add("BATCH_OWNERSHIP_MISMATCH", "member_id", "no credential history for this member on the node")
			continue
		}
		if s.UploadBytes > math.MaxInt64-s.DownloadBytes || total > math.MaxInt64-s.UploadBytes-s.DownloadBytes {
			add("VALIDATION_FAILED", "bytes", "byte counters exceed the signed 64-bit accounting range")
			continue
		}
		total += s.UploadBytes + s.DownloadBytes
	}
	return rejects
}

// Allocation is one ledger row.
type Allocation struct {
	PeriodStart   time.Time
	PeriodEnd     time.Time
	UpBytes       int64
	DownBytes     int64
	Estimated     bool
	BoundarySplit bool
	Correction    bool
}

// Allocate distributes one sample over the candidate cycles. Samples entirely
// inside a closed cycle are recorded as corrections; samples straddling a
// boundary are split by overlap duration and flagged estimated.
func Allocate(sample contracts.UsageSample, windows []period.Boundary, currentStart time.Time) []Allocation {
	slices := period.AllocateSample(sample.StartedAt, sample.EndedAt, windows, sample.UploadBytes, sample.DownloadBytes)
	out := make([]Allocation, 0, len(slices))
	for _, s := range slices {
		out = append(out, Allocation{
			PeriodStart:   s.Period.Start,
			PeriodEnd:     s.Period.End,
			UpBytes:       s.UpBytes,
			DownBytes:     s.DownBytes,
			Estimated:     s.Estimated,
			BoundarySplit: s.Split,
			Correction:    currentStart.IsZero() || s.Period.Start.Before(currentStart),
		})
	}
	return out
}

// RejectCodePriority picks the HTTP-relevant code for a set of rejects.
func RejectCodePriority(rejects []Reject) string {
	rank := map[string]int{
		"VALIDATION_FAILED":           0,
		"BATCH_CLOCK_SKEW":            1,
		"SNAPSHOT_SCHEMA_UNSUPPORTED": 2,
		"BATCH_OWNERSHIP_MISMATCH":    3,
	}
	best := ""
	for _, r := range rejects {
		if best == "" || rank[r.Code] > rank[best] {
			best = r.Code
		}
	}
	return best
}
