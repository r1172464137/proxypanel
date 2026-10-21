package xray

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseStatsQuery(t *testing.T) {
	raw := []byte(`{"stat":[{"name":"user>>>a@b.test>>>traffic>>>uplink","value":"123"},{"name":"user>>>a@b.test>>>traffic>>>downlink","value":45},{"name":"","value":"1"}]}`)
	counters, err := ParseStatsQuery(raw)
	if err != nil {
		t.Fatalf("ParseStatsQuery: %v", err)
	}
	if len(counters) != 2 {
		t.Fatalf("expected 2 counters, got %d", len(counters))
	}
	if counters[0].Name != "user>>>a@b.test>>>traffic>>>uplink" || counters[0].Value != 123 {
		t.Fatalf("unexpected counter: %+v", counters[0])
	}
	if counters[1].Value != 45 {
		t.Fatalf("unexpected numeric counter: %+v", counters[1])
	}
	if empty, err := ParseStatsQuery(nil); err != nil || empty != nil {
		t.Fatalf("empty output must be accepted: %v %v", empty, err)
	}
	if _, err := ParseStatsQuery([]byte("not json")); err == nil {
		t.Fatal("invalid output must be an error")
	}
}

func TestParseVersion(t *testing.T) {
	out := "Xray 26.3.27 (Xray, Penetrates Everything.) d2758a0 (go1.26.1 windows/amd64)"
	if got := ParseVersion(out); got != "26.3.27" {
		t.Fatalf("ParseVersion = %q", got)
	}
	if got := ParseVersion("26.3.27"); got != "26.3.27" {
		t.Fatalf("ParseVersion = %q", got)
	}
}

func TestStatsQueryCommandLineIsFixed(t *testing.T) {
	got := strings.Join(StatsQueryCommandLine("127.0.0.1:12345"), " ")
	if got != "api statsquery --server=127.0.0.1:12345" {
		t.Fatalf("unexpected command line: %q", got)
	}
}

func TestLogSinkRedactsSecretsAndRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "xray.log")
	sink, err := openLogSink(path, 1<<20, []string{"super-secret-token-value"})
	if err != nil {
		t.Fatalf("openLogSink: %v", err)
	}
	if _, err := sink.Write([]byte("starting core with token=super-secret-token-value\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := sink.Write([]byte("second line without secrets\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	content := string(raw)
	if strings.Contains(content, "super-secret-token-value") {
		t.Fatalf("secret leaked into the core log: %s", content)
	}
	if !strings.Contains(content, "***redacted***") {
		t.Fatalf("secret was not redacted: %s", content)
	}
	if !strings.Contains(content, "second line without secrets") {
		t.Fatalf("log line missing: %s", content)
	}
	if recent := sink.Recent(); len(recent) != 2 {
		t.Fatalf("ring buffer = %v", recent)
	}
}

func TestWriteFileAtomicReplacesContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := writeFileAtomic(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := writeFileAtomic(path, []byte("second"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "second" {
		t.Fatalf("content = %q", raw)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}
