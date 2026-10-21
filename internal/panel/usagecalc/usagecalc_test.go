package usagecalc

import (
	"math"
	"testing"
	"time"

	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/period"
)

var now = time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC)

func sample(node, member string, start, end time.Time, up, down int64) contracts.UsageSample {
	return contracts.UsageSample{NodeID: node, MemberID: member, StartedAt: start, EndedAt: end, UploadBytes: up, DownloadBytes: down}
}

func validator() Validator {
	return Validator{
		Now:           now,
		AgentServerID: "srv_1",
		NodeServer: func(nodeID string) (string, bool) {
			switch nodeID {
			case "nd_1", "nd_2":
				return "srv_1", true
			case "nd_other":
				return "srv_2", true
			}
			return "", false
		},
		NodeHasHistory: func(nodeID, memberID string) bool {
			return !(nodeID == "nd_2" && memberID == "mbr_unknown")
		},
	}
}

func TestValidateAcceptsWellFormedBatch(t *testing.T) {
	batch := contracts.UsageBatch{
		SchemaVersion: contracts.SchemaVersion,
		BatchID:       "b1",
		Samples: []contracts.UsageSample{
			sample("nd_1", "mbr_a", now.Add(-time.Minute), now, 100, 200),
			sample("nd_2", "mbr_b", now.Add(-time.Minute), now, 1, 2),
		},
	}
	if rejects := Validate(batch, validator()); len(rejects) != 0 {
		t.Fatalf("unexpected rejects: %+v", rejects)
	}
}

func TestValidateRejectsForeignNode(t *testing.T) {
	batch := contracts.UsageBatch{BatchID: "b2", Samples: []contracts.UsageSample{
		sample("nd_other", "mbr_a", now.Add(-time.Minute), now, 1, 1),
	}}
	rejects := Validate(batch, validator())
	if len(rejects) != 1 || rejects[0].Code != "BATCH_OWNERSHIP_MISMATCH" {
		t.Fatalf("rejects = %+v", rejects)
	}
}

func TestValidateRejectsUnknownNode(t *testing.T) {
	batch := contracts.UsageBatch{BatchID: "b3", Samples: []contracts.UsageSample{
		sample("nd_missing", "mbr_a", now.Add(-time.Minute), now, 1, 1),
	}}
	if rejects := Validate(batch, validator()); rejects[0].Code != "BATCH_OWNERSHIP_MISMATCH" {
		t.Fatalf("rejects = %+v", rejects)
	}
}

func TestValidateRejectsMissingHistory(t *testing.T) {
	batch := contracts.UsageBatch{BatchID: "b4", Samples: []contracts.UsageSample{
		sample("nd_2", "mbr_unknown", now.Add(-time.Minute), now, 1, 1),
	}}
	if rejects := Validate(batch, validator()); rejects[0].Field != "member_id" {
		t.Fatalf("rejects = %+v", rejects)
	}
}

func TestValidateRejectsNegativeAndInverted(t *testing.T) {
	cases := []contracts.UsageSample{
		{NodeID: "nd_1", MemberID: "mbr_a", StartedAt: now.Add(-time.Minute), EndedAt: now, UploadBytes: -1},
		{NodeID: "nd_1", MemberID: "mbr_a", StartedAt: now, EndedAt: now, UploadBytes: 1},
		{NodeID: "", MemberID: "mbr_a", StartedAt: now.Add(-time.Minute), EndedAt: now},
		{NodeID: "nd_1", MemberID: "", StartedAt: now.Add(-time.Minute), EndedAt: now},
	}
	for i, s := range cases {
		rejects := Validate(contracts.UsageBatch{BatchID: "b", Samples: []contracts.UsageSample{s}}, validator())
		if len(rejects) == 0 {
			t.Fatalf("case %d should be rejected", i)
		}
	}
}

func TestValidateClockSkew(t *testing.T) {
	batch := contracts.UsageBatch{BatchID: "b5", Samples: []contracts.UsageSample{
		sample("nd_1", "mbr_a", now.Add(time.Hour), now.Add(time.Hour+time.Minute), 1, 1),
	}}
	rejects := Validate(batch, validator())
	if len(rejects) != 1 || rejects[0].Code != "BATCH_CLOCK_SKEW" {
		t.Fatalf("rejects = %+v", rejects)
	}
	// slightly ahead within tolerance is fine
	batch.Samples[0] = sample("nd_1", "mbr_a", now.Add(-time.Minute), now.Add(time.Minute), 1, 1)
	if rejects := Validate(batch, validator()); len(rejects) != 0 {
		t.Fatalf("within tolerance must pass: %+v", rejects)
	}
}

func TestValidateSchemaAndSize(t *testing.T) {
	batch := contracts.UsageBatch{SchemaVersion: "proxypanel.agent/v2", BatchID: "b6"}
	if rejects := Validate(batch, validator()); rejects[0].Code != "SNAPSHOT_SCHEMA_UNSUPPORTED" {
		t.Fatalf("rejects = %+v", rejects)
	}
	v := validator()
	v.MaxBatchBytes = 10
	batch = contracts.UsageBatch{BatchID: "b7", Samples: []contracts.UsageSample{
		sample("nd_1", "mbr_a", now.Add(-time.Minute), now, 100, 100),
	}}
	if rejects := Validate(batch, v); len(rejects) != 1 || rejects[0].Field != "payload" {
		t.Fatalf("rejects = %+v", rejects)
	}
	v = validator()
	v.MaxSamples = 1
	batch.Samples = append(batch.Samples, sample("nd_1", "mbr_a", now.Add(-time.Minute), now, 1, 1))
	if rejects := Validate(batch, v); len(rejects) == 0 {
		t.Fatal("sample count limit must be enforced")
	}
}

func TestPayloadHashIsStableAndContentSensitive(t *testing.T) {
	a := contracts.UsageBatch{SchemaVersion: contracts.SchemaVersion, BatchID: "x", Samples: []contracts.UsageSample{
		sample("nd_1", "mbr_a", now.Add(-time.Minute), now, 1, 2),
	}}
	b := a
	if PayloadSHA256(a) != PayloadSHA256(b) {
		t.Fatal("same payload must hash identically")
	}
	b.Samples = []contracts.UsageSample{sample("nd_1", "mbr_a", now.Add(-time.Minute), now, 1, 3)}
	if PayloadSHA256(a) == PayloadSHA256(b) {
		t.Fatal("different payload must hash differently")
	}
	c := a
	c.Samples = []contracts.UsageSample{sample("nd_1", "mbr_b", now.Add(-time.Minute), now, 1, 2)}
	if PayloadSHA256(a) == PayloadSHA256(c) {
		t.Fatal("member change must change the hash")
	}
}

func TestAllocateCrossesBoundaryAndFlagsCorrection(t *testing.T) {
	loc, err := period.LoadTimezone("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	current := period.Containing(1, loc, now)
	windows := period.CandidateWindows(1, loc, now.Add(-50*24*time.Hour), now)
	// sample spanning the previous boundary
	s := sample("nd_1", "mbr_a", current.Start.Add(-30*time.Minute), current.Start.Add(30*time.Minute), 300, 600)
	allocs := Allocate(s, windows, current.Start)
	if len(allocs) != 2 {
		t.Fatalf("allocations = %+v", allocs)
	}
	if allocs[0].PeriodStart.Equal(current.Start) {
		t.Fatalf("first slice must belong to the closed cycle: %+v", allocs[0])
	}
	if !allocs[0].Correction || !allocs[0].BoundarySplit || !allocs[0].Estimated {
		t.Fatalf("previous cycle slice must be a flagged correction: %+v", allocs[0])
	}
	if allocs[1].Correction {
		t.Fatalf("current cycle slice is not a correction: %+v", allocs[1])
	}
	var up, down int64
	for _, a := range allocs {
		up += a.UpBytes
		down += a.DownBytes
	}
	if up != 300 || down != 600 {
		t.Fatalf("byte conservation broken: %d %d", up, down)
	}
}

func TestAllocateBackfillIntoClosedCycleIsCorrection(t *testing.T) {
	loc, _ := period.LoadTimezone("Asia/Shanghai")
	current := period.Containing(1, loc, now)
	windows := period.CandidateWindows(1, loc, now.Add(-80*24*time.Hour), now)
	old := current.Start.Add(-20 * 24 * time.Hour)
	s := sample("nd_1", "mbr_a", old, old.Add(time.Minute), 10, 20)
	allocs := Allocate(s, windows, current.Start)
	if len(allocs) != 1 || !allocs[0].Correction || allocs[0].Estimated {
		t.Fatalf("closed cycle backfill = %+v", allocs)
	}
}

func TestRejectCodePriority(t *testing.T) {
	rejects := []Reject{{Code: "VALIDATION_FAILED"}, {Code: "BATCH_OWNERSHIP_MISMATCH"}}
	if got := RejectCodePriority(rejects); got != "BATCH_OWNERSHIP_MISMATCH" {
		t.Fatalf("priority = %s", got)
	}
	if got := RejectCodePriority([]Reject{{Code: "BATCH_CLOCK_SKEW"}, {Code: "VALIDATION_FAILED"}}); got != "BATCH_CLOCK_SKEW" {
		t.Fatalf("priority = %s", got)
	}
}

func TestLargeTrafficIsNotPayloadSize(t *testing.T) {
	batch := contracts.UsageBatch{SchemaVersion: contracts.SchemaVersion, BatchID: "high-throughput", Samples: []contracts.UsageSample{
		sample("nd_1", "mbr_a", now.Add(-5*time.Second), now, 1<<30, 2<<30),
	}}
	if rejects := Validate(batch, validator()); len(rejects) != 0 {
		t.Fatalf("legitimate 3 GiB traffic rejected: %+v", rejects)
	}
	batch.Samples[0].UploadBytes = math.MaxInt64
	if rejects := Validate(batch, validator()); len(rejects) == 0 {
		t.Fatal("counter sum overflow accepted")
	}
	batch.Samples = []contracts.UsageSample{
		sample("nd_1", "mbr_a", now.Add(-5*time.Second), now, math.MaxInt64, 0),
		sample("nd_1", "mbr_b", now.Add(-5*time.Second), now, 1, 0),
	}
	if rejects := Validate(batch, validator()); len(rejects) == 0 {
		t.Fatal("batch aggregate overflow accepted")
	}
}
