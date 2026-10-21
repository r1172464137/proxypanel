package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"proxypanel/internal/contracts"
)

func newClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := New(srv.URL, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.SetToken("secret-token")
	return client, srv
}

func TestSnapshotReturnsNilOn204(t *testing.T) {
	client, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PathSnapshot {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("revision"); got != "7" {
			t.Errorf("revision = %q", got)
		}
		if got := r.URL.Query().Get("control_generation"); got != "gen-1" {
			t.Errorf("control_generation = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("authorization = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	snapshot, err := client.Snapshot(context.Background(), 7, "gen-1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot != nil {
		t.Fatalf("expected nil snapshot for 204, got %+v", snapshot)
	}
}

func TestSnapshotUnwrapsDataEnvelope(t *testing.T) {
	sent := contracts.Snapshot{
		SchemaVersion:     contracts.SchemaVersion,
		ControlGeneration: "gen-1",
		Revision:          3,
		IssuedAt:          time.Now().UTC().Truncate(time.Second),
		ExpectedUserCount: 0,
		Instances:         []contracts.Instance{},
	}
	client, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": sent})
	})
	got, err := client.Snapshot(context.Background(), 0, "")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got == nil || got.Revision != 3 || got.ControlGeneration != "gen-1" || got.Instances == nil {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
}

func TestSnapshotHTTPErrorIsTyped(t *testing.T) {
	client, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "DB_DOWN", "message": "database unavailable"}})
	})
	_, err := client.Snapshot(context.Background(), 1, "")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T", err)
	}
	if !apiErr.Retryable() || apiErr.Code != "DB_DOWN" {
		t.Fatalf("unexpected APIError: %+v", apiErr)
	}
}

func TestUnauthorizedIsNotRetryable(t *testing.T) {
	client, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := client.Snapshot(context.Background(), 1, "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %v", err)
	}
	if !apiErr.Unauthorized() || apiErr.Retryable() {
		t.Fatalf("401 must be unauthorized and not retryable: %+v", apiErr)
	}
}

func TestMissingDataEnvelopeIsAnError(t *testing.T) {
	client, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	if _, err := client.Snapshot(context.Background(), 1, ""); err == nil {
		t.Fatal("a response without data must not be accepted")
	}
}

func TestAppliedRequiresAccepted(t *testing.T) {
	var received contracts.ApplyReport
	client, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PathApplied {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&received)
		_, _ = w.Write([]byte(`{"data":{"accepted":true}}`))
	})
	report := contracts.ApplyReport{ControlGeneration: "gen", Revision: 4, State: "applied", AppliedAt: time.Now().UTC()}
	if err := client.Applied(context.Background(), report); err != nil {
		t.Fatalf("Applied: %v", err)
	}
	if received.Revision != 4 {
		t.Fatalf("report not transmitted: %+v", received)
	}

	rejecting, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"accepted":false}}`))
	})
	if err := rejecting.Applied(context.Background(), report); err == nil {
		t.Fatal("accepted=false must be an error")
	}
}

func TestUsagePropagatesAck(t *testing.T) {
	client, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"batch_id":"b1","status":"duplicate"}}`))
	})
	ack, err := client.Usage(context.Background(), contracts.UsageBatch{SchemaVersion: contracts.SchemaVersion, BatchID: "b1"})
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if ack.Status != "duplicate" || ack.BatchID != "b1" {
		t.Fatalf("unexpected ack: %+v", ack)
	}
}

func TestHeartbeatSendsBearerAndPayload(t *testing.T) {
	var hb contracts.Heartbeat
	client, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("missing bearer token")
		}
		_ = json.NewDecoder(r.Body).Decode(&hb)
		_, _ = w.Write([]byte(`{"data":{"accepted":true}}`))
	})
	err := client.Heartbeat(context.Background(), contracts.Heartbeat{Version: "1.2.3", Revision: 9, QueueDepth: 2})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if hb.Version != "1.2.3" || hb.Revision != 9 {
		t.Fatalf("unexpected heartbeat payload: %+v", hb)
	}
}

func TestRegisterRejects204(t *testing.T) {
	client, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if _, err := client.Register(context.Background(), contracts.RegistrationRequest{Code: "c"}); err == nil {
		t.Fatal("204 on register must be an error")
	}
}

func TestBackoffIsBoundedAndGrows(t *testing.T) {
	b := NewBackoff(time.Second, 8*time.Second)
	first := b.Next()
	if first < 800*time.Millisecond || first > 1200*time.Millisecond {
		t.Fatalf("first backoff = %v", first)
	}
	for i := 0; i < 10; i++ {
		if d := b.Next(); d > 8*time.Second {
			t.Fatalf("backoff exceeded the cap: %v", d)
		}
	}
	b.Reset()
	if b.Attempts() != 0 {
		t.Fatalf("reset did not clear attempts")
	}
	_ = strconv.Itoa(0)
}
