// Package reporting samples the core counters into the local queue and uploads
// them reliably.
package reporting

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
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

// Retry policy of the counter query. A statsquery that fails with a transient
// error (a killed helper process, a deadline exceeded, an API port that is not
// dialable yet) is retried a bounded number of times with a growing delay
// before the cycle gives that instance up: a single lost query otherwise loses
// a whole sampling period, and the counters are accounting data.
//
// Retrying can never book the same traffic twice: the counters are cumulative,
// state.Measure is idempotent per (node, core generation, counter) baseline and
// a cycle measures every instance exactly once - the retry only decides which
// snapshot feeds that one measurement.
const (
	// DefaultSampleAttempts is the number of statsquery rounds per sampling
	// cycle (the first try plus two retries).
	DefaultSampleAttempts = 3
	// DefaultSampleBackoff is the delay before the second attempt; every
	// further retry doubles it, up to maxSampleBackoff.
	DefaultSampleBackoff = 500 * time.Millisecond
	maxSampleBackoff     = 10 * time.Second
)

// Sampler converts cumulative counters into member deltas and persists them
// together with the new baselines.
type Sampler struct {
	store    *state.Store
	source   StatsSource
	logger   *log.Logger
	now      func() time.Time
	attempts int
	backoff  time.Duration
	sleep    func(context.Context, time.Duration) error

	mu       sync.Mutex
	lastErrs []string
}

// NewSampler builds a sampler.
func NewSampler(store *state.Store, source StatsSource, logger *log.Logger) *Sampler {
	return &Sampler{
		store:    store,
		source:   source,
		logger:   logger,
		now:      func() time.Time { return time.Now().UTC() },
		attempts: DefaultSampleAttempts,
		backoff:  DefaultSampleBackoff,
		sleep:    sleepContext,
	}
}

// SetNow overrides the clock (tests).
func (s *Sampler) SetNow(fn func() time.Time) {
	if fn != nil {
		s.now = fn
	}
}

// SetRetryPolicy overrides how often a failing statsquery is retried and the
// base delay between attempts (tests and operators).
func (s *Sampler) SetRetryPolicy(attempts int, backoff time.Duration) {
	if attempts < 1 {
		attempts = 1
	}
	if backoff < 0 {
		backoff = 0
	}
	s.attempts = attempts
	s.backoff = backoff
}

// SetSleep overrides how the sampler waits between two attempts (tests).
func (s *Sampler) SetSleep(fn func(context.Context, time.Duration) error) {
	if fn != nil {
		s.sleep = fn
	}
}

// LastSamplingErrors reports the persistent sampling failures of the most recent
// cycle, one "sampling_error ..." line per instance. It is the queryable twin of
// SampleResult.SamplingErrors, so a status or heartbeat consumer can surface a
// degraded sampling loop instead of the failure being logged once and lost. It
// returns nil when the last cycle sampled every instance.
func (s *Sampler) LastSamplingErrors() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lastErrs...)
}

// SampleResult summarises one sampling round.
type SampleResult struct {
	Instances    int
	Counters     int
	SampleRows   int
	BatchIDs     []string
	Unmapped     int
	InstanceErrs []string
	// SamplingErrors lists the instances whose counters are still missing after
	// the retry budget: one "sampling_error node=... instance=... attempts=N:
	// <cause>" entry each. A degraded cycle is marked here instead of being
	// swallowed, so the caller can log, count or report it.
	SamplingErrors []string
	// Retries counts the extra statsquery rounds this cycle needed.
	Retries int
}

// sampleCandidate follows one instance through the retry rounds of a cycle.
// Every instance is measured exactly once per cycle, no matter how many attempts
// it took.
type sampleCandidate struct {
	instanceID string
	nodeID     string
	stats      xray.InstanceStats
	attempts   int
	err        error
}

// RunOnce samples all running instances. A failing statsquery is retried with a
// growing backoff; a node that still fails, or whose sample cannot be persisted,
// is marked in the result and never stops the other nodes from being sampled.
func (s *Sampler) RunOnce(ctx context.Context) (SampleResult, error) {
	var res SampleResult
	attempts := s.attempts
	if attempts < 1 {
		attempts = 1
	}
	candidates := make([]*sampleCandidate, 0)
	byKey := make(map[string]*sampleCandidate)
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			if !hasUnresolved(candidates) {
				break
			}
			if err := s.sleep(ctx, s.backoffFor(attempt)); err != nil {
				// The cycle was cancelled while waiting: report the instances
				// that are still missing instead of pretending they were read.
				break
			}
			res.Retries++
		}
		for _, inst := range s.source.QueryStats(ctx) {
			key := inst.InstanceID
			if key == "" {
				key = inst.NodeID
			}
			c, ok := byKey[key]
			if !ok {
				c = &sampleCandidate{instanceID: inst.InstanceID, nodeID: inst.NodeID}
				byKey[key] = c
				candidates = append(candidates, c)
			}
			c.attempts++
			if inst.Err != nil {
				c.err = inst.Err
				continue
			}
			c.stats = inst
			c.err = nil
		}
	}

	res.Instances = len(candidates)
	var persistErr error
	for _, c := range candidates {
		node := firstNonEmpty(c.nodeID, c.stats.NodeID)
		instance := firstNonEmpty(c.instanceID, node)
		if c.err != nil {
			res.SamplingErrors = append(res.SamplingErrors, fmt.Sprintf(
				"sampling_error node=%s instance=%s attempts=%d: %v",
				node, instance, c.attempts, c.err))
			continue
		}
		if c.attempts > 1 {
			s.logf("agent: sampling: instance %s recovered on attempt %d", instance, c.attempts)
		}
		items := make([]state.CounterItem, 0, len(c.stats.Counters))
		for _, counter := range c.stats.Counters {
			member, ok := MemberForCounter(counter.Name, c.stats.EmailToMember)
			if !ok {
				res.Unmapped++
				continue
			}
			res.Counters++
			items = append(items, state.CounterItem{MemberID: member, Counter: counter.Name, Value: counter.Value})
		}
		if len(items) == 0 {
			continue
		}
		measured, err := s.store.Measure(ctx, c.stats.NodeID, c.stats.CoreGeneration, s.now(), items)
		if err != nil {
			// One node must never stop the others: collect the failure, finish
			// the cycle and report it afterwards.
			res.SamplingErrors = append(res.SamplingErrors, fmt.Sprintf(
				"sampling_error node=%s instance=%s task=persist: %v", node, instance, err))
			if persistErr == nil {
				persistErr = fmt.Errorf("persist sample for node %s: %w", node, err)
			}
			continue
		}
		if measured.BatchID != "" {
			res.SampleRows += measured.SampleRows
			res.BatchIDs = append(res.BatchIDs, measured.BatchID)
		}
	}
	// InstanceErrs keeps the existing supervisor log line ("agent: sampling:
	// ...") working; it carries the same entries as SamplingErrors.
	res.InstanceErrs = append(res.InstanceErrs, res.SamplingErrors...)
	s.setLastErrors(res.SamplingErrors)
	return res, persistErr
}

// hasUnresolved reports whether any instance still misses its counters.
func hasUnresolved(candidates []*sampleCandidate) bool {
	for _, c := range candidates {
		if c.err != nil {
			return true
		}
	}
	return false
}

// backoffFor returns the delay before the given attempt (2, 3, ...).
func (s *Sampler) backoffFor(attempt int) time.Duration {
	if s.backoff <= 0 {
		return 0
	}
	delay := s.backoff
	for i := 2; i < attempt; i++ {
		delay *= 2
		if delay >= maxSampleBackoff {
			return maxSampleBackoff
		}
	}
	return delay
}

func (s *Sampler) setLastErrors(errs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErrs = append([]string(nil), errs...)
}

func (s *Sampler) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

// sleepContext waits for the given delay or until the context is cancelled.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// firstNonEmpty returns the first value that is not blank.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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
