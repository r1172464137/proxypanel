package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StatCounter is one counter returned by the Xray stats service.
type StatCounter struct {
	Name  string
	Value int64
}

// StartSpec describes one core process start.
type StartSpec struct {
	InstanceID string
	ConfigPath string
	LogPath    string
	Secrets    []string
}

// Proc is a managed core process.
type Proc interface {
	PID() int
	Exited() <-chan struct{}
	ExitError() error
	Stop(ctx context.Context) error
	Output() []string
}

// Runner abstracts the fixed Xray command line so lifecycle logic is testable.
type Runner interface {
	Test(ctx context.Context, configPath string) error
	Start(ctx context.Context, spec StartSpec) (Proc, error)
	StatsQuery(ctx context.Context, server string) ([]StatCounter, error)
	Version(ctx context.Context) (string, error)
}

// CLIRunner executes the fixed official Xray binary. No shell is involved and
// every argument list is fixed by the agent.
type CLIRunner struct {
	Path        string
	MaxLogBytes int64
}

// NewCLIRunner returns a runner for the configured Xray binary.
func NewCLIRunner(path string) *CLIRunner {
	return &CLIRunner{Path: path, MaxLogBytes: 8 << 20}
}

// Test runs `xray run -test -c <config>`.
func (r *CLIRunner) Test(ctx context.Context, configPath string) error {
	cmd := exec.CommandContext(ctx, r.Path, "run", "-test", "-c", configPath)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(redactSecrets(truncate(string(out), 4096), nil))
		if msg == "" {
			return fmt.Errorf("xray -test failed: %v", err)
		}
		return fmt.Errorf("xray -test failed: %v: %s", err, msg)
	}
	return nil
}

// Start launches `xray run -c <config>` with stdout/stderr captured into a
// redacted per-instance log file.
func (r *CLIRunner) Start(ctx context.Context, spec StartSpec) (Proc, error) {
	if err := os.MkdirAll(filepathDir(spec.LogPath), 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	sink, err := openLogSink(spec.LogPath, r.maxLogBytes(), spec.Secrets)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(r.Path, "run", "-c", spec.ConfigPath)
	hideWindow(cmd)
	cmd.Dir = filepathDir(spec.ConfigPath)
	cmd.Stdout = sink
	cmd.Stderr = sink
	if err := cmd.Start(); err != nil {
		sink.Close()
		return nil, fmt.Errorf("start xray: %w", err)
	}
	p := &cliProc{cmd: cmd, sink: sink, exited: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.exited)
		sink.Close()
	}()
	return p, nil
}

// StatsQuery runs the fixed, non-resetting `xray api statsquery` command.
func (r *CLIRunner) StatsQuery(ctx context.Context, server string) ([]StatCounter, error) {
	cmd := exec.CommandContext(ctx, r.Path, "api", "statsquery", "--server="+server)
	hideWindow(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("xray api statsquery failed: %v: %s", err, truncate(strings.TrimSpace(stderr.String()), 1024))
	}
	return ParseStatsQuery(stdout.Bytes())
}

// StatsQueryCommandLine documents the exact command the agent relies on.
func StatsQueryCommandLine(server string) []string {
	return []string{"api", "statsquery", "--server=" + server}
}

type statsQueryResponse struct {
	Stat []struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
	} `json:"stat"`
}

// ParseStatsQuery parses the JSON emitted by `xray api statsquery`.
func ParseStatsQuery(raw []byte) ([]StatCounter, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var resp statsQueryResponse
	if err := json.Unmarshal(trimmed, &resp); err != nil {
		return nil, fmt.Errorf("decode statsquery output: %w", err)
	}
	out := make([]StatCounter, 0, len(resp.Stat))
	for _, s := range resp.Stat {
		if s.Name == "" {
			continue
		}
		val, err := parseCounterValue(s.Value)
		if err != nil {
			return nil, fmt.Errorf("counter %q: %w", s.Name, err)
		}
		out = append(out, StatCounter{Name: s.Name, Value: val})
	}
	return out, nil
}

func parseCounterValue(raw json.RawMessage) (int64, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return 0, nil
	}
	if strings.HasPrefix(text, "\"") {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, err
		}
		text = s
	}
	if text == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(text, 64)
		if ferr != nil {
			return 0, fmt.Errorf("value %q is not numeric", text)
		}
		return int64(f), nil
	}
	return n, nil
}

// Version runs `xray version` and returns the version token.
func (r *CLIRunner) Version(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, r.Path, "version")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("xray version: %w", err)
	}
	return ParseVersion(string(out)), nil
}

// ParseVersion extracts the version from `xray version` output.
func ParseVersion(out string) string {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) < 2 {
		return strings.TrimSpace(out)
	}
	if strings.EqualFold(fields[0], "xray") {
		return fields[1]
	}
	return fields[0]
}

func (r *CLIRunner) maxLogBytes() int64 {
	if r.MaxLogBytes > 0 {
		return r.MaxLogBytes
	}
	return 8 << 20
}

type cliProc struct {
	cmd     *exec.Cmd
	sink    *logSink
	exited  chan struct{}
	waitErr error
}

func (p *cliProc) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *cliProc) Exited() <-chan struct{} { return p.exited }

func (p *cliProc) ExitError() error {
	select {
	case <-p.exited:
		return p.waitErr
	default:
		return nil
	}
}

func (p *cliProc) Output() []string {
	if p.sink == nil {
		return nil
	}
	return p.sink.Recent()
}

// Stop terminates the process: graceful where the platform supports it, then a
// hard kill. Old proxy connections die with the process, which is what the
// revocation contract requires.
func (p *cliProc) Stop(ctx context.Context) error {
	if p.cmd.Process == nil {
		return nil
	}
	select {
	case <-p.exited:
		return nil
	default:
	}
	gracefulStop(p.cmd.Process)
	wait := 5 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < wait {
			wait = remaining
		}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-p.exited:
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}
	_ = p.cmd.Process.Kill()
	select {
	case <-p.exited:
	case <-time.After(3 * time.Second):
	}
	return nil
}

// logSink redacts secrets, keeps the last lines in memory for error reports and
// appends to a size limited log file.
type logSink struct {
	mu      sync.Mutex
	file    *os.File
	max     int64
	written int64
	secrets []string
	ring    []string
	buf     []byte
}

const logRingSize = 40

func openLogSink(path string, maxBytes int64, secrets []string) (*logSink, error) {
	if st, err := os.Stat(path); err == nil && st.Size() > maxBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open xray log: %w", err)
	}
	st, _ := f.Stat()
	sink := &logSink{file: f, max: maxBytes, secrets: append([]string{}, secrets...)}
	if st != nil {
		sink.written = st.Size()
	}
	return sink, nil
}

func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		idx := bytes.IndexByte(l.buf, '\n')
		if idx < 0 {
			break
		}
		line := string(l.buf[:idx])
		l.buf = l.buf[idx+1:]
		l.emitLocked(line)
	}
	if len(l.buf) > 64<<10 {
		l.emitLocked(string(l.buf))
		l.buf = nil
	}
	return len(p), nil
}

func (l *logSink) emitLocked(line string) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}
	line = redactSecrets(line, l.secrets)
	if len(line) > 4096 {
		line = line[:4096] + "...[truncated]"
	}
	l.ring = append(l.ring, line)
	if len(l.ring) > logRingSize {
		l.ring = l.ring[len(l.ring)-logRingSize:]
	}
	if l.file == nil || l.written > l.max {
		return
	}
	entry := time.Now().UTC().Format(time.RFC3339) + " " + line + "\n"
	n, err := l.file.WriteString(entry)
	if err == nil {
		l.written += int64(n)
	}
}

func (l *logSink) Recent() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.ring))
	copy(out, l.ring)
	return out
}

func (l *logSink) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// redactSecrets removes known secret values from log lines.
func redactSecrets(line string, secrets []string) string {
	for _, s := range secrets {
		if len(s) < 6 {
			continue
		}
		line = strings.ReplaceAll(line, s, "***redacted***")
	}
	return line
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}

func filepathDir(p string) string {
	idx := strings.LastIndexAny(p, `/\`)
	if idx <= 0 {
		return "."
	}
	return p[:idx]
}
