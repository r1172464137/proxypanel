package access

import (
	"testing"
	"time"
)

var now = time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC)

func ptr(v int64) *int64 { return &v }
func ptrTime(t time.Time) *time.Time { return &t }

func base() Input {
	return Input{
		Now:                       now,
		MemberID:                  "mbr_a",
		AccountActive:             true,
		ProxyEnabled:              true,
		BindingPresent:            true,
		SubscriptionID:            "sub_a",
		BindingExpiresAt:          ptrTime(now.Add(30 * 24 * time.Hour)),
		SubscriptionAccessEnabled: true,
		SubscriptionQuotaBytes:    ptr(1000),
		UsedBytes:                 100,
		AuthorizedGroupIDs:        []string{"ng_a"},
		Nodes: []NodeState{
			{ID: "nd_1", Name: "tokyo", Enabled: true, GroupIDs: []string{"ng_a"}},
			{ID: "nd_2", Name: "osaka", Enabled: true, GroupIDs: []string{"ng_b", "ng_a"}},
			{ID: "nd_3", Name: "disabled", Enabled: false, GroupIDs: []string{"ng_a"}},
			{ID: "nd_4", Name: "other-group", Enabled: true, GroupIDs: []string{"ng_z"}},
		},
	}
}

func TestDecideAllowsAuthorizedNodesOnly(t *testing.T) {
	d := Decide(base())
	if !d.Access || d.ProxyAccess != ProxyEnabled {
		t.Fatalf("expected access, got %+v", d)
	}
	if len(d.AllowedNodeIDs) != 2 || d.AllowedNodeIDs[0] != "nd_1" || d.AllowedNodeIDs[1] != "nd_2" {
		t.Fatalf("allowed nodes = %v", d.AllowedNodeIDs)
	}
	// node present in two groups is output once
	seen := map[string]int{}
	for _, id := range d.AllowedNodeIDs {
		seen[id]++
	}
	if seen["nd_2"] != 1 {
		t.Fatalf("node dedupe failed: %v", seen)
	}
	if d.NodeReasons["nd_3"] != ReasonNodeDisabled || d.NodeReasons["nd_4"] != ReasonNodeNotAuthorized {
		t.Fatalf("node reasons = %v", d.NodeReasons)
	}
	if d.Reasons[0] != ReasonOK {
		t.Fatalf("reasons = %v", d.Reasons)
	}
	if d.AlgorithmVersion != AlgorithmVersion {
		t.Fatalf("algorithm version missing")
	}
}

func TestDecideBlockedAccountIgnoresOverrides(t *testing.T) {
	in := base()
	in.AccountActive = false
	in.OverrideQuotaEnabled = true
	in.OverrideQuotaBytes = ptr(1 << 40)
	d := Decide(in)
	if d.Access {
		t.Fatal("suspended account must not have access")
	}
	if d.ProxyAccess != ProxyAccountBlocked || d.Reasons[0] != ReasonAccountSuspended {
		t.Fatalf("got %+v", d)
	}
	if len(d.AllowedNodeIDs) != 0 {
		t.Fatalf("no nodes may be allowed, got %v", d.AllowedNodeIDs)
	}
}

func TestDecideArchivedAccount(t *testing.T) {
	in := base()
	in.AccountArchived = true
	d := Decide(in)
	if d.Access || d.Reasons[0] != ReasonAccountArchived {
		t.Fatalf("got %+v", d)
	}
}

func TestDecideProxyPaused(t *testing.T) {
	in := base()
	in.ProxyEnabled = false
	d := Decide(in)
	if d.Access || d.ProxyAccess != ProxyPausedByAdmin || d.Reasons[0] != ReasonProxyPaused {
		t.Fatalf("got %+v", d)
	}
}

func TestDecideNoBinding(t *testing.T) {
	in := base()
	in.BindingPresent = false
	in.SubscriptionID = ""
	d := Decide(in)
	if d.Access || d.ProxyAccess != ProxyNoSubscription || d.Reasons[0] != ReasonNoBinding {
		t.Fatalf("got %+v", d)
	}
}

func TestDecideExpiredBinding(t *testing.T) {
	in := base()
	in.BindingExpiresAt = ptrTime(now.Add(-time.Second))
	d := Decide(in)
	if d.Access || d.ProxyAccess != ProxyExpired || d.Reasons[0] != ReasonBindingExpired {
		t.Fatalf("got %+v", d)
	}
	// exactly now is expired ([start,end) semantics)
	in.BindingExpiresAt = ptrTime(now)
	if d := Decide(in); d.Access {
		t.Fatalf("expiry boundary must be exclusive: %+v", d)
	}
}

func TestDecideSubscriptionPausedAndArchived(t *testing.T) {
	in := base()
	in.SubscriptionAccessEnabled = false
	d := Decide(in)
	if d.Access || d.Reasons[0] != ReasonSubscriptionPaused {
		t.Fatalf("got %+v", d)
	}
	in = base()
	in.SubscriptionArchived = true
	d = Decide(in)
	if d.Access || d.Reasons[0] != ReasonSubscriptionArchived {
		t.Fatalf("got %+v", d)
	}
}

func TestDecideOverQuota(t *testing.T) {
	in := base()
	in.UsedBytes = 1000
	d := Decide(in)
	if d.Access || d.ProxyAccess != ProxyOverQuota || d.Reasons[0] != ReasonOverQuota {
		t.Fatalf("got %+v", d)
	}
	// override above usage restores access and reports the override source
	in = base()
	in.UsedBytes = 5000
	in.OverrideQuotaEnabled = true
	in.OverrideQuotaBytes = ptr(9000)
	d = Decide(in)
	if !d.Access || d.QuotaSource != "override" || *d.EffectiveQuotaBytes != 9000 {
		t.Fatalf("got %+v", d)
	}
}

func TestDecideUnlimitedQuota(t *testing.T) {
	in := base()
	in.SubscriptionQuotaBytes = nil
	in.UsedBytes = 1 << 50
	d := Decide(in)
	if !d.Access || d.EffectiveQuotaBytes != nil {
		t.Fatalf("unlimited quota must allow: %+v", d)
	}
}

func TestDecideNoNodesAuthorized(t *testing.T) {
	in := base()
	in.AuthorizedGroupIDs = []string{}
	d := Decide(in)
	if !d.Access {
		t.Fatalf("member level access should remain true: %+v", d)
	}
	if len(d.AllowedNodeIDs) != 0 {
		t.Fatalf("allowed = %v", d.AllowedNodeIDs)
	}
	found := false
	for _, r := range d.Reasons {
		if r == ReasonNoNodesAuthorized {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %s in %v", ReasonNoNodesAuthorized, d.Reasons)
	}
}

func TestDecideStepOrder(t *testing.T) {
	d := Decide(base())
	want := []string{"account_status", "proxy_access", "binding_validity", "subscription_access", "quota", "node_authorization"}
	if len(d.Steps) != len(want) {
		t.Fatalf("steps = %+v", d.Steps)
	}
	for i, w := range want {
		if d.Steps[i].Step != w {
			t.Fatalf("step %d = %q want %q", i, d.Steps[i].Step, w)
		}
	}
}

func TestEffectiveQuotaSources(t *testing.T) {
	if q, src := EffectiveQuota(ptr(10), nil, true); q != nil || src != "override" {
		t.Fatalf("override unlimited: %v %s", q, src)
	}
	if q, src := EffectiveQuota(ptr(10), ptr(20), false); q == nil || *q != 10 || src != "subscription" {
		t.Fatalf("subscription default: %v %s", q, src)
	}
	if q, src := EffectiveQuota(nil, nil, false); q != nil || src != "subscription" {
		t.Fatalf("unlimited subscription: %v %s", q, src)
	}
}
