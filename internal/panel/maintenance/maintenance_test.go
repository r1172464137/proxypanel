package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"proxypanel/internal/panel/store"
)

const testToken = "123456:TEST-TOKEN-DO-NOT-LEAK"

func observation(kind, subjectID, name string) store.AlertObservation {
	return store.AlertObservation{
		Kind:        kind,
		Severity:    "warning",
		SubjectType: "agent",
		SubjectID:   subjectID,
		SubjectName: name,
		Message:     "condition is true",
		DedupeKey:   kind + ":" + subjectID,
	}
}

func openAlert(kind, subjectID string) store.OpenAlert {
	return store.OpenAlert{
		EventID:     "evt_open_" + subjectID,
		Kind:        kind,
		Severity:    "warning",
		SubjectType: "agent",
		SubjectID:   subjectID,
		Message:     "condition is true",
		DedupeKey:   kind + ":" + subjectID,
	}
}

// A detected condition opens exactly one alert; the next tick adopts it instead
// of creating a second event.
func TestReconcileRaisesThenAdopts(t *testing.T) {
	obs := observation(store.AlertAgentOffline, "agt_1", "edge-a")
	set := store.ObservationSet{Alerts: []store.AlertObservation{obs}, Incomplete: map[string]bool{}}

	first := reconcile(set, nil, store.MaintenanceAlertKinds)
	if len(first) != 1 || !first[0].Raise || first[0].OpenEventID != "" {
		t.Fatalf("first pass must raise the alert: %+v", first)
	}
	second := reconcile(set, []store.OpenAlert{openAlert(store.AlertAgentOffline, "agt_1")}, store.MaintenanceAlertKinds)
	if len(second) != 1 {
		t.Fatalf("second pass must produce exactly one decision: %+v", second)
	}
	if second[0].Raise || second[0].Recover {
		t.Fatalf("second pass must adopt the open alert, got %+v", second[0])
	}
	if second[0].OpenEventID != "evt_open_agt_1" {
		t.Fatalf("adopted event id = %q", second[0].OpenEventID)
	}
	if second[0].Text == "" {
		t.Fatal("the notification body must be present")
	}
}

func TestReconcileRecoversOnlyWhenTheConditionIsGone(t *testing.T) {
	open := []store.OpenAlert{openAlert(store.AlertAgentOffline, "agt_1")}
	empty := store.ObservationSet{Incomplete: map[string]bool{}}

	got := reconcile(empty, open, store.MaintenanceAlertKinds)
	if len(got) != 1 || !got[0].Recover || got[0].OpenEventID != "evt_open_agt_1" {
		t.Fatalf("a healthy agent must recover its alert: %+v", got)
	}
	if !strings.Contains(got[0].Text, "RECOVERED") {
		t.Fatalf("the recovery notification must be explicit: %q", got[0].Text)
	}

	// While the condition is still observed there is no recovery.
	still := store.ObservationSet{
		Alerts:     []store.AlertObservation{observation(store.AlertAgentOffline, "agt_1", "edge-a")},
		Incomplete: map[string]bool{},
	}
	for _, d := range reconcile(still, open, store.MaintenanceAlertKinds) {
		if d.Recover {
			t.Fatalf("observed condition must not be recovered: %+v", d)
		}
	}
}

// A bounded scan cannot prove absence: alerts of a truncated kind stay open.
func TestReconcileKeepsAlertsOfTruncatedScans(t *testing.T) {
	open := []store.OpenAlert{openAlert(store.AlertMemberOverQuota, "mbr_1")}
	set := store.ObservationSet{Incomplete: map[string]bool{store.AlertMemberOverQuota: true}}
	if got := reconcile(set, open, store.MaintenanceAlertKinds); len(got) != 0 {
		t.Fatalf("a truncated scan must not recover alerts: %+v", got)
	}
	set.Incomplete = map[string]bool{}
	if got := reconcile(set, open, store.MaintenanceAlertKinds); len(got) != 1 || !got[0].Recover {
		t.Fatalf("a complete scan must recover the alert: %+v", got)
	}
}

// Events written by other components for other purposes are never touched.
func TestReconcileIgnoresForeignKinds(t *testing.T) {
	foreign := []store.OpenAlert{{
		EventID: "evt_apply", Kind: "agent_apply_failed", SubjectType: "agent",
		SubjectID: "agt_1", Message: "apply failed", DedupeKey: "apply_failed:agt_1:3",
	}}
	if got := reconcile(store.ObservationSet{Incomplete: map[string]bool{}}, foreign, store.MaintenanceAlertKinds); len(got) != 0 {
		t.Fatalf("foreign events must be left alone: %+v", got)
	}
}

// The ledger already writes "over_quota:<member>" events; the pass must adopt
// them through its dedupe key instead of raising a duplicate.
func TestReconcileAdoptsLedgerOverQuotaEvent(t *testing.T) {
	ledger := store.OpenAlert{
		EventID: "evt_ledger", Kind: "member_over_quota", Severity: "warning",
		SubjectType: "member", SubjectID: "mbr_1",
		Message: "member reached the effective quota", DedupeKey: "over_quota:mbr_1",
	}
	set := store.ObservationSet{
		Alerts:     []store.AlertObservation{observation(store.AlertMemberOverQuota, "mbr_1", "alice")},
		Incomplete: map[string]bool{},
	}
	got := reconcile(set, []store.OpenAlert{ledger}, store.MaintenanceAlertKinds)
	if len(got) != 1 || got[0].Raise || got[0].OpenEventID != "evt_ledger" {
		t.Fatalf("the ledger event must be adopted: %+v", got)
	}
	if got[0].Text == "" || strings.Contains(got[0].Text, testToken) {
		t.Fatalf("unexpected notification body: %q", got[0].Text)
	}
}

func TestDedupeKind(t *testing.T) {
	cases := map[string]string{
		"agent_offline:agt_1":   store.AlertAgentOffline,
		"over_quota:mbr_1":      store.AlertMemberOverQuota,
		"server_expiring:srv_1": store.AlertServerExpiring,
		"apply_failed:agt_1:5":  "apply_failed",
		"nocolon":               "nocolon",
	}
	for in, want := range cases {
		if got := dedupeKind(in); got != want {
			t.Fatalf("dedupeKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEndpointIsPinnedToTelegram(t *testing.T) {
	svc := New(nil, nil)
	if got := svc.endpoint("abc"); got != "https://api.telegram.org/botabc/sendMessage" {
		t.Fatalf("endpoint = %q", got)
	}
	if svc.baseURL != telegramAPIBase {
		t.Fatalf("New must pin the Telegram endpoint, got %q", svc.baseURL)
	}
}

func TestBackoffIsExponentialAndBounded(t *testing.T) {
	if got := backoffFor(1, 0); got != 30*time.Second {
		t.Fatalf("first retry = %s", got)
	}
	if got := backoffFor(3, 0); got != 2*time.Minute {
		t.Fatalf("third retry = %s", got)
	}
	if got := backoffFor(50, 0); got != maxRetryAfter {
		t.Fatalf("backoff must be capped: %s", got)
	}
	// A Telegram retry_after hint wins but is clamped.
	if got := backoffFor(1, 42*time.Second); got != 42*time.Second {
		t.Fatalf("retry_after was not honoured: %s", got)
	}
	if got := backoffFor(1, 48*time.Hour); got != maxRetryAfter {
		t.Fatalf("retry_after must be clamped: %s", got)
	}
	if got := backoffFor(1, time.Millisecond); got != minRetryAfter {
		t.Fatalf("retry_after must be clamped up: %s", got)
	}
}

func TestSanitizeErrorDropsURLAndToken(t *testing.T) {
	inner := errors.New("dial tcp 127.0.0.1:1: connectex: connection refused")
	got := sanitizeError(&url.Error{Op: "Post", URL: telegramAPIBase + "/bot" + testToken + "/sendMessage", Err: inner}, testToken)
	if strings.Contains(got, testToken) || strings.Contains(got, "api.telegram.org") {
		t.Fatalf("sanitized error leaks the endpoint: %q", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Fatalf("sanitized error dropped the useful cause: %q", got)
	}
}

func TestSendRequiresHTTP200AndOKTrue(t *testing.T) {
	type tc struct {
		name       string
		status     int
		body       string
		wantSent   bool
		wantRetry  time.Duration
		wantErrHas string
	}
	cases := []tc{
		{name: "ok", status: 200, body: `{"ok":true,"result":{"message_id":1}}`, wantSent: true},
		{name: "ok-false", status: 200, body: `{"ok":false,"description":"chat not found"}`, wantErrHas: "chat not found"},
		{name: "rate-limited", status: 429, body: `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":42}}`, wantRetry: 42 * time.Second, wantErrHas: "HTTP 429"},
		{name: "server-error", status: 500, body: `{"ok":false,"description":"internal"}`, wantErrHas: "HTTP 500"},
		{name: "garbage", status: 502, body: `upstream unavailable`, wantErrHas: "HTTP 502"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			svc := newService(nil, srv.Client())
			svc.baseURL = srv.URL // test only: production is pinned to api.telegram.org

			res := svc.send(context.Background(), testToken, "42", "[proxypanel] ALERT test")
			if res.sent != c.wantSent {
				t.Fatalf("sent = %v, want %v (err %q)", res.sent, c.wantSent, res.errText)
			}
			if c.wantRetry != 0 && res.retryAfter != c.wantRetry {
				t.Fatalf("retryAfter = %s, want %s", res.retryAfter, c.wantRetry)
			}
			if c.wantErrHas != "" && !strings.Contains(res.errText, c.wantErrHas) {
				t.Fatalf("errText = %q, want it to contain %q", res.errText, c.wantErrHas)
			}
			if strings.Contains(res.errText, testToken) {
				t.Fatalf("errText leaks the bot token: %q", res.errText)
			}
			if strings.Contains(res.errText, srv.URL) {
				t.Fatalf("errText leaks the request URL: %q", res.errText)
			}
			if !strings.HasSuffix(gotPath, "/bot"+testToken+"/sendMessage") {
				t.Fatalf("unexpected request path %q", gotPath)
			}
		})
	}
}

func TestSendBodyCarriesChatAndText(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	svc := newService(nil, srv.Client())
	svc.baseURL = srv.URL
	if res := svc.send(context.Background(), testToken, "-100123", "[proxypanel] ALERT agent_offline"); !res.sent {
		t.Fatalf("send failed: %+v", res)
	}
	if body["chat_id"] != "-100123" {
		t.Fatalf("chat_id missing from the request body: %+v", body)
	}
	if text, _ := body["text"].(string); !strings.Contains(text, "agent_offline") {
		t.Fatalf("text missing from the request body: %+v", body)
	}
	if _, ok := body["parse_mode"]; ok {
		t.Fatal("parse_mode must not be set: alert text is sent verbatim")
	}
}

// A network failure must be recorded as a sanitized transport error, never as a
// URL that embeds the token.
func TestSendRecordsNetworkFailureWithoutLeakingTheURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // connection refused from now on
	svc := newService(nil, nil)
	svc.baseURL = base
	svc.limits.Timeout = 2 * time.Second
	res := svc.send(context.Background(), testToken, "42", "text")
	if res.sent {
		t.Fatal("a transport failure must not be reported as delivered")
	}
	if strings.Contains(res.errText, testToken) || strings.Contains(res.errText, base) {
		t.Fatalf("errText leaks the request URL or token: %q", res.errText)
	}
	if res.errText == "" {
		t.Fatal("the failure must be recorded")
	}
}

func TestSendIsBoundedByTheTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer func() { close(release); srv.Close() }()
	svc := newService(nil, srv.Client())
	svc.baseURL = srv.URL
	svc.limits.Timeout = 100 * time.Millisecond
	start := time.Now()
	res := svc.send(context.Background(), testToken, "42", "text")
	if res.sent {
		t.Fatal("a timed out request must not be reported as delivered")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("send was not bounded: %s", elapsed)
	}
	if strings.Contains(res.errText, testToken) {
		t.Fatalf("errText leaks the token: %q", res.errText)
	}
}

// Telegram accepts at most 4096 characters; the body is clipped by runes.
func TestClipBoundsMessagesWithoutBreakingRunes(t *testing.T) {
	long := strings.Repeat("告", maxMessageRunes+50)
	got := clip(long, maxMessageRunes)
	if r := []rune(strings.TrimSuffix(got, "...")); len(r) != maxMessageRunes {
		t.Fatalf("clip kept %d runes", len(r))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatal("clipped text must be marked")
	}
	if got := clip("  short  ", 10); got != "short" {
		t.Fatalf("clip trimmed = %q", got)
	}
}

func TestRedactRemovesTheToken(t *testing.T) {
	if got := redact("url https://api.telegram.org/bot"+testToken+"/sendMessage", testToken); strings.Contains(got, testToken) {
		t.Fatalf("redact left the token in place: %q", got)
	}
	if got := redact("plain", ""); got != "plain" {
		t.Fatalf("redact without a secret changed the text: %q", got)
	}
}

func TestRunOnceWithoutStoreFails(t *testing.T) {
	svc := New(nil, nil)
	if _, err := svc.RunOnce(context.Background()); err == nil {
		t.Fatal("a service without a store must fail instead of silently doing nothing")
	}
}
