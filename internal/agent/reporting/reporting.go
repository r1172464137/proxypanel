// Package reporting samples the core counters into the local queue and uploads
// them reliably.
package reporting

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"proxypanel/internal/agent/state"
	"proxypanel/internal/agent/xray"
	"proxypanel/internal/contracts"
)

// StatsSource yields the counters of the running core instances.
type StatsSource interface {
	QueryStats(ctx context.Context) []xray.InstanceStats
}

// UsageClient uploads one usage batch.
type UsageClient interface {
	Usage(ctx context.Context, batch contracts.UsageBatch) (contracts.UsageAck, error)
}

// ReportClient uploads one apply report.
type ReportClient interface {
	Applied(ctx context.Context, report contracts.ApplyReport) error
}

// Sampler converts cumulative counters into member deltas and persists them
// together with the new baselines.
type Sampler struct {
	store  *state.Store
	source StatsSource
	logger *log.Logger
	now    func() time.Time
}

// NewSampler builds a sampler.
func NewSampler(store *state.Store, source StatsSource, logger *log.Logger) *Sampler {
	return &Sampler{store: store, source: source, logger: logger, now: func() time.Time { return time.Now().UTC() }}
}

// SetNow overrides the clock (tests).
func (s *Sampler) SetNow(fn func() time.Time) {
	if fn != nil {
		s.now = fn
	}
}

// SampleResult summarises one sampling round.
type SampleResult struct {
	Instances    int
	Counters     int
	SampleRows   int
	BatchIDs     []string
	Unmapped     int
	InstanceErrs []string
}

// RunOnce samples all running instances.
func (s *Sampler) RunOnce(ctx context.Context) (SampleResult, error) {
	var res SampleResult
	stats := s.source.QueryStats(ctx)
	for _, inst := range stats {
		res.Instances++
		if inst.Err != nil {
			res.InstanceErrs = append(res.InstanceErrs, fmt.Sprintf("%s: %v", inst.InstanceID, inst.Err))
			continue
		}
		items := make([]state.CounterItem, 0, len(inst.Counters))
		for _, c := range inst.Counters {
			member, ok := MemberForCounter(c.Name, inst.EmailToMember)
			if !ok {
				res.Unmapped++
				continue
			}
			res.Counters++
			items = append(items, state.CounterItem{MemberID: member, Counter: c.Name, Value: c.Value})
		}
		if len(items) == 0 {
			continue
		}
		measured, err := s.store.Measure(ctx, inst.NodeID, inst.CoreGeneration, s.now(), items)
		if err != nil {
			return res, fmt.Errorf("persist sample for node %s: %w", inst.NodeID, err)
		}
		if measured.BatchID != "" {
			res.SampleRows += measured.SampleRows
			res.BatchIDs = append(res.BatchIDs, measured.BatchID)
		}
	}
	return res, nil
}

// MemberForCounter maps "user>>>email>>>traffic>>>uplink|downlink" to the stable
// member id of the instance.
func MemberForCounter(name string, emailToMember map[string]string) (string, bool) {
	parts := strings.Split(name, ">>>")
	if len(parts) != 4 || parts[0] != "user" || parts[2] != "traffic" {
		return "", false
	}
	if parts[3] != "uplink" && parts[3] != "downlink" {
		return "", false
	}
	member, ok := emailToMember[parts[1]]
	if !ok || strings.TrimSpace(member) == "" {
		return "", false
	}
	return member, true
}

// Uploader delivers queued batches and apply reports.
type Uploader struct {
	store      *state.Store
	usage      UsageClient
	reports    ReportClient
	logger     *log.Logger
	batchLimit int
}

// NewUploader builds an uploader.
func NewUploader(store *state.Store, usage UsageClient, reports ReportClient, logger *log.Logger) *Uploader {
	return &Uploader{store: store, usage: usage, reports: reports, logger: logger, batchLimit: 10}
}

// UploadResult summarises one upload round.
type UploadResult struct {
	Batches  int
	Samples  int
	Acked    int
	Failed   int
	Reports  int
	Errors   []string
	Retained []string
}

// RunOnce uploads queued batches. A batch is only deleted after the panel
// answered accepted or duplicate; every other outcome keeps the data.
func (u *Uploader) RunOnce(ctx context.Context) (UploadResult, error) {
	var res UploadResult
	pending, err := u.store.PendingBatches(ctx, u.batchLimit)
	if err != nil {
		return res, fmt.Errorf("read upload queue: %w", err)
	}
	var firstErr error
	for _, b := range pending {
		res.Batches++
		res.Samples += len(b.Samples)
		batch := contracts.UsageBatch{
			SchemaVersion: contracts.SchemaVersion,
			BatchID:       b.BatchID,
			Samples:       b.Samples,
		}
		ack, err := u.usage.Usage(ctx, batch)
		if err != nil {
			res.Failed++
			res.Retained = append(res.Retained, b.BatchID)
			res.Errors = append(res.Errors, fmt.Sprintf("batch %s: %v", b.BatchID, err))
			if markErr := u.store.MarkBatchAttempt(ctx, b.BatchID, err.Error()); markErr != nil {
				u.logf("agent: cannot record upload attempt for %s: %v", b.BatchID, markErr)
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("upload batch %s: %w", b.BatchID, err)
			}
			break
		}
		if ack.BatchID != "" && ack.BatchID != b.BatchID {
			err := fmt.Errorf("panel acknowledged batch %q while the agent sent %q", ack.BatchID, b.BatchID)
			res.Failed++
			res.Retained = append(res.Retained, b.BatchID)
			res.Errors = append(res.Errors, err.Error())
			_ = u.store.MarkBatchAttempt(ctx, b.BatchID, err.Error())
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		switch ack.Status {
		case "accepted", "duplicate":
			if err := u.store.DeleteBatch(ctx, b.BatchID); err != nil {
				return res, fmt.Errorf("delete acknowledged batch %s: %w", b.BatchID, err)
			}
			res.Acked++
		default:
			err := fmt.Errorf("panel returned status %q which is not a durable acknowledgement", ack.Status)
			res.Failed++
			res.Retained = append(res.Retained, b.BatchID)
			res.Errors = append(res.Errors, fmt.Sprintf("batch %s: %v", b.BatchID, err))
			_ = u.store.MarkBatchAttempt(ctx, b.BatchID, err.Error())
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return res, firstErr
}

// UploadReports delivers queued apply reports.
func (u *Uploader) UploadReports(ctx context.Context) (int, error) {
	if u.reports == nil {
		return 0, nil
	}
	pending, err := u.store.PendingReports(ctx, u.batchLimit)
	if err != nil {
		return 0, fmt.Errorf("read report queue: %w", err)
	}
	sent := 0
	var firstErr error
	for _, r := range pending {
		switch r.Kind {
		case state.ReportKindApplied:
			var report contracts.ApplyReport
			if err := json.Unmarshal(r.Payload, &report); err != nil {
				_ = u.store.MarkReportAttempt(ctx, r.ID, err.Error())
				if firstErr == nil {
					firstErr = fmt.Errorf("report %s: %w", r.ID, err)
				}
				continue
			}
			if err := u.reports.Applied(ctx, report); err != nil {
				_ = u.store.MarkReportAttempt(ctx, r.ID, err.Error())
				if firstErr == nil {
					firstErr = fmt.Errorf("report %s: %w", r.ID, err)
				}
				continue
			}
			if err := u.store.DeleteReport(ctx, r.ID); err != nil {
				return sent, fmt.Errorf("delete acknowledged report %s: %w", r.ID, err)
			}
			sent++
		default:
			err := fmt.Errorf("unknown report kind %q", r.Kind)
			_ = u.store.MarkReportAttempt(ctx, r.ID, err.Error())
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return sent, firstErr
}

func (u *Uploader) logf(format string, args ...any) {
	if u.logger != nil {
		u.logger.Printf(format, args...)
	}
}
