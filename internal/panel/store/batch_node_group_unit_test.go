package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"proxypanel/internal/panel/domain"
)

// AC-NODE-01: adding a node to a referenced group. A node that no group
// contained yet has no entry in the evidence node -> groups map, so a
// simulation that only rewrites the groups of already mapped nodes drops it
// from afterDecision.AllowedNodeIDs. The preview would then report no added
// node and the submit-time reconciliation would never create the credential,
// even though the public subscription output (which re-reads the committed
// group map) lists the node. This guards the pure simulation half of that
// contract; the credential row itself needs PostgreSQL.
func TestSimulateUpdateNodeGroupAllowsNewlyAddedNode(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	subID := "sub_nodes"
	quota := int64(1) << 30
	ev := &Evidence{
		Member: &domain.Member{ID: "mbr_nodes", Username: "nodes-user", Active: true, ProxyEnabled: true,
			SubscriptionID: &subID},
		Subscription: &domain.Subscription{ID: subID, Name: "sub-nodes", AccessEnabled: true,
			NodeGroupIDs: []string{"grp_one", "grp_two"}, QuotaBytes: &quota, Timezone: "UTC", AnchorDay: 1},
		Nodes: []domain.Node{
			{ID: "node_one", Name: "node-one", Enabled: true},
			{ID: "node_two", Name: "node-two", Enabled: true},
			{ID: "node_three", Name: "node-three", Enabled: true},
		},
		// node_three belongs to no group yet: the map has no entry for it.
		NodeGroups: map[string][]string{
			"node_one": {"grp_one", "grp_two"},
			"node_two": {"grp_two"},
		},
	}
	before := ev.Decision(now)
	if len(before.AllowedNodeIDs) != 2 {
		t.Fatalf("before the group change %d nodes are allowed, want 2 (%v)", len(before.AllowedNodeIDs), before.AllowedNodeIDs)
	}

	var values domain.Object
	raw, err := json.Marshal(map[string]any{
		"id":    "grp_one",
		"patch": map[string]any{"node_ids": []string{"node_one", "node_three"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	req := PreviewRequest{Operation: OpUpdateNodeGroup, Values: values}

	after, err := (&Store{}).simulateChange(context.Background(), ev, req, nil,
		[]string{"node_one", "node_three"}, now)
	if err != nil {
		t.Fatalf("simulate %s: %v", OpUpdateNodeGroup, err)
	}
	counts := map[string]int{}
	for _, id := range after.AllowedNodeIDs {
		counts[id]++
	}
	if counts["node_three"] != 1 {
		t.Fatalf("the newly added node is allowed %d times, want 1 (allowed %v)",
			counts["node_three"], after.AllowedNodeIDs)
	}
	// node_one is covered by both authorized groups and must stay deduplicated.
	if counts["node_one"] != 1 || counts["node_two"] != 1 || len(after.AllowedNodeIDs) != 3 {
		t.Fatalf("allowed nodes = %v, want node_one/node_two/node_three exactly once each", after.AllowedNodeIDs)
	}
	// The simulation must not leak its group rewrite into the shared evidence.
	if got := ev.NodeGroups["node_three"]; len(got) != 0 {
		t.Fatalf("the simulation mutated the shared evidence: node_three groups = %v", got)
	}
	if got := len(ev.NodeGroups["node_one"]); got != 2 {
		t.Fatalf("the simulation mutated the shared evidence: node_one groups = %v", ev.NodeGroups["node_one"])
	}

	// Dropping the node from the patched group set must take it away again.
	after, err = (&Store{}).simulateChange(context.Background(), ev, req, nil, []string{"node_one"}, now)
	if err != nil {
		t.Fatalf("simulate %s without the new node: %v", OpUpdateNodeGroup, err)
	}
	for _, id := range after.AllowedNodeIDs {
		if id == "node_three" {
			t.Fatalf("a node outside the patched group is still allowed: %v", after.AllowedNodeIDs)
		}
	}
}
