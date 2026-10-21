package store

import (
	"context"
	"strconv"

	"proxypanel/internal/contracts"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/xrayconf"
)

// BuildSnapshot always computes a full authoritative snapshot. Only the HTTP
// layer may answer 204 after comparing both client revision and generation.
//
// The build is linear in the number of members and nodes: the shared decision
// inputs (nodes, node group map, subscriptions, ledger and adjustment totals) and
// the credential secrets are loaded once, and the frozen access algorithm then
// runs per member on those inputs. Reloading the whole authorization state per
// member made a 205 member snapshot take minutes.
func (s *Store) BuildSnapshot(ctx context.Context, agent *domain.Agent) (*contracts.Snapshot, error) {
	control, err := s.GetControlState(ctx)
	if err != nil {
		return nil, err
	}
	// Recovery coordination: never push a possibly restored (older) permission
	// set to a node. The agent keeps its last, stricter state instead.
	if control.RecoveryMode {
		return nil, domain.E(domain.CodeRecoveryMode, 409,
			"panel is in recovery coordination mode; snapshots are paused until checks complete")
	}
	// Internal, page-through listings: a panel with more than 200 nodes or
	// members must still push the complete authoritative set, never a silently
	// truncated one.
	members, err := s.listAllMembers(ctx, "")
	if err != nil {
		return nil, err
	}
	// A cycle that ended is rolled forward before the ledger aggregates below are
	// read, so the decision uses the same cycle the rest of the panel persists.
	// A running cycle is a pure in-memory comparison and costs no query.
	for i := range members {
		if err := s.ensureMemberPeriod(ctx, &members[i]); err != nil {
			return nil, err
		}
	}
	inputs, err := s.LoadEvidenceBatch(ctx, members)
	if err != nil {
		return nil, err
	}
	nodes := make([]domain.Node, 0, len(inputs.Nodes))
	nodeIDs := make([]string, 0, len(inputs.Nodes))
	for _, n := range inputs.Nodes {
		if n.ServerID != agent.ServerID {
			continue
		}
		nodes = append(nodes, n)
		nodeIDs = append(nodeIDs, n.ID)
	}
	secrets, err := s.activeCredentialSecrets(ctx, nodeIDs)
	if err != nil {
		return nil, err
	}

	type memberState struct {
		member  *domain.Member
		access  bool
		allowed map[string]bool
	}
	now := s.now()
	states := make([]memberState, 0, len(members))
	for i := range members {
		m := &members[i]
		d := inputs.Evidence(m).Decision(now)
		allowed := make(map[string]bool, len(d.AllowedNodeIDs))
		for _, id := range d.AllowedNodeIDs {
			allowed[id] = true
		}
		states = append(states, memberState{member: m, access: d.Access, allowed: allowed})
	}

	instances := make([]contracts.Instance, 0, len(nodes))
	expected := 0
	for _, n := range nodes {
		clients := make([]xrayconf.Client, 0, len(states))
		users := make([]contracts.AuthorizedUser, 0, len(states))
		for i := range states {
			st := &states[i]
			if !st.access || !st.allowed[n.ID] {
				continue
			}
			secret := ""
			if byNode := secrets[st.member.ID]; byNode != nil {
				secret = byNode[n.ID]
			}
			if secret == "" {
				continue
			}
			email := xrayconf.ClientEmail(st.member.ID)
			clients = append(clients, xrayconf.Client{
				MemberID: st.member.ID, Email: email,
				UUID: secret, Password: secret, Flow: n.Flow,
			})
			users = append(users, contracts.AuthorizedUser{
				MemberID: st.member.ID, Email: email,
				ExpiresAt: st.member.ExpiresAt,
			})
		}
		cfg, err := xrayconf.Build(n, clients)
		if err != nil {
			return nil, err
		}
		expected += len(users)
		instances = append(instances, contracts.Instance{ID: n.ID, NodeID: n.ID, Config: cfg, Users: users})
	}
	// Re-read the desired revision after the build: evaluating the members may
	// have rolled a cycle forward and published a newer revision. Returning the
	// stale value would let the agent answer 204 for a snapshot it has not
	// actually received.
	revision := agent.DesiredRevision
	var fresh int64
	if err := s.DB.QueryRowContext(ctx, `SELECT desired_revision FROM agents WHERE id=$1`, agent.ID).Scan(&fresh); err == nil {
		revision = fresh
	}
	return &contracts.Snapshot{
		SchemaVersion:     contracts.SchemaVersion,
		ControlGeneration: strconv.FormatInt(control.ControlGeneration, 10),
		Revision:          revision,
		IssuedAt:          s.now(),
		ExpectedUserCount: expected,
		Instances:         instances,
	}, nil
}
