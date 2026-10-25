package store

import (
	"testing"

	"proxypanel/internal/panel/domain"
)

func TestDigestMembersIsOrderInsensitiveAndVersioned(t *testing.T) {
	a := []memberStamp{
		{MemberID: "mbr_a", MemberVersion: 2, SubscriptionID: "sub_1", SubscriptionVersion: 3},
		{MemberID: "mbr_b", MemberVersion: 1, SubscriptionID: "sub_1", SubscriptionVersion: 3},
	}
	b := []memberStamp{a[1], a[0]}
	if digestMembers(a) != digestMembers(b) {
		t.Fatal("digest must not depend on ordering")
	}
	if digestMembers(a) == digestMembers([]memberStamp{
		{MemberID: "mbr_a", MemberVersion: 3, SubscriptionID: "sub_1", SubscriptionVersion: 3},
		{MemberID: "mbr_b", MemberVersion: 1, SubscriptionID: "sub_1", SubscriptionVersion: 3},
	}) {
		t.Fatal("a member version change must change the digest")
	}
	if digestMembers(a) == digestMembers([]memberStamp{
		{MemberID: "mbr_a", MemberVersion: 2, SubscriptionID: "sub_2", SubscriptionVersion: 3},
		{MemberID: "mbr_b", MemberVersion: 1, SubscriptionID: "sub_1", SubscriptionVersion: 3},
	}) {
		t.Fatal("a subscription change must change the digest")
	}
	if digestMembers(nil) == digestMembers(a) {
		t.Fatal("an empty set must not collide with a populated set")
	}
}

func TestRequestFingerprintBindsPreviewAndConfirmation(t *testing.T) {
	base := requestFingerprint("prv_1", true)
	if base != requestFingerprint("prv_1", true) {
		t.Fatal("fingerprint must be stable")
	}
	if base == requestFingerprint("prv_2", true) {
		t.Fatal("different previews must have different fingerprints")
	}
	if base == requestFingerprint("prv_1", false) {
		t.Fatal("confirmation state must change the fingerprint")
	}
}

func TestDiffNodes(t *testing.T) {
	added, removed := diffNodes([]string{"a", "b"}, []string{"b", "c"})
	if added != 1 || removed != 1 {
		t.Fatalf("diffNodes = %d/%d", added, removed)
	}
	added, removed = diffNodes(nil, []string{"a"})
	if added != 1 || removed != 0 {
		t.Fatalf("diffNodes from empty = %d/%d", added, removed)
	}
}

func TestApplySubscriptionPatchObjectValidation(t *testing.T) {
	sub := &domain.Subscription{Name: "x", Timezone: "Asia/Shanghai", AnchorDay: 1}
	patch := domain.NewObject()
	_ = patch.Set("anchor_day", 0)
	if err := applySubscriptionPatchObject(sub, patch); err == nil {
		t.Fatal("anchor_day 0 must be rejected")
	}
	patch = domain.NewObject()
	_ = patch.Set("timezone", "Mars/Olympus")
	if err := applySubscriptionPatchObject(sub, patch); err == nil {
		t.Fatal("unknown timezone must be rejected")
	}
	patch = domain.NewObject()
	_ = patch.Set("quota_bytes", -1)
	if err := applySubscriptionPatchObject(sub, patch); err == nil {
		t.Fatal("negative quota must be rejected")
	}
	patch = domain.NewObject()
	_ = patch.Set("quota_bytes", nil)
	if err := applySubscriptionPatchObject(sub, patch); err != nil {
		t.Fatalf("null quota (unlimited) must be accepted: %v", err)
	}
	if sub.QuotaBytes != nil {
		t.Fatal("null quota must mean unlimited")
	}
}

func TestOperationConstantsMatchContract(t *testing.T) {
	want := map[string]bool{
		"assign_subscription": true, "set_active": true, "set_expiry": true,
		"clear_override": true, "update_subscription": true, "update_node_group": true,
	}
	for _, op := range []string{OpAssignSubscription, OpSetActive, OpSetExpiry, OpClearOverride,
		OpUpdateSubscription, OpUpdateNodeGroup} {
		if !want[op] {
			t.Fatalf("unexpected operation constant %q", op)
		}
	}
	if len(want) != 6 {
		t.Fatalf("expected 6 operations, got %d", len(want))
	}
}
