// Package maintenance is the panel's periodic operations pass (plan F11/F12):
//
//  1. alert state machine reconciliation (agent offline, failed core instance,
//     member expiry / over quota, server expiry) with deduplicated open events
//     and explicit recovery events,
//  2. delivery of a durable PostgreSQL notification outbox to Telegram,
//  3. bounded retention cleanup of data that is safe to drop.
//
// PostgreSQL is the authority for every step. The reconciliation and cleanup
// run inside one transaction that holds a pg advisory lock
// (store.InMaintenanceTx), so only one panel process performs a pass at a time.
// Telegram delivery runs outside that transaction and is idempotent per event.
package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"proxypanel/internal/panel/store"
)

const (
	// telegramAPIBase is the only endpoint the panel ever talks to. There is no
	// constructor argument, environment variable or settings field that can
	// change it: notifications can never be redirected to another host (SSRF).
	telegramAPIBase = "https://api.telegram.org"

	defaultHTTPTimeout       = 10 * time.Second
	maxMessageRunes          = 3500
	maxResponseBytes   int64 = 8 << 10
	minRetryAfter            = time.Second
	maxRetryAfter            = time.Hour
	backoffBase              = 30 * time.Second
)

// Limits bound what a single pass may do. Interval is only used by Run.
type Limits struct {
	Interval      time.Duration
	Bounds        store.MaintenanceBounds
	Notifications int
	MaxAttempts   int
	Timeout       time.Duration
}

// DefaultLimits returns the production limits. The panel ticks every 15
// seconds (`RunOnce` may also be driven by an external ticker).
func DefaultLimits() Limits {
	return Limits{
		Interval:      15 * time.Second,
		Bounds:        store.DefaultMaintenanceBounds(),
		Notifications: 5,
		MaxAttempts:   8,
		Timeout:       defaultHTTPTimeout,
	}
}

// Stats summarizes one pass.
type Stats struct {
	Raised    int // new open alert events
	Adopted   int // conditions whose open event already existed
	Recovered int // open alerts closed by this pass
	Enqueued  int // new outbox rows
	Sent      int // notifications acknowledged by Telegram (HTTP 200 + ok=true)
	Failed    int // notification attempts that failed and were rescheduled
	Skipped   bool
	Cleanup   store.CleanupStats
}

// Service runs the maintenance pass.
type Service struct {
	st      *store.Store
	http    *http.Client
	baseURL string // telegramAPIBase in production; replaced only by tests in this package
	limits  Limits
	now     func() time.Time
	logf    func(format string, args ...any)
}

// New returns the maintenance service for a store. hc may be nil, in which case
// a client with a bounded timeout is created. The Telegram endpoint is pinned to
// api.telegram.org; only tests inside this package can override it (baseURL is
// unexported and there is no exported option).
func New(st *store.Store, hc *http.Client) *Service {
	return newService(st, hc)
}

func newService(st *store.Store, hc *http.Client) *Service {
	if hc == nil {
		hc = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Service{
		st:      st,
		http:    hc,
		baseURL: telegramAPIBase,
		limits:  DefaultLimits(),
		now:     func() time.Time { return time.Now().UTC() },
		logf:    log.Printf,
	}
}

func (s *Service) logf0(format string, args ...any) {
	if s != nil && s.logf != nil {
		s.logf(format, args...)
	}
}

// Run executes RunOnce immediately and then every Limits.Interval until ctx is
// done. Wiring: `go svc.Run(ctx)`.
func (s *Service) Run(ctx context.Context) {
	interval := s.limits.Interval
	if interval <= 0 {
		interval = DefaultLimits().Interval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.RunOnce(ctx); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			s.logf0("maintenance: pass failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOnce performs exactly one pass:
//
//   - alert reconciliation and bounded cleanup in one advisory locked
//     transaction (returns Stats.Skipped=true when another process holds it),
//   - one bounded batch of Telegram deliveries using the latest settings.
//
// A returned error is the first failure of the pass; nothing is reported as
// delivered unless Telegram answered HTTP 200 with ok=true.
func (s *Service) RunOnce(ctx context.Context) (Stats, error) {
	var stats Stats
	if s == nil || s.st == nil {
		return stats, errors.New("maintenance: store is required")
	}
	started := s.now()
	runErr := s.st.InMaintenanceTx(ctx, s.limits.Bounds, func(ctx context.Context, mtx *store.MaintenanceTx) error {
		now := s.now()
		set, err := mtx.ObserveAlerts(ctx, now)
		if err != nil {
			return err
		}
		open, err := mtx.OpenAlerts(ctx, store.MaintenanceAlertKinds)
		if err != nil {
			return err
		}
		plan := reconcile(set, open, store.MaintenanceAlertKinds)
		for _, d := range plan {
			if d.Recover {
				recoveryID, err := mtx.RecoverAlert(ctx, d.OpenEventID, d.Text)
				if err != nil {
					return err
				}
				if recoveryID == "" {
					continue // already closed by someone else
				}
				stats.Recovered++
				enqueued, err := mtx.EnqueueNotification(ctx, recoveryID, d.Observation.Kind,
					d.Observation.SubjectType, d.Observation.SubjectID, d.Text)
				if err != nil {
					return err
				}
				if enqueued {
					stats.Enqueued++
				}
				continue
			}
			eventID := d.OpenEventID
			if d.Raise || eventID == "" {
				id, raised, err := mtx.RaiseAlert(ctx, d.Observation)
				if err != nil {
					return err
				}
				switch {
				case raised:
					stats.Raised++
				case id != "":
					stats.Adopted++
				}
				eventID = id
			} else {
				stats.Adopted++
			}
			if eventID == "" {
				continue
			}
			enqueued, err := mtx.EnqueueNotification(ctx, eventID, d.Observation.Kind,
				d.Observation.SubjectType, d.Observation.SubjectID, d.Text)
			if err != nil {
				return err
			}
			if enqueued {
				stats.Enqueued++
			}
		}
		settings, err := mtx.Settings(ctx)
		if err != nil {
			return err
		}
		stats.Cleanup, err = mtx.Cleanup(ctx, settings.RetentionDays, now)
		return err
	})
	if errors.Is(runErr, store.ErrMaintenanceBusy) {
		// Another panel process is running the pass; not an error.
		stats.Skipped = true
		return stats, nil
	}
	if runErr == nil {
		sent, failed, err := s.deliver(ctx)
		stats.Sent, stats.Failed = sent, failed
		if err != nil {
			runErr = err
		}
	}

	if runErr == nil && stats.Cleanup.Total() > 0 {
		s.logf0("maintenance: %d alert(s) raised, %d recovered, %d cleaned up",
			stats.Raised, stats.Recovered, stats.Cleanup.Total())
	}
	summary := store.MaintenanceRunResult{
		StartedAt: started, FinishedAt: s.now(),
		Raised: stats.Raised, Adopted: stats.Adopted, Recovered: stats.Recovered,
		Enqueued: stats.Enqueued, Sent: stats.Sent, Failed: stats.Failed,
		Cleanup: stats.Cleanup,
	}
	if runErr != nil {
		summary.Error = runErr.Error()
	}
	if err := s.st.RecordMaintenanceRun(ctx, summary); err != nil && runErr == nil {
		runErr = err
	}
	return stats, runErr
}

// ─── reconciliation (pure) ─────────────────────────────────────────────────

// decision is the reconcile outcome for one alert key.
type decision struct {
	Observation store.AlertObservation
	Raise       bool   // open the alert: no open event for this key yet
	Recover     bool   // close OpenEventID: the condition is gone
	OpenEventID string // existing open event (adopted, or to be closed)
	Text        string // notification body (never contains secrets)
}

// reconcile compares the observed alert facts with the persisted open alerts.
// It is pure so the state machine can be unit tested without a database.
//
// Rules:
//   - one open event per (kind, subject): an observation with an existing open
//     key is adopted, so no event is created twice across ticks or restarts;
//   - an open alert of a managed kind that is no longer observed is recovered,
//     but only when that kind's scan was complete (a bounded scan cannot prove
//     absence);
//   - every decision carries the notification text; the outbox deduplicates per
//     event id, so repeated decisions never deliver twice.
func reconcile(set store.ObservationSet, open []store.OpenAlert, kinds []string) []decision {
	managed := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		managed[k] = true
	}
	openByKey := make(map[string]store.OpenAlert, len(open))
	for _, a := range open {
		if !managed[dedupeKind(a.DedupeKey)] {
			continue
		}
		openByKey[a.DedupeKey] = a
	}
	observed := make(map[string]bool, len(set.Alerts))
	out := make([]decision, 0, len(set.Alerts)+len(openByKey))
	for _, o := range set.Alerts {
		observed[o.DedupeKey] = true
		d := decision{Observation: o, Text: alertText(o)}
		if a, ok := openByKey[o.DedupeKey]; ok {
			d.OpenEventID = a.EventID
		} else {
			d.Raise = true
		}
		out = append(out, d)
	}
	for _, a := range openByKey {
		if observed[a.DedupeKey] {
			continue
		}
		kind := dedupeKind(a.DedupeKey)
		if !set.Complete(kind) {
			// The candidate scan was truncated for this kind: "not observed"
			// does not mean "healthy", so the alert must stay open.
			continue
		}
		out = append(out, decision{
			Recover:     true,
			OpenEventID: a.EventID,
			Observation: store.AlertObservation{
				Kind:        kind,
				SubjectType: a.SubjectType,
				SubjectID:   a.SubjectID,
				Message:     a.Message,
				DedupeKey:   a.DedupeKey,
			},
			Text: recoveryText(kind, a.SubjectType, a.SubjectID),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Observation.DedupeKey == out[j].Observation.DedupeKey {
			return !out[i].Recover && out[j].Recover
		}
		return out[i].Observation.DedupeKey < out[j].Observation.DedupeKey
	})
	return out
}

// dedupeKind returns the kind encoded in an alert dedupe key ("kind:subject").
func dedupeKind(key string) string {
	if i := strings.IndexByte(key, ':'); i > 0 {
		return key[:i]
	}
	return key
}

func alertText(o store.AlertObservation) string {
	subject := o.SubjectType + " " + o.SubjectID
	if name := strings.TrimSpace(o.SubjectName); name != "" {
		subject = fmt.Sprintf("%s %q (%s)", o.SubjectType, name, o.SubjectID)
	}
	return fmt.Sprintf("[proxypanel] ALERT %s: %s", o.Kind, clip(o.Message, maxMessageRunes)) + "\nsubject: " + subject
}

func recoveryText(kind, subjectType, subjectID string) string {
	return fmt.Sprintf("[proxypanel] RECOVERED %s: %s %s is healthy again", kind, subjectType, subjectID)
}

// ─── Telegram delivery ─────────────────────────────────────────────────────

// sendResult is the outcome of one HTTP attempt.
type sendResult struct {
	sent       bool
	retryAfter time.Duration
	errText    string
}

// deliver sends at most Limits.Notifications queued messages using the current
// settings. When Telegram is disabled, unconfigured or has no token, the outbox
// stays queued and nothing is sent: the decision always follows the latest
// configuration, not the one that was active when the alert was raised.
func (s *Service) deliver(ctx context.Context) (sent, failed int, err error) {
	settings, err := s.st.GetSettings(ctx)
	if err != nil {
		return 0, 0, err
	}
	chatID := strings.TrimSpace(settings.TelegramChatID)
	if !settings.TelegramEnabled || chatID == "" || !settings.TelegramTokenSet {
		return 0, 0, nil
	}
	token, err := s.st.TelegramToken(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("maintenance: telegram token unavailable: %w", redactError(err, ""))
	}
	if strings.TrimSpace(token) == "" {
		return 0, 0, nil
	}
	limit := s.limits.Notifications
	if limit <= 0 {
		limit = DefaultLimits().Notifications
	}
	due, err := s.st.ClaimNotifications(ctx, s.now(), limit)
	if err != nil {
		return 0, 0, err
	}
	maxAttempts := s.limits.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultLimits().MaxAttempts
	}
	for _, n := range due {
		if err := ctx.Err(); err != nil {
			return sent, failed, err
		}
		res := s.send(ctx, token, chatID, n.Text)
		if res.sent {
			if err := s.st.MarkNotificationSent(ctx, n.ID, s.now()); err != nil {
				return sent, failed, err
			}
			sent++
			continue
		}
		dead := n.Attempts >= maxAttempts
		delay := backoffFor(n.Attempts, res.retryAfter)
		if err := s.st.MarkNotificationFailed(ctx, n.ID, res.errText, delay, dead); err != nil {
			return sent, failed, err
		}
		failed++
		s.logf0("maintenance: telegram notification %s (event %s) failed on attempt %d: %s",
			n.ID, n.EventID, n.Attempts, res.errText)
	}
	return sent, failed, nil
}

// send performs one Telegram sendMessage call. Success requires HTTP 200 and
// {"ok":true}; every other answer (429, 5xx, network error, malformed body) is a
// failure with backoff. Neither the request URL (it embeds the bot token) nor
// the token is ever placed in an error string or a log line.
func (s *Service) send(ctx context.Context, token, chatID, text string) sendResult {
	body, err := json.Marshal(map[string]any{
		"chat_id":                  chatID,
		"text":                     redact(clip(text, maxMessageRunes), token),
		"disable_web_page_preview": true,
	})
	if err != nil {
		return sendResult{errText: "encode request: " + redact(err.Error(), token)}
	}
	timeout := s.limits.Timeout
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, s.endpoint(token), bytes.NewReader(body))
	if err != nil {
		return sendResult{errText: "build request: " + sanitizeError(err, token)}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return sendResult{errText: sanitizeError(err, token)}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	var payload telegramResponse
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	if resp.StatusCode == http.StatusOK && payload.OK {
		return sendResult{sent: true}
	}
	res := sendResult{}
	if payload.Parameters != nil && payload.Parameters.RetryAfter > 0 {
		res.retryAfter = clampDuration(time.Duration(payload.Parameters.RetryAfter)*time.Second, minRetryAfter, maxRetryAfter)
	} else if header := strings.TrimSpace(resp.Header.Get("Retry-After")); header != "" {
		if seconds, convErr := parseSeconds(header); convErr == nil {
			res.retryAfter = clampDuration(time.Duration(seconds)*time.Second, minRetryAfter, maxRetryAfter)
		}
	}
	detail := payload.Description
	if strings.TrimSpace(detail) == "" && readErr != nil {
		detail = readErr.Error()
	} else if strings.TrimSpace(detail) == "" {
		detail = strings.TrimSpace(string(raw))
	}
	res.errText = fmt.Sprintf("telegram HTTP %d: %s", resp.StatusCode, redact(clip(detail, 200), token))
	return res
}

type telegramResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// endpoint builds the fixed Bot API URL. Telegram requires the bot token in the
// path, so the URL is a secret and must never be logged or persisted.
func (s *Service) endpoint(token string) string {
	base := strings.TrimRight(s.baseURL, "/")
	if base == "" {
		base = telegramAPIBase
	}
	return base + "/bot" + token + "/sendMessage"
}

// backoffFor returns the delay before the next attempt: honour a Telegram
// retry_after hint when present, otherwise exponential backoff from 30s, capped
// at one hour.
func backoffFor(attempts int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return clampDuration(retryAfter, minRetryAfter, maxRetryAfter)
	}
	if attempts < 1 {
		attempts = 1
	}
	shift := attempts - 1
	if shift > 7 {
		shift = 7
	}
	// Clamp the lower bound at minRetryAfter, not at backoffBase: the base is the
	// *default* first delay, but a caller (or an operator via SetRetryPolicy) may
	// configure a base above maxRetryAfter. Clamping to the base would then return
	// a delay larger than the documented one-hour cap. The cap must apply to every
	// value this function produces, whatever the configured base is.
	return clampDuration(backoffBase<<uint(shift), minRetryAfter, maxRetryAfter)
}

func clampDuration(d, min, max time.Duration) time.Duration {
	if d < min {
		return min
	}
	if d > max {
		return max
	}
	return d
}

// sanitizeError renders an error without the request URL. net/http wraps every
// failure in *url.Error, whose message contains the URL and therefore the bot
// token; only the inner error is kept.
func sanitizeError(err error, token string) string {
	if err == nil {
		return ""
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return "transport: " + redact(clip(ue.Err.Error(), 200), token)
	}
	return "transport: " + redact(clip(err.Error(), 200), token)
}

// redactError scrubs a secret that is only known later in the pipeline.
func redactError(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	return errors.New(redact(err.Error(), secret))
}

func redact(text, secret string) string {
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "[redacted]")
}

// clip bounds free text by runes so a long Telegram description cannot grow the
// outbox error column or a log line without limit.
func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

func parseSeconds(s string) (int, error) { return strconv.Atoi(strings.TrimSpace(s)) }
