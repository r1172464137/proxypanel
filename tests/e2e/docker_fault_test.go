package e2e

// Bounded controlled-fault acceptance for goal criteria 5 and 6
// (A08/A10/A11: ledger, restart and offline-expiry reliability plus controlled
// fault behaviour). Goal: .pi/goal/完成计划剩余任务与-docker-ssh-隧道验收-20261021-1045.md
//
// Really executed here:
//   - panel, PostgreSQL, Redis, both Xray Agents and the target fixture run as
//     the owned containers ppaccept-20261021-* and are reached through the SSH
//     tunnel; traffic, counters and the usage ledger are real;
//   - the Agent SQLite outbox and the panel's agent rows are read from the real
//     state files and the real acceptance database.
//
// Faults injected on purpose (bounded to the owned containers, each one named
// as an injection in the log; nothing here claims a natural outage):
//   - the owned PostgreSQL container is stopped -> a real upload must not be
//     acknowledged and must stay in the Agent outbox;
//   - the owned Redis container is stopped, removed and recreated -> the
//     PostgreSQL ledger and the sessions must survive and authentication must
//     stay enforced;
//   - a 16 MiB tmpfs state filesystem (seeded with a copy of the agent1 state,
//     staged only in the owned faultstate directory) is filled until ENOSPC ->
//     the agent must stop the affected proxy, then coordinate it back to
//     running after the capacity is restored.
//
// ACK loss and duplicate delivery are covered end-to-end by
// TestDockerACKLossAcceptance (docker_ack_test.go): a real committed usage
// acknowledgement is dropped on the wire, the real agent reporting/state code
// re-sends the same batch id and PostgreSQL has to hold exactly one credit.
// The end-to-end run below still proves the durable half of the outage cases:
// the agent retains the unacknowledged batches and replays them exactly once
// (the outbox drains, the ledger grows by at least the retained bytes and then
// stays stable).
//
// This case shares the Docker acceptance fixture with TestDockerAcceptance, so
// it must never run in parallel.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"proxypanel/internal/contracts"

	_ "modernc.org/sqlite"
)

// faultScript is deployed next to control.sh inside the owned root by the
// operator that holds the SSH tunnel. Only the fixed action strings below are
// ever passed; the script re-verifies the project root marker and the ownership
// label of every container it touches.
const faultScript = "faults.sh"

func faultTail(raw string) string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) > 2 {
		lines = lines[len(lines)-2:]
	}
	out := strings.TrimSpace(strings.Join(lines, " | "))
	if len(out) > 240 {
		out = out[:240]
	}
	return out
}

// runOwned executes one fixed action of one owned remote script.
func (d *dockerTest) runOwned(script, action string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := append(d.sshArgs(), "root@192.168.5.203", "sh "+dockerRemote+"/"+script+" "+action)
	out, err := exec.CommandContext(ctx, "ssh", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %q failed: %w (%s)", script, action, err, faultTail(string(out)))
	}
	return string(out), nil
}

func (d *dockerTest) fault(action string) string {
	d.t.Helper()
	out, err := d.runOwned(faultScript, action, 180*time.Second)
	if err != nil {
		d.t.Fatal(err)
	}
	return out
}

func (d *dockerTest) tryFault(action string) (string, error) {
	return d.runOwned(faultScript, action, 180*time.Second)
}

func (d *dockerTest) tryRemote(action string) (string, error) {
	return d.runOwned("control.sh", action, 120*time.Second)
}

// faultInfo reads the capacity evidence, retrying the short window in which a
// freshly created container has not reached its running state yet. A container
// that really stopped still fails with the docker exec error.
func (d *dockerTest) faultInfo() string {
	d.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for {
		out, err := d.tryFault("capacity-info")
		if err == nil {
			return out
		}
		lastErr = err
		if time.Now().After(deadline) {
			d.t.Fatalf("the capacity container did not report its limits: %v", lastErr)
		}
		time.Sleep(2 * time.Second)
	}
}

// faultPoll is waitFor with a slower cadence for checks that cross SSH.
func faultPoll(t *testing.T, budget, interval time.Duration, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if check() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(interval)
	}
}

// agentQueue is the observable part of the owned agent outbox. The agent
// database also stores the agent token in its meta table: this helper only ever
// queries batches and samples, never meta, and the decoded copy lives only in
// the test's temporary directory (removed with the test) and is never logged.
type agentQueue struct {
	PendingBatches int64
	PendingSamples int64
	PendingBytes   int64
	Attempts       int64
	LastError      string
}

func (d *dockerTest) queue(container string) agentQueue {
	d.t.Helper()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		q, err := d.readQueue(container)
		if err == nil {
			return q
		}
		lastErr = err
	}
	d.t.Fatalf("cannot read the %s outbox: %v", container, lastErr)
	return agentQueue{}
}

func (d *dockerTest) readQueue(container string) (agentQueue, error) {
	switch container {
	case "agent1", "agent2", "agent1cap":
	default:
		return agentQueue{}, fmt.Errorf("refusing a state read of unowned container %q", container)
	}
	out, err := d.tryFault("state-dump " + container)
	if err != nil {
		return agentQueue{}, err
	}
	files, err := decodeStateDump(out)
	if err != nil {
		return agentQueue{}, err
	}
	dir := d.t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			return agentQueue{}, err
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "agent.db"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return agentQueue{}, err
	}
	defer db.Close()
	var q agentQueue
	if err := db.QueryRow(`select count(*), coalesce(sum(attempts),0), coalesce(max(last_error),'') from batches where state='pending'`).
		Scan(&q.PendingBatches, &q.Attempts, &q.LastError); err != nil {
		return agentQueue{}, fmt.Errorf("read pending batches: %w", err)
	}
	if err := db.QueryRow(`select count(*), coalesce(sum(upload+download),0) from samples s
		where exists (select 1 from batches b where b.batch_id=s.batch_id and b.state='pending')`).
		Scan(&q.PendingSamples, &q.PendingBytes); err != nil {
		return agentQueue{}, fmt.Errorf("read pending samples: %w", err)
	}
	return q, nil
}

// decodeStateDump parses the framed base64 stream printed by the fault script:
//
//	FILE <name> <size>
//	<base64>
//	END <name>
func decodeStateDump(raw string) (map[string][]byte, error) {
	files := map[string][]byte{}
	var (
		name string
		size int
		buf  strings.Builder
	)
	flush := func() error {
		body, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(buf.String()), ""))
		if err != nil {
			return fmt.Errorf("decode %s: %w", name, err)
		}
		if len(body) != size {
			return fmt.Errorf("decode %s: got %d bytes, the script reported %d", name, len(body), size)
		}
		files[name] = body
		buf.Reset()
		return nil
	}
	for _, raw := range strings.Split(raw, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case name == "" && strings.HasPrefix(line, "FILE "):
			fields := strings.Fields(strings.TrimPrefix(line, "FILE "))
			if len(fields) != 2 {
				return nil, fmt.Errorf("unexpected state dump header %q", line)
			}
			n, err := strconv.Atoi(fields[1])
			if err != nil || n < 0 {
				return nil, fmt.Errorf("unexpected state dump size %q", fields[1])
			}
			name, size = fields[0], n
		case name != "" && line == "END "+name:
			if err := flush(); err != nil {
				return nil, err
			}
			name = ""
		case name != "":
			buf.WriteString(line)
		}
	}
	if name != "" {
		return nil, fmt.Errorf("state dump of %s is truncated", name)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("state dump carried no file")
	}
	return files, nil
}

type panelReply struct {
	status int
	code   string
	body   map[string]any
}

// reply issues one panel request. Authenticated requests reuse the administrator
// session, anonymous ones use a client without cookies. Response bodies are
// parsed but never logged, so neither a session cookie nor a CSRF token can
// leak into the report.
func (d *dockerTest) reply(method, path string, payload any, authenticated bool) panelReply {
	d.t.Helper()
	var reader io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			d.t.Fatalf("encode %s %s: %v", method, path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, d.api.base+path, reader)
	if err != nil {
		d.t.Fatalf("build %s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: d.transport, Timeout: 20 * time.Second}
	if authenticated {
		client = d.api.client
		req.Header.Set("X-CSRF-Token", d.api.csrf)
	}
	res, err := client.Do(req)
	if err != nil {
		d.t.Fatalf("%s %s failed: %v", method, path, err)
	}
	defer res.Body.Close()
	out := panelReply{status: res.StatusCode}
	var body map[string]any
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body) == nil {
		out.body = body
		out.code, _ = body["code"].(string)
	}
	return out
}

func (d *dockerTest) readyz() (status int, state, postgres, redis string) {
	d.t.Helper()
	reply := d.reply("GET", "/readyz", nil, false)
	data, _ := reply.body["data"].(map[string]any)
	state, _ = data["status"].(string)
	postgres, _ = data["postgres"].(string)
	redis, _ = data["redis"].(string)
	return reply.status, state, postgres, redis
}

// count runs one acceptance query. SQL errors are never swallowed: a failed
// query fails the case with the driver error.
func (d *dockerTest) count(query string, args ...any) int64 {
	d.t.Helper()
	var n int64
	if err := d.db.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		d.t.Fatalf("acceptance query failed: %v", err)
	}
	return n
}

// agentRuntime reads what the panel itself stores for the owned agent row:
// the running instance count the agent reported and its instance list. -1 marks
// a missing metric so an absent heartbeat can never look like "0 running".
func (d *dockerTest) agentRuntime(serverID string) (running int, instances string) {
	d.t.Helper()
	err := d.db.QueryRow(context.Background(),
		`select coalesce((metrics->>'instances_running')::int, -1), coalesce(instances::text, '') from agents where server_id=$1 and archived=false`,
		serverID).Scan(&running, &instances)
	if err != nil {
		d.t.Fatalf("agent runtime query failed: %v", err)
	}
	return running, instances
}

// agentObservation is the panel's stored view of one agent row. The panel only
// refreshes the instance list and the running count from a heartbeat
// (store.RecordHeartbeat), so Heartbeat is the freshness watermark: an
// observation is evidence only once its heartbeat is newer than the event it is
// supposed to prove.
type agentObservation struct {
	Running   int
	Instances []contracts.InstanceResult
	Heartbeat time.Time
}

// recoveredAfter reports whether this observation proves that the instance of
// nodeID runs again after stop, the heartbeat at which the stop was observed. A
// stale heartbeat is never evidence, even when it still carries a running count.
func (o agentObservation) recoveredAfter(nodeID string, stop time.Time) (state string, ok bool) {
	if !o.Heartbeat.After(stop) || o.Running < 1 {
		return "", false
	}
	state, known := o.instance(nodeID)
	if !known || (state != "applied" && state != "rollback") {
		return "", false
	}
	return state, true
}

// instance returns the panel's reported state of one instance id.
func (o agentObservation) instance(id string) (state string, known bool) {
	for _, inst := range o.Instances {
		if inst.InstanceID == id {
			return inst.State, true
		}
	}
	return "", false
}

// agentObservation reads the owned agent row in one query so the heartbeat
// watermark and the values it carried can never come from different rows.
func (d *dockerTest) agentObservation(serverID string) agentObservation {
	d.t.Helper()
	var (
		running   int
		raw       []byte
		heartbeat sql.NullTime
	)
	if err := d.db.QueryRow(context.Background(),
		`select coalesce((metrics->>'instances_running')::int, -1), coalesce(instances, '[]'::jsonb), last_heartbeat_at
			from agents where server_id=$1 and archived=false`, serverID).Scan(&running, &raw, &heartbeat); err != nil {
		d.t.Fatalf("agent observation query failed: %v", err)
	}
	observation := agentObservation{Running: running}
	if err := json.Unmarshal(raw, &observation.Instances); err != nil {
		d.t.Fatalf("decode the agent instance list: %v", err)
	}
	if heartbeat.Valid {
		observation.Heartbeat = heartbeat.Time.UTC()
	}
	return observation
}

// agent1Config reorders the exported client configuration so its first outbound
// is the owned node served by agent1 (port 20101). The fault phases then assert
// the agent they actually faulted instead of whichever node came first.
func agent1Config(t *testing.T, cfg map[string]any) map[string]any {
	t.Helper()
	outbounds, ok := cfg["outbounds"].([]any)
	if !ok || len(outbounds) == 0 {
		t.Fatal("exported client configuration has no outbounds")
	}
	for index, item := range outbounds {
		out, ok := item.(map[string]any)
		if !ok {
			continue
		}
		settings, _ := out["settings"].(map[string]any)
		vnext, _ := settings["vnext"].([]any)
		if len(vnext) == 0 {
			continue
		}
		server, _ := vnext[0].(map[string]any)
		if port, _ := server["port"].(float64); int(port) != 20101 {
			continue
		}
		ordered := []any{out}
		for i, other := range outbounds {
			if i != index {
				ordered = append(ordered, other)
			}
		}
		cfg["outbounds"] = ordered
		return cfg
	}
	t.Fatal("exported client configuration does not contain the owned agent1 node (127.0.0.1:20101)")
	return nil
}

// capacityLimits is the runtime evidence that the fault filesystem is bounded
// and that the capacity copy authenticates the panel like the real agent1.
type capacityLimits struct {
	status      string
	privileged  string
	network     string
	memory      int64
	cpus        int64
	pids        int64
	securityopt string
	capdrop     string
	tmpfs       string
	binds       string
	ca          string
	uid         string
	owner       string
	files       int64
	totalKiB    int64
}

func parseCapacityInfo(t *testing.T, info string) capacityLimits {
	t.Helper()
	var out capacityLimits
	for _, raw := range strings.Split(info, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "container "):
			for _, field := range strings.Fields(strings.TrimPrefix(line, "container ")) {
				key, value, ok := strings.Cut(field, "=")
				if !ok {
					continue
				}
				switch key {
				case "status":
					out.status = value
				case "privileged":
					out.privileged = value
				case "network":
					out.network = value
				case "memory":
					n, err := strconv.ParseInt(value, 10, 64)
					if err != nil {
						t.Fatalf("capacity info memory %q is not a number: %v", value, err)
					}
					out.memory = n
				case "cpus":
					n, err := strconv.ParseInt(value, 10, 64)
					if err != nil {
						t.Fatalf("capacity info cpus %q is not a number: %v", value, err)
					}
					out.cpus = n
				case "pids":
					if value == "null" {
						// An unset limit is reported as null: never treat it as present.
						break
					}
					n, err := strconv.ParseInt(value, 10, 64)
					if err != nil {
						t.Fatalf("capacity info pids %q is not a number: %v", value, err)
					}
					out.pids = n
				}
			}
		case strings.HasPrefix(line, "securityopt "):
			out.securityopt = strings.TrimPrefix(line, "securityopt ")
		case strings.HasPrefix(line, "capdrop "):
			out.capdrop = strings.TrimPrefix(line, "capdrop ")
		case strings.HasPrefix(line, "tmpfs "):
			out.tmpfs = strings.TrimPrefix(line, "tmpfs ")
		case strings.HasPrefix(line, "binds "):
			out.binds = strings.TrimPrefix(line, "binds ")
		case strings.HasPrefix(line, "uid="):
			out.uid = strings.TrimPrefix(line, "uid=")
		case strings.HasPrefix(line, "ca="):
			out.ca = strings.TrimPrefix(line, "ca=")
		case strings.HasPrefix(line, "state owner="):
			out.owner = strings.TrimPrefix(line, "state owner=")
		case strings.HasPrefix(line, "state files="):
			n, err := strconv.ParseInt(strings.TrimPrefix(line, "state files="), 10, 64)
			if err != nil {
				t.Fatalf("capacity info file count %q is not a number: %v", line, err)
			}
			out.files = n
		case strings.HasPrefix(line, "state df: "):
			fields := strings.Fields(strings.TrimPrefix(line, "state df: "))
			if len(fields) < 6 {
				t.Fatalf("capacity info filesystem line %q is incomplete", line)
			}
			n, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				t.Fatalf("capacity info filesystem size %q is not a number: %v", fields[1], err)
			}
			out.totalKiB = n
		}
	}
	if out.status == "" || out.tmpfs == "" || out.binds == "" || out.totalKiB == 0 ||
		out.uid == "" || out.ca == "" || out.owner == "" {
		t.Fatalf("capacity info did not report the bounded limits:\n%s", info)
	}
	return out
}

// assertBoundedCapacity verifies the safety boundary of the injected capacity
// fault from the running container itself.
func assertBoundedCapacity(t *testing.T, info string) capacityLimits {
	t.Helper()
	limits := parseCapacityInfo(t, info)
	switch {
	case limits.privileged != "false":
		t.Fatalf("capacity container privileged=%q, the acceptance boundary forbids it", limits.privileged)
	case limits.network == "host" || !strings.HasSuffix(limits.network, "-net"):
		t.Fatalf("capacity container network=%q, only the owned bridge network is allowed", limits.network)
	case limits.memory <= 0 || limits.memory > 256<<20:
		t.Fatalf("capacity container memory=%d, expected a bounded non-zero limit", limits.memory)
	case limits.cpus <= 0 || limits.cpus > 1_000_000_000:
		t.Fatalf("capacity container cpus=%d NanoCPUs, expected a bounded non-zero CPU limit", limits.cpus)
	case limits.pids <= 0 || limits.pids > 256:
		t.Fatalf("capacity container pids=%d, expected a bounded non-zero process limit", limits.pids)
	case !strings.Contains(limits.securityopt, "no-new-privileges"):
		t.Fatalf("capacity container security options %s do not include no-new-privileges", limits.securityopt)
	case !strings.Contains(limits.capdrop, "ALL"):
		t.Fatalf("capacity container does not drop all capabilities: %s", limits.capdrop)
	case limits.ca != "present":
		t.Fatalf("the capacity container cannot read the SSL_CERT_FILE target /run/fixture/ca.pem: %s", limits.ca)
	case limits.owner != limits.uid:
		t.Fatalf("the staged state is owned by uid %s but the agent runs as uid %s", limits.owner, limits.uid)
	case !strings.Contains(limits.tmpfs, "size=16m"):
		t.Fatalf("capacity container tmpfs=%s, expected a 16 MiB state filesystem", limits.tmpfs)
	case limits.totalKiB > 16*1024:
		t.Fatalf("state filesystem reports %d KiB, above the 16 MiB bound", limits.totalKiB)
	case strings.Contains(limits.binds, "docker.sock"):
		t.Fatalf("capacity container mounts the Docker socket: %s", limits.binds)
	case !strings.Contains(limits.binds, "faultstate/agent1"):
		t.Fatalf("capacity container does not use the dedicated state copy: %s", limits.binds)
	case !strings.Contains(limits.binds, "secrets/ca.pem"):
		t.Fatalf("capacity container does not mount the owned test CA: %s", limits.binds)
	case limits.files < 1:
		t.Fatalf("capacity state filesystem is empty, the agent state copy was not applied")
	}
	return limits
}

// TestDockerFaultAcceptance covers the controlled fault behaviour of the owned
// acceptance stack. It is not parallel: it shares the Docker fixture with
// TestDockerAcceptance.
func TestDockerFaultAcceptance(t *testing.T) {
	d := dockerSetup(t)
	// The shared d.memberPatch helper already unwraps the {member,access}
	// member detail response when it reads the optimistic version.
	d.remote("agent1")
	d.remote("agent2")
	d.remote("start agent1")
	d.remote("start agent2")
	d.memberPatch(map[string]any{"active": true, "proxy_enabled": true, "expires_at": nil})
	d.applied()
	client, tr, _ := dockerProxy(t, agent1Config(t, d.export("xray")))

	t.Log("real run: owned panel, PostgreSQL, Redis, two Agents and Xray traffic")
	ledgerStart := d.ledger()
	if !dockerFetch(client) {
		t.Fatal("baseline proxy traffic through the owned agent1 failed")
	}
	waitFor(t, 30*time.Second, "baseline usage ledger", func() bool { return d.ledger() > ledgerStart })
	if running, instances := d.agentRuntime(d.fixture.Servers[0]); running < 1 {
		t.Fatalf("agent1 does not report a running instance before the faults (running=%d instances=%s)", running, instances)
	}

	faultPostgresPhase(t, d, client, tr)
	faultRedisPhase(t, d, client, tr)
	faultCapacityPhase(t, d, client, tr)

	t.Log(d.remote("inspect"))
	t.Log("bounded fault acceptance finished: injected PostgreSQL outage, injected Redis outage and recreate, injected 16 MiB ENOSPC; no ACK was faked and no host resource was exhausted")
}

// faultPostgresPhase stops the owned PostgreSQL container: the panel must not
// acknowledge a real upload while it cannot commit, and the retained batches
// must be credited exactly once after the recovery.
func faultPostgresPhase(t *testing.T, d *dockerTest, client *http.Client, tr *http.Transport) {
	t.Helper()
	t.Log("fault injection: stopping the owned acceptance PostgreSQL container")
	queuedBefore := d.queue("agent1")
	if queuedBefore.PendingBatches != 0 {
		t.Logf("baseline: agent1 already retains %d batch(es) before the injected outage", queuedBefore.PendingBatches)
	}
	ledgerBefore := d.ledger()
	batchesBefore := d.count(`select count(*) from usage_batches`)
	restored := false
	defer func() {
		if restored {
			return
		}
		if _, err := d.tryRemote("start postgres"); err != nil {
			t.Errorf("restore PostgreSQL: %v", err)
		}
	}()

	d.remote("stop postgres")
	waitFor(t, 60*time.Second, "panel readiness reports the PostgreSQL outage", func() bool {
		status, _, _, _ := d.readyz()
		return status == http.StatusServiceUnavailable
	})
	// The API must fail rather than acknowledge: no session, agent or usage
	// write can be committed while PostgreSQL is down.
	if reply := d.reply("GET", "/api/v1/admin/overview", nil, true); reply.status < 500 {
		t.Fatalf("authenticated admin read answered %d (%s) while PostgreSQL was stopped", reply.status, reply.code)
	}
	t.Log("panel refused the API during the injected outage (readyz=503, authenticated admin read >=500)")

	for i := 0; i < 3; i++ {
		if !dockerFetch(client) {
			t.Fatal("proxy traffic failed while only PostgreSQL was stopped")
		}
	}
	var queued agentQueue
	faultPoll(t, 120*time.Second, 2*time.Second, "the agent retains usage the panel never acknowledged", func() bool {
		queued = d.queue("agent1")
		return queued.PendingBatches > 0 && queued.Attempts > 0 && queued.LastError != "" && queued.PendingSamples > 0
	})
	t.Logf("injected outage: agent1 outbox keeps %d batch(es) / %d sample(s) / %d byte(s) with %d failed upload attempt(s), last error %q",
		queued.PendingBatches, queued.PendingSamples, queued.PendingBytes, queued.Attempts, queued.LastError)

	d.remote("start postgres")
	waitFor(t, 120*time.Second, "panel readiness restores PostgreSQL", func() bool {
		status, _, postgres, _ := d.readyz()
		return status == http.StatusOK && postgres == "ok"
	})
	faultPoll(t, 300*time.Second, 2*time.Second, "the retained outboxes are accepted after the recovery", func() bool {
		return d.queue("agent1").PendingBatches == 0 && d.queue("agent2").PendingBatches == 0
	})
	ledgerAfter := d.ledger()
	if ledgerAfter-ledgerBefore < queued.PendingBytes {
		t.Fatalf("ledger grew by %d bytes after the recovery, less than the %d retained bytes: unacknowledged usage was lost",
			ledgerAfter-ledgerBefore, queued.PendingBytes)
	}
	batchesAfter := d.count(`select count(*) from usage_batches`)
	if batchesAfter-batchesBefore < queued.PendingBatches {
		t.Fatalf("%d usage batches reached the panel, fewer than the %d the agent retained: a batch was dropped",
			batchesAfter-batchesBefore, queued.PendingBatches)
	}
	// No replay double count: with no traffic the ledger must stay put.
	time.Sleep(2 * time.Second)
	stable := d.ledger()
	time.Sleep(4 * time.Second)
	if again := d.ledger(); again != stable {
		t.Fatalf("ledger kept growing from %d to %d without traffic: a replayed batch was credited twice", stable, again)
	}
	tr.CloseIdleConnections()
	if !dockerFetch(client) {
		t.Fatal("proxy traffic did not resume after the PostgreSQL recovery")
	}
	d.applied()
	restored = true
	t.Logf("injected outage recovered: %d retained byte(s) credited once, outbox empty, ledger %d -> %d", queued.PendingBytes, ledgerBefore, ledgerAfter)
}

// faultRedisPhase stops, removes and recreates the owned Redis container. The
// ledger and the sessions live in PostgreSQL, so neither may be lost and
// authentication must stay enforced (Redis is only the login rate limiter).
func faultRedisPhase(t *testing.T, d *dockerTest, client *http.Client, tr *http.Transport) {
	t.Helper()
	if status, state, _, redis := d.readyz(); status != http.StatusOK || redis != "ok" {
		t.Fatalf("acceptance panel does not report a healthy Redis baseline (status=%d state=%q redis=%q): REDIS_URL must be configured for this criterion",
			status, state, redis)
	}
	ledgerBefore := d.ledger()
	sessionsBefore := d.count(`select count(*) from sessions where revoked_at is null`)
	restored := false
	defer func() {
		if restored {
			return
		}
		if _, err := d.tryRemote("start redis"); err != nil {
			if _, uerr := d.tryRemote("up"); uerr != nil {
				t.Errorf("restore Redis: start failed (%v) and the stack could not recreate it (%v)", err, uerr)
			}
		}
	}()

	t.Log("fault injection: stopping the owned acceptance Redis container")
	d.remote("stop redis")
	waitFor(t, 60*time.Second, "panel readiness reports Redis degraded with PostgreSQL healthy", func() bool {
		status, state, postgres, redis := d.readyz()
		return status == http.StatusOK && state == "degraded" && postgres == "ok" && redis == "unavailable"
	})
	if reply := d.reply("GET", "/api/v1/admin/overview", nil, true); reply.status != http.StatusOK {
		t.Fatalf("the existing administrator session was rejected (%d %s) while Redis was down", reply.status, reply.code)
	}
	if reply := d.reply("GET", "/api/v1/admin/overview", nil, false); reply.status != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated admin read returned %d while Redis was down: authentication was bypassed", reply.status)
	}
	if reply := d.reply("POST", "/api/v1/auth/login",
		map[string]any{"username": "docker-alice", "password": "not-the-acceptance-password"}, false); reply.status != http.StatusUnauthorized {
		t.Fatalf("a wrong password returned %d while Redis was down: authentication was bypassed", reply.status)
	}
	// The limiter must degrade to its bounded local counter, never to "allow".
	probe := map[string]any{"username": "acceptance-limiter-probe", "password": "x"}
	for attempt := 1; attempt <= 11; attempt++ {
		reply := d.reply("POST", "/api/v1/auth/login", probe, false)
		switch {
		case attempt <= 10 && reply.status != http.StatusUnauthorized:
			t.Fatalf("login attempt %d with an unknown user returned %d, want 401", attempt, reply.status)
		case attempt == 11 && reply.status != http.StatusTooManyRequests:
			t.Fatalf("login attempt %d returned %d, want 429: the rate limiter was bypassed without Redis", attempt, reply.status)
		}
	}
	if after := d.count(`select count(*) from sessions where revoked_at is null`); after < sessionsBefore {
		t.Fatalf("live sessions dropped from %d to %d during the Redis outage", sessionsBefore, after)
	}
	if after := d.ledger(); after < ledgerBefore {
		t.Fatalf("ledger regressed from %d to %d during the Redis outage", ledgerBefore, after)
	}
	t.Log("injected Redis outage: sessions, ledger and the local rate-limit fallback stayed intact")

	t.Log("fault injection: removing the owned Redis container and recreating it from the stack definition")
	d.remote("remove redis")
	d.remote("up")
	waitFor(t, 120*time.Second, "the recreated Redis reports healthy", func() bool {
		status, state, _, redis := d.readyz()
		return status == http.StatusOK && state == "ok" && redis == "ok"
	})
	if reply := d.reply("GET", "/api/v1/admin/overview", nil, true); reply.status != http.StatusOK {
		t.Fatalf("the session did not survive the Redis container recreation (%d %s)", reply.status, reply.code)
	}
	if after := d.ledger(); after < ledgerBefore {
		t.Fatalf("ledger regressed from %d to %d across the Redis recreation", ledgerBefore, after)
	}
	restored = true
	// Real traffic must be credited again through the recreated rate limiter.
	credited := d.ledger()
	tr.CloseIdleConnections()
	if !dockerFetch(client) {
		t.Fatal("proxy traffic failed after the Redis recreation")
	}
	waitFor(t, 60*time.Second, "usage credited after the Redis recreation", func() bool { return d.ledger() > credited })
	d.applied()
	t.Log("injected Redis outage recovered: ledger preserved and new usage credited")
}

// faultCapacityPhase replaces the owned agent1 with a copy of its state on a
// 16 MiB tmpfs, fills that filesystem until ENOSPC and proves the agent stops
// the affected proxy instead of dropping usage - then restores the capacity and
// lets the panel coordinate the agent back to running.
func faultCapacityPhase(t *testing.T, d *dockerTest, client *http.Client, tr *http.Transport) {
	t.Helper()
	running, instances := d.agentRuntime(d.fixture.Servers[0])
	if running < 1 {
		t.Fatalf("agent1 does not report a running instance before the capacity fault (running=%d instances=%s)", running, instances)
	}
	ledgerBefore := d.ledger()
	restored := false
	defer func() {
		if restored {
			return
		}
		if _, err := d.tryFault("capacity-down"); err != nil {
			t.Errorf("restore the owned agent1 after the capacity fault: %v", err)
		}
	}()

	t.Log("fault injection: moving the owned agent1 state onto a bounded 16 MiB tmpfs")
	d.fault("capacity-up")
	limits := assertBoundedCapacity(t, d.faultInfo())
	t.Logf("bounded capacity filesystem: tmpfs=%s total=%d KiB privileged=%s network=%s memory=%d cpus=%d pids=%d securityopt=%s ca=%s state_owner=%s agent_uid=%s",
		limits.tmpfs, limits.totalKiB, limits.privileged, limits.network, limits.memory,
		limits.cpus, limits.pids, limits.securityopt, limits.ca, limits.owner, limits.uid)
	waitFor(t, 120*time.Second, "the bounded capacity agent serves the owned node", func() bool {
		tr.CloseIdleConnections()
		return dockerFetch(client)
	})

	t.Log("fault injection: filling the bounded state filesystem until the kernel refuses writes")
	fill := d.fault("capacity-fill")
	if !strings.Contains(fill, "enospc=yes") {
		t.Fatalf("the bounded state filesystem still accepted writes after the fill: %s", strings.TrimSpace(fill))
	}
	t.Logf("capacity filesystem exhausted: %s", strings.TrimSpace(fill))
	faultPoll(t, 120*time.Second, 2*time.Second, "the agent stops the affected proxy when it cannot persist usage", func() bool {
		tr.CloseIdleConnections()
		return !dockerFetch(client)
	})
	var stopped agentObservation
	waitFor(t, 120*time.Second, "the panel reports the affected instance stopped", func() bool {
		current := d.agentObservation(d.fixture.Servers[0])
		if current.Running == 0 && len(current.Instances) == 0 {
			stopped = current
			return true
		}
		return false
	})
	if stopped.Heartbeat.IsZero() {
		t.Fatal("the panel reports no heartbeat timestamp for the stopped agent: the observation cannot be attributed to a heartbeat")
	}
	after := d.faultInfo()
	if !strings.Contains(after, "status=running") {
		t.Fatalf("the agent that stopped the affected proxy is no longer running:\n%s", after)
	}
	t.Logf("ENOSPC handling: the agent kept running, stopped the affected proxy and a fresh heartbeat %s reports no running instance",
		stopped.Heartbeat.Format(time.RFC3339Nano))

	t.Log("fault injection: restoring the state filesystem capacity")
	capacityFree := d.fault("capacity-free")
	if strings.Contains(capacityFree, "fillers=") && !strings.Contains(capacityFree, "fillers=0") {
		t.Fatalf("the filler files were not removed from the dedicated state directory: %s", strings.TrimSpace(capacityFree))
	}
	// The panel publishes a new revision; the agent must apply it and serve the
	// node again on its own (no manual process start).
	d.memberPatch(map[string]any{"active": true, "proxy_enabled": true})
	// Barrier instead of a fixed sleep: the panel only refreshes the instance
	// list and the running count from a heartbeat, and the heartbeat runs on its
	// own 2s timer, so an immediate read after the first successful connection
	// can still carry the stopped state. The recovery is accepted only once a
	// heartbeat strictly newer than the one that reported the stop says the node
	// is applied again AND real traffic through the restarted core succeeds in
	// the same round.
	var (
		recovered      agentObservation
		recoveredState string
	)
	deadline := time.Now().Add(180 * time.Second)
	for {
		tr.CloseIdleConnections()
		served := dockerFetch(client)
		current := d.agentObservation(d.fixture.Servers[0])
		if served {
			if state, ok := current.recoveredAfter(d.fixture.Nodes[0], stopped.Heartbeat); ok {
				recovered, recoveredState = current, state
				break
			}
		}
		if time.Now().After(deadline) {
			state, known := current.instance(d.fixture.Nodes[0])
			t.Fatalf("no fresh heartbeat attributed the recovered instance: proxy_traffic=%v last_heartbeat=%s stop_heartbeat=%s running=%d instances=%+v node=%s node_state=%q node_known=%v",
				served, current.Heartbeat.Format(time.RFC3339Nano), stopped.Heartbeat.Format(time.RFC3339Nano),
				current.Running, current.Instances, d.fixture.Nodes[0], state, known)
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("capacity recovery: fresh heartbeat %s (stop heartbeat %s) reports %d running instance(s) and node %s state %q while real proxy traffic is served",
		recovered.Heartbeat.Format(time.RFC3339Nano), stopped.Heartbeat.Format(time.RFC3339Nano), recovered.Running, d.fixture.Nodes[0], recoveredState)
	d.applied()
	if after := d.ledger(); after < ledgerBefore {
		t.Fatalf("ledger regressed from %d to %d across the capacity fault", ledgerBefore, after)
	}
	// Real usage is credited again after the recovery.
	credited := d.ledger()
	tr.CloseIdleConnections()
	if !dockerFetch(client) {
		t.Fatal("proxy traffic failed after the capacity recovery")
	}
	waitFor(t, 60*time.Second, "usage credited after the capacity recovery", func() bool { return d.ledger() > credited })

	// Leave the owned stack as it was: the real agent1 container comes back on
	// its own named state volume.
	d.fault("capacity-down")
	restored = true
	waitFor(t, 120*time.Second, "the real agent1 serves the owned node again", func() bool {
		tr.CloseIdleConnections()
		return dockerFetch(client)
	})
	d.applied()
	t.Log("bounded capacity injection recovered: proxy stopped under ENOSPC, coordinated back to running, real agent1 restored")
}

// TestFaultStateDumpDecoder checks the parser used by the fault phases against
// the exact framing the fault script prints, without any remote access.
func TestFaultStateDumpDecoder(t *testing.T) {
	payload := []byte("SQLite format 3\x00acceptance-state-payload")
	encoded := base64.StdEncoding.EncodeToString(payload)
	wrapped := encoded[:8] + "\r\n" + encoded[8:] + "\n"
	dump := "FILE agent.db " + strconv.Itoa(len(payload)) + "\n" + wrapped + "END agent.db\n" +
		"FILE agent.db-wal 3\n" + base64.StdEncoding.EncodeToString([]byte("wal")) + "\nEND agent.db-wal\n"
	files, err := decodeStateDump(dump)
	if err != nil {
		t.Fatal(err)
	}
	if string(files["agent.db"]) != string(payload) || string(files["agent.db-wal"]) != "wal" {
		t.Fatalf("decoded state dump mismatch: %d / %d bytes", len(files["agent.db"]), len(files["agent.db-wal"]))
	}
	if _, err := decodeStateDump("FILE agent.db 99\n" + encoded + "\nEND agent.db\n"); err == nil {
		t.Fatal("a state dump with a size mismatch must be rejected")
	}
	if _, err := decodeStateDump("FILE agent.db 4\n" + encoded + "\n"); err == nil {
		t.Fatal("a truncated state dump must be rejected")
	}
}

// TestCapacityInfoParsing pins the exact capacity-info framing the fault script
// prints, so a drift in the runtime limit evidence (for example a missing CA
// mount or an unset PID/CPU limit) fails locally instead of only after a remote
// run.
func TestCapacityInfoParsing(t *testing.T) {
	root := "/opt/proxypanel-acceptance-20261021"
	info := "container name=/ppaccept-20261021-agent1cap status=running privileged=false network=ppaccept-20261021-net memory=201326592 cpus=500000000 pids=128 restarts=0\n" +
		"securityopt [\"no-new-privileges\"]\n" +
		"capdrop [\"ALL\"]\n" +
		"tmpfs {\"/state\":\"rw,noexec,nosuid,size=16m\",\"/tmp\":\"rw,noexec,nosuid,size=16m\"}\n" +
		"binds [\"" + root + "/faultstate/agent1:/run/seed:ro\",\"" + root + "/secrets/agent1.json:/run/fixture/agent1.json:ro\",\"" + root + "/secrets/ca.pem:/run/fixture/ca.pem:ro\"]\n" +
		"uid=0\nca=present\nstate owner=0\nstate files=3\nstate df: /dev/shm 16384 96 16288 1% /state\n"
	limits := assertBoundedCapacity(t, info)
	if limits.cpus != 500000000 || limits.pids != 128 || limits.ca != "present" || limits.owner != limits.uid {
		t.Fatalf("unexpected parsed limits: %+v", limits)
	}
	// An unset PID limit is reported as null and must never look bounded.
	unset := parseCapacityInfo(t, strings.Replace(info, "pids=128", "pids=null", 1))
	if unset.pids != 0 {
		t.Fatalf("an unset PID limit parsed as %d", unset.pids)
	}
}

// TestAgentObservationRecoveryBarrier pins the freshness rule the capacity
// recovery uses: the panel only refreshes instances/metrics from a heartbeat, so
// an observation that is not newer than the heartbeat which reported the stop
// never proves the instance is back, no matter what values it still carries.
func TestAgentObservationRecoveryBarrier(t *testing.T) {
	stop := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	const node = "node-1"
	applied := []contracts.InstanceResult{{InstanceID: node, State: "applied"}}
	fresh := stop.Add(2 * time.Second)
	if state, ok := (agentObservation{Running: 1, Instances: applied, Heartbeat: fresh}).recoveredAfter(node, stop); !ok || state != "applied" {
		t.Fatalf("a fresh applied heartbeat was not accepted: state=%q ok=%v", state, ok)
	}
	if state, ok := (agentObservation{Running: 1, Instances: []contracts.InstanceResult{{InstanceID: node, State: "rollback"}}, Heartbeat: fresh}).recoveredAfter(node, stop); !ok || state != "rollback" {
		t.Fatalf("a fresh rollback heartbeat was not accepted: state=%q ok=%v", state, ok)
	}
	rejected := map[string]agentObservation{
		"stale heartbeat with a running count": {Running: 1, Instances: applied, Heartbeat: stop},
		"no heartbeat at all":                  {Running: 1, Instances: applied},
		"fresh heartbeat, nothing running":     {Running: 0, Instances: applied, Heartbeat: fresh},
		"fresh heartbeat, failed instance":     {Running: 1, Instances: []contracts.InstanceResult{{InstanceID: node, State: "failed"}}, Heartbeat: fresh},
		"fresh heartbeat, other node":          {Running: 1, Instances: []contracts.InstanceResult{{InstanceID: "node-2", State: "applied"}}, Heartbeat: fresh},
		"fresh heartbeat, no instances":        {Running: 1, Heartbeat: fresh},
	}
	for name, observation := range rejected {
		if state, ok := observation.recoveredAfter(node, stop); ok {
			t.Fatalf("%s must not prove the recovery: state=%q observation=%+v", name, state, observation)
		}
	}
}
