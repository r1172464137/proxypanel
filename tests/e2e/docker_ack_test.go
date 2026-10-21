package e2e

// End-to-end acceptance for goal criterion A08/A10: a lost usage acknowledgement
// must be re-sent as the same batch and PostgreSQL must credit it exactly once
// (goal .pi/goal/完成计划剩余任务与-docker-ssh-隧道验收-20261021-1045.md).
//
// What is real here:
//   - the owned acceptance panel reached over its SSH tunnel (TLS + the real
//     bearer agent authentication);
//   - the acceptance PostgreSQL ledger;
//   - the real agent state store (internal/agent/state: SQLite outbox with
//     baselines, batches and samples);
//   - the real agent sampler and uploader (internal/agent/reporting) driven
//     through the real agent control client (internal/agent/control).
//
// The only injected fault is a loopback-only TLS reverse proxy owned by the test
// process. It forwards the usage request to the real panel, waits for the
// committed "accepted" acknowledgement (PostgreSQL already holds the batch and
// the ledger rows at that point) and then drops exactly that one connection
// instead of returning the response. The agent therefore never sees the ACK, has
// to keep the batch and re-sends the same batch id; the panel answers
// "duplicate" and the ledger must not grow a second time.
//
// TLS and authentication are never weakened: the panel hop reuses the acceptance
// CA (the same http.Transport the other Docker cases use), the proxy hop gets its
// own ephemeral in-process CA, and no listener leaves 127.0.0.1. The proxy only
// ever drops a response whose panel acknowledgement is "accepted".
//
// The case shares the Docker acceptance fixture with TestDockerAcceptance and
// must not run in parallel.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"proxypanel/internal/agent/control"
	"proxypanel/internal/agent/reporting"
	"proxypanel/internal/agent/state"
	"proxypanel/internal/agent/xray"
	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/domain"

	_ "modernc.org/sqlite"
)

// ackReply is one usage answer the panel committed for the proxied request.
type ackReply struct {
	Path    string
	Status  int
	Ack     string
	Dropped bool
}

// ackProxy terminates TLS for the agent, forwards every request to the real
// panel and drops the first successfully committed usage acknowledgement.
type ackProxy struct {
	target  *url.URL
	round   http.RoundTripper
	server  *httptest.Server
	mu      sync.Mutex
	replies []ackReply
	dropped int
}

func newAckProxy(target string, round http.RoundTripper) (*ackProxy, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(target), "/"))
	if err != nil {
		return nil, fmt.Errorf("ack proxy target %q: %w", target, err)
	}
	if u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("ack proxy target %q must be an absolute URL", target)
	}
	if round == nil {
		round = http.DefaultTransport
	}
	p := &ackProxy{target: u, round: round}
	mux := http.NewServeMux()
	mux.HandleFunc("/", p.handle)
	p.server = httptest.NewTLSServer(mux)
	return p, nil
}

func (p *ackProxy) Close() {
	if p.server != nil {
		p.server.Close()
	}
}

func (p *ackProxy) URL() string { return p.server.URL }

// Client returns the agent-side HTTP client. It trusts only the proxy's
// ephemeral certificate and keeps TLS verification and a 1.2 minimum enabled.
func (p *ackProxy) Client() *http.Client {
	roots := x509.NewCertPool()
	roots.AddCert(p.server.Certificate())
	return &http.Client{
		Transport: &http.Transport{
			Proxy:           nil,
			TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		},
		Timeout: 15 * time.Second,
	}
}

func (p *ackProxy) droppedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dropped
}

func (p *ackProxy) usageReplies() []ackReply {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ackReply, 0, len(p.replies))
	for _, reply := range p.replies {
		if reply.Path == control.PathUsage {
			out = append(out, reply)
		}
	}
	return out
}

// record stores one panel answer and reports whether this answer must be
// dropped. Only the first committed usage acknowledgement is ever dropped.
func (p *ackProxy) record(reply ackReply) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	drop := reply.Path == control.PathUsage && reply.Status == http.StatusOK &&
		reply.Ack == "accepted" && p.dropped == 0
	if drop {
		p.dropped++
		reply.Dropped = true
	}
	p.replies = append(p.replies, reply)
	return drop
}

func (p *ackProxy) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "ack proxy: read request body", http.StatusBadRequest)
		return
	}
	target := *p.target
	target.Path = strings.TrimRight(p.target.Path, "/") + r.URL.Path
	target.RawQuery = r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "ack proxy: build panel request", http.StatusInternalServerError)
		return
	}
	for _, header := range []string{"Authorization", "Content-Type", "Accept", "User-Agent"} {
		if value := r.Header.Get(header); value != "" {
			req.Header.Set(header, value)
		}
	}
	res, err := p.round.RoundTrip(req)
	if err != nil {
		http.Error(w, "ack proxy: panel request failed", http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		http.Error(w, "ack proxy: read panel response", http.StatusBadGateway)
		return
	}
	ack := ""
	var envelope struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if json.Unmarshal(answer, &envelope) == nil {
		ack = envelope.Data.Status
	}
	if p.record(ackReply{Path: r.URL.Path, Status: res.StatusCode, Ack: ack}) {
		// The panel committed and answered; drop the response at the transport
		// level so the agent sees a broken connection, never an acknowledgement.
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, hijackErr := hijacker.Hijack(); hijackErr == nil {
				_ = conn.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	}
	if contentType := res.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(answer)
}

// TestACKLossProxyInjection proves the injected fault itself without Docker: the
// first committed acknowledgement never reaches the client while the panel did
// receive and answer the request, and the re-send is answered normally.
func TestACKLossProxyInjection(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil || len(body) == 0 || r.URL.Path != control.PathUsage {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer ack-loss-token" {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		hits++
		first := hits == 1
		mu.Unlock()
		status := "duplicate"
		if first {
			status = "accepted"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"batch_id": "ack-loss-batch", "status": status},
		})
	}))
	defer panel.Close()

	proxy, err := newAckProxy(panel.URL, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	client, err := control.New(proxy.URL(), "ack-loss-proxy-test")
	if err != nil {
		t.Fatal(err)
	}
	client.SetHTTPClient(proxy.Client())
	client.SetToken("ack-loss-token")
	batch := contracts.UsageBatch{
		SchemaVersion: contracts.SchemaVersion,
		BatchID:       "ack-loss-batch",
		Samples: []contracts.UsageSample{{
			NodeID: "ack-node", MemberID: "ack-member", CoreGeneration: "ack-gen",
			StartedAt: time.Unix(1700000000, 0).UTC(), EndedAt: time.Unix(1700000001, 0).UTC(),
			UploadBytes: 1, DownloadBytes: 2,
		}},
	}
	if _, err := client.Usage(context.Background(), batch); err == nil {
		t.Fatal("the injected connection drop did not surface as an upload error")
	}
	ack, err := client.Usage(context.Background(), batch)
	if err != nil {
		t.Fatalf("the re-send after the dropped acknowledgement failed: %v", err)
	}
	if ack.Status != "duplicate" || ack.BatchID != batch.BatchID {
		t.Fatalf("unexpected re-send acknowledgement: %+v", ack)
	}
	if proxy.droppedCount() != 1 {
		t.Fatalf("the proxy dropped %d responses, want exactly 1", proxy.droppedCount())
	}
	replies := proxy.usageReplies()
	if len(replies) != 2 {
		t.Fatalf("the panel answered %d usage requests, want 2: %+v", len(replies), replies)
	}
	if replies[0].Status != http.StatusOK || replies[0].Ack != "accepted" || !replies[0].Dropped {
		t.Fatalf("the first (dropped) answer was not the committed acceptance: %+v", replies[0])
	}
	if replies[1].Status != http.StatusOK || replies[1].Ack != "duplicate" || replies[1].Dropped {
		t.Fatalf("the re-send was not acknowledged as a duplicate: %+v", replies[1])
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 2 {
		t.Fatalf("the panel saw %d usage requests, want 2", hits)
	}
}

// TestACKLossAgentOutbox runs the agent half of the same case without the
// remote panel: the real sampler and state store create the batch, the real
// uploader loses its first acknowledgement through the proxy and re-sends the
// same batch id. The fake panel only counts credits, so this is not the
// PostgreSQL evidence (that is TestDockerACKLossAcceptance) but it does prove
// the durable outbox re-send path deterministically on every run.
func TestACKLossAgentOutbox(t *testing.T) {
	var mu sync.Mutex
	requests, credits := 0, 0
	seen := map[string]bool{}
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != control.PathUsage {
			http.NotFound(w, r)
			return
		}
		var batch contracts.UsageBatch
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&batch); err != nil || batch.BatchID == "" {
			http.Error(w, "bad usage batch", http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests++
		status := "accepted"
		if seen[batch.BatchID] {
			status = "duplicate"
		} else {
			seen[batch.BatchID] = true
			credits++
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"batch_id": batch.BatchID, "status": status},
		})
	}))
	defer panel.Close()

	proxy, err := newAckProxy(panel.URL, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	client, err := control.New(proxy.URL(), "ack-loss-outbox-test")
	if err != nil {
		t.Fatal(err)
	}
	client.SetHTTPClient(proxy.Client())
	client.SetToken("ack-loss-token")

	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open the real agent state store: %v", err)
	}
	defer store.Close()
	now := time.Now().UTC().Add(-time.Second)
	sampler := reporting.NewSampler(store, ackStats{
		node: "ack-node", member: "ack-member", email: "member@acceptance.test", up: 1024, down: 2048,
	}, log.New(io.Discard, "", 0))
	sampler.SetNow(func() time.Time { return now })
	round, err := sampler.RunOnce(context.Background())
	if err != nil || len(round.BatchIDs) != 1 {
		t.Fatalf("the sampler produced no durable batch: %+v (%v)", round, err)
	}
	batchID := round.BatchIDs[0]

	uploader := reporting.NewUploader(store, client, nil, log.New(io.Discard, "", 0))
	if first, err := uploader.RunOnce(context.Background()); err == nil || first.Failed != 1 || first.Acked != 0 {
		t.Fatalf("the unacknowledged batch was not retained: %+v (%v)", first, err)
	}
	if pending, _ := store.PendingBatches(context.Background(), 10); len(pending) != 1 || pending[0].BatchID != batchID {
		t.Fatalf("the outbox lost the batch after the dropped acknowledgement: %+v", pending)
	}
	second, err := uploader.RunOnce(context.Background())
	if err != nil || second.Acked != 1 || second.Failed != 0 {
		t.Fatalf("the re-send was not acknowledged: %+v (%v)", second, err)
	}
	if pending, _ := store.PendingBatches(context.Background(), 10); len(pending) != 0 {
		t.Fatalf("the acknowledged batch stayed in the outbox: %+v", pending)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 || credits != 1 {
		t.Fatalf("the panel saw %d request(s) and credited %d time(s), want 2/1", requests, credits)
	}
}

// ackStats is the StatsSource of one owned node. It only supplies counters: the
// batching, baseline handling and outbox durability are the real agent code.
type ackStats struct {
	node, member, email string
	up, down            int64
}

func (s ackStats) QueryStats(context.Context) []xray.InstanceStats {
	return []xray.InstanceStats{{
		InstanceID:     "ack-loss-instance",
		NodeID:         s.node,
		CoreGeneration: "ack-loss-generation",
		EmailToMember:  map[string]string{s.email: s.member},
		Counters: []xray.StatCounter{
			{Name: "user>>>" + s.email + ">>>traffic>>>uplink", Value: s.up},
			{Name: "user>>>" + s.email + ">>>traffic>>>downlink", Value: s.down},
		},
	}}
}

type ackTarget struct {
	Node   string
	Member string
}

// ackAgentIdentity reads the identity of the owned agent1 from its own state
// database. The token is only ever used as the real bearer credential, never
// logged, and the decoded copy stays in the test's temporary directory.
func ackAgentIdentity(t *testing.T, d *dockerTest) (agentID, token string) {
	t.Helper()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		agentID, token, lastErr = ackReadIdentity(t, d)
		if lastErr != nil {
			continue
		}
		if agentID == "" || token == "" {
			t.Fatal("agent1 holds no registered identity in its state: run TestDockerAcceptance first so the owned agent registered")
		}
		return agentID, token
	}
	t.Fatalf("read the owned agent1 identity: %v (the fault script must be deployed and agent1 must have registered once)", lastErr)
	return "", ""
}

// ackReadIdentity copies the live agent1 state into the test's temporary
// directory and reads the identity there. A live SQLite copy can be torn by a
// concurrent checkpoint, so the caller retries with a fresh copy.
func ackReadIdentity(t *testing.T, d *dockerTest) (agentID, token string, err error) {
	t.Helper()
	raw, err := d.tryFault("state-dump agent1")
	if err != nil {
		return "", "", err
	}
	files, err := decodeStateDump(raw)
	if err != nil {
		return "", "", err
	}
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			return "", "", err
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "agent.db"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return "", "", err
	}
	defer db.Close()
	var firstErr error
	for key, dest := range map[string]*string{"agent_id": &agentID, "token": &token} {
		if qerr := db.QueryRow(`select value from meta where key=?`, key).Scan(dest); qerr != nil && qerr != sql.ErrNoRows && firstErr == nil {
			firstErr = qerr
		}
	}
	if firstErr != nil {
		return "", "", firstErr
	}
	return agentID, token, nil
}

// ackUsageTarget picks a node of the agent's own server for which the owned
// fixture member has credential history - the panel rejects every other node.
func ackUsageTarget(t *testing.T, d *dockerTest, agentID string) ackTarget {
	t.Helper()
	var node string
	err := d.db.QueryRow(context.Background(), `select n.id from credentials c
		join nodes n on n.id=c.node_id join agents a on a.server_id=n.server_id
		where c.member_id=$1 and a.id=$2 order by (c.state='active') desc, c.created_at desc limit 1`,
		d.fixture.Member, agentID).Scan(&node)
	if err != nil {
		t.Fatalf("the owned fixture has no credential history for member %s on a node of agent %s: %v", d.fixture.Member, agentID, err)
	}
	owned := false
	for _, id := range d.fixture.Nodes {
		if id == node {
			owned = true
		}
	}
	if !owned {
		t.Fatalf("node %s is not one of the owned fixture nodes", node)
	}
	return ackTarget{Node: node, Member: d.fixture.Member}
}

// ackLedger is the PostgreSQL evidence for one batch.
type ackLedger struct {
	Batches int64
	Entries int64
	Bytes   int64
}

func (d *dockerTest) ackLedger(agentID, batchID string) ackLedger {
	d.t.Helper()
	return ackLedger{
		Batches: d.count(`select count(*) from usage_batches where agent_id=$1 and batch_id=$2 and status='accepted'`, agentID, batchID),
		Entries: d.count(`select count(*) from usage_entries where agent_id=$1 and batch_id=$2`, agentID, batchID),
		Bytes:   d.count(`select coalesce(sum(up_bytes+down_bytes),0) from usage_entries where agent_id=$1 and batch_id=$2`, agentID, batchID),
	}
}

// TestDockerACKLossAcceptance is the ACK-loss end-to-end entry: real agent
// reporting/state, real panel, real PostgreSQL, one dropped committed
// acknowledgement, the same batch re-sent, and exactly one ledger credit.
func TestDockerACKLossAcceptance(t *testing.T) {
	d := dockerSetup(t)
	if status, state, postgres, _ := d.readyz(); status != http.StatusOK || postgres != "ok" {
		t.Fatalf("the acceptance panel is not healthy for the ACK-loss case (status=%d state=%q postgres=%q)", status, state, postgres)
	}
	agentID, token := ackAgentIdentity(t, d)
	target := ackUsageTarget(t, d, agentID)
	now, err := dockerServerNow(d.db)
	if err != nil {
		t.Fatalf("PostgreSQL clock query failed: %v", err)
	}
	// One second in the past keeps the sample inside the accepted clock window
	// even if the panel container clock differs by a fraction of a second.
	now = now.Add(-time.Second)

	proxy, err := newAckProxy(d.api.base, d.transport.Clone())
	if err != nil {
		t.Fatalf("start the controlled ACK-loss proxy: %v", err)
	}
	t.Cleanup(proxy.Close)
	client, err := control.New(proxy.URL(), "ack-loss-acceptance")
	if err != nil {
		t.Fatalf("build the real agent control client: %v", err)
	}
	client.SetHTTPClient(proxy.Client())
	client.SetToken(token)
	client.SetAgentID(agentID)

	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open the real agent state store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const upload, download int64 = 4096, 8192
	sampler := reporting.NewSampler(store, ackStats{
		node: target.Node, member: target.Member,
		email: "docker-alice@acceptance.test", up: upload, down: download,
	}, log.New(io.Discard, "", 0))
	sampler.SetNow(func() time.Time { return now })
	round, err := sampler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("the real agent sampler failed: %v", err)
	}
	if len(round.BatchIDs) != 1 || round.SampleRows != 1 {
		t.Fatalf("the sampler produced %d batch(es) / %d sample row(s), want 1/1", len(round.BatchIDs), round.SampleRows)
	}
	batchID := round.BatchIDs[0]
	ledgerBefore := d.ledger()

	// First upload: the panel commits, the proxy drops the acknowledgement.
	uploader := reporting.NewUploader(store, client, nil, log.New(io.Discard, "", 0))
	first, firstErr := uploader.RunOnce(context.Background())
	if firstErr == nil {
		t.Fatalf("the uploader reported success although the acknowledgement was dropped: %+v", first)
	}
	if first.Acked != 0 || first.Failed != 1 || len(first.Retained) != 1 || first.Retained[0] != batchID {
		t.Fatalf("the unacknowledged batch was not retained for a re-send: %+v (error %v)", first, firstErr)
	}
	retained, err := store.PendingBatches(context.Background(), 10)
	if err != nil {
		t.Fatalf("read the real agent outbox: %v", err)
	}
	if len(retained) != 1 || retained[0].BatchID != batchID || retained[0].Attempts < 1 || retained[0].LastError == "" {
		t.Fatalf("the durable outbox did not keep the failed batch with its attempt: %+v", retained)
	}
	if got := proxy.droppedCount(); got != 1 {
		t.Fatalf("the proxy dropped %d acknowledgements, want exactly 1", got)
	}
	if replies := proxy.usageReplies(); len(replies) != 1 || replies[0].Ack != "accepted" || !replies[0].Dropped {
		t.Fatalf("the panel did not commit the batch before the acknowledgement was dropped: %+v", replies)
	}
	committed := d.ackLedger(agentID, batchID)
	// The sample sits inside one cycle, so one ledger row is expected; a boundary
	// split would still have to add up to the same bytes exactly once.
	if committed.Batches != 1 || committed.Entries < 1 || committed.Bytes != upload+download {
		t.Fatalf("PostgreSQL does not hold exactly one committed batch after the dropped acknowledgement: %+v", committed)
	}
	ledgerAfterDrop := d.ledger()
	if delta := ledgerAfterDrop - ledgerBefore; delta != upload+download {
		t.Fatalf("the member ledger moved by %d bytes while only one batch was committed, want %d", delta, upload+download)
	}
	t.Logf("injected ACK loss: panel committed batch %s (1 batch row / 1 ledger row / %d bytes) and the agent kept it after %q",
		batchID, committed.Bytes, retained[0].LastError)

	// Same batch id, second upload: the panel must answer "duplicate" and the
	// ledger must not move.
	second, secondErr := uploader.RunOnce(context.Background())
	if secondErr != nil {
		t.Fatalf("the re-send of the retained batch failed: %v", secondErr)
	}
	if second.Acked != 1 || second.Failed != 0 {
		t.Fatalf("the re-send was not acknowledged: %+v", second)
	}
	if pending, err := store.PendingBatches(context.Background(), 10); err != nil || len(pending) != 0 {
		t.Fatalf("the acknowledged batch stayed in the outbox: %+v (%v)", pending, err)
	}
	replies := proxy.usageReplies()
	if len(replies) != 2 || replies[1].Status != http.StatusOK || replies[1].Ack != "duplicate" || replies[1].Dropped {
		t.Fatalf("the re-sent batch id was not acknowledged as a duplicate: %+v", replies)
	}
	afterResend := d.ackLedger(agentID, batchID)
	if afterResend != committed {
		t.Fatalf("the re-sent batch was credited again: %+v, want %+v", afterResend, committed)
	}
	if ledger := d.ledger(); ledger != ledgerAfterDrop {
		t.Fatalf("the member ledger grew from %d to %d bytes on the duplicate re-send", ledgerAfterDrop, ledger)
	}

	// Same batch id with a different payload must be refused, not re-credited.
	var started, ended time.Time
	if err := d.db.QueryRow(context.Background(),
		`select sample_started_at, sample_ended_at from usage_entries where agent_id=$1 and batch_id=$2`, agentID, batchID).
		Scan(&started, &ended); err != nil {
		t.Fatalf("read the committed sample window: %v", err)
	}
	mismatched := contracts.UsageBatch{
		SchemaVersion: contracts.SchemaVersion,
		BatchID:       batchID,
		Samples: []contracts.UsageSample{{
			NodeID: target.Node, MemberID: target.Member, CoreGeneration: "ack-loss-generation",
			StartedAt: started.UTC(), EndedAt: ended.UTC(),
			UploadBytes: upload + 1, DownloadBytes: download,
		}},
	}
	if _, err := client.Usage(context.Background(), mismatched); err == nil {
		t.Fatal("the panel accepted the same batch id with a different payload")
	} else {
		var apiErr *control.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != domain.CodeBatchPayloadMismatch {
			t.Fatalf("the same batch id with a different payload was not refused with %s/409: %v", domain.CodeBatchPayloadMismatch, err)
		}
	}
	if afterMismatch := d.ackLedger(agentID, batchID); afterMismatch != committed {
		t.Fatalf("the refused mismatched batch changed the ledger: %+v, want %+v", afterMismatch, committed)
	}
	if ledger := d.ledger(); ledger != ledgerAfterDrop {
		t.Fatalf("the refused mismatched batch moved the member ledger from %d to %d bytes", ledgerAfterDrop, ledger)
	}
	t.Logf("ACK loss end-to-end: batch %s committed once (%+v), re-sent once answered %q, mismatched payload refused (%s); member ledger %d bytes unchanged",
		batchID, committed, replies[1].Ack, domain.CodeBatchPayloadMismatch, ledgerAfterDrop-ledgerBefore)
}
