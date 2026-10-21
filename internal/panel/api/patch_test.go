package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"proxypanel/internal/panel/access"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/store"
)

// decodeInto runs the real request decoder so the tests cover the strict
// DisallowUnknownFields behaviour together with the nullable fields.
func decodeInto(t *testing.T, dst any, body string) error {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/x", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return decodeJSON(req, dst)
}

func int64Ptr(v int64) *int64        { return &v }
func intPtr(v int) *int              { return &v }
func strPtr(v string) *string        { return &v }
func timePtr(t time.Time) *time.Time { return &t }

func TestSubscriptionPatchDistinguishesOmittedNullAndValue(t *testing.T) {
	sub := &domain.Subscription{
		ID: "sub_1", Name: "base", Timezone: "Asia/Shanghai", AnchorDay: 1,
		QuotaBytes: int64Ptr(100), DefaultValidDays: intPtr(30), TemplateID: strPtr("tpl_1"),
	}

	// explicit null clears
	var in subscriptionInput
	if err := decodeInto(t, &in, `{"quota_bytes":null,"default_valid_days":null,"template_id":null}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !in.QuotaBytes.Set || !in.QuotaBytes.Null {
		t.Fatalf("quota_bytes null not detected: %+v", in.QuotaBytes)
	}
	if !in.DefaultValidDays.Set || !in.DefaultValidDays.Null {
		t.Fatalf("default_valid_days null not detected: %+v", in.DefaultValidDays)
	}
	if !in.TemplateID.Set || !in.TemplateID.Null {
		t.Fatalf("template_id null not detected: %+v", in.TemplateID)
	}
	if err := applySubscriptionPatch(sub, &in); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if sub.QuotaBytes != nil {
		t.Fatalf("quota must become unlimited, got %v", *sub.QuotaBytes)
	}
	if sub.DefaultValidDays != nil {
		t.Fatalf("default_valid_days must be cleared, got %v", *sub.DefaultValidDays)
	}
	if sub.TemplateID != nil {
		t.Fatalf("template_id must be cleared, got %v", *sub.TemplateID)
	}

	// explicit values are applied
	var withValues subscriptionInput
	if err := decodeInto(t, &withValues, `{"quota_bytes":123,"default_valid_days":7,"template_id":"tpl_2"}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := applySubscriptionPatch(sub, &withValues); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if sub.QuotaBytes == nil || *sub.QuotaBytes != 123 {
		t.Fatalf("quota = %v", sub.QuotaBytes)
	}
	if sub.DefaultValidDays == nil || *sub.DefaultValidDays != 7 {
		t.Fatalf("default_valid_days = %v", sub.DefaultValidDays)
	}
	if sub.TemplateID == nil || *sub.TemplateID != "tpl_2" {
		t.Fatalf("template_id = %v", sub.TemplateID)
	}

	// omitted keeps the current value
	var omitted subscriptionInput
	if err := decodeInto(t, &omitted, `{"description":"renamed"}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if omitted.QuotaBytes.Set || omitted.DefaultValidDays.Set || omitted.TemplateID.Set {
		t.Fatal("omitted fields must not be marked as set")
	}
	if err := applySubscriptionPatch(sub, &omitted); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if sub.QuotaBytes == nil || *sub.QuotaBytes != 123 {
		t.Fatalf("omitted quota must be untouched, got %v", sub.QuotaBytes)
	}
	if sub.Description != "renamed" {
		t.Fatalf("description = %q", sub.Description)
	}
}

func TestSubscriptionPatchRejectsUnknownInvalidsAndKeepsStrictDecoding(t *testing.T) {
	sub := &domain.Subscription{Name: "x", Timezone: "Asia/Shanghai", AnchorDay: 1}

	var in subscriptionInput
	if err := decodeInto(t, &in, `{"quota_byte":1}`); err == nil {
		t.Fatal("unknown fields must still be rejected")
	} else if !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unexpected error: %v", err)
	}

	var negative subscriptionInput
	if err := decodeInto(t, &negative, `{"quota_bytes":-1}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := applySubscriptionPatch(sub, &negative); err == nil {
		t.Fatal("negative quota must be rejected")
	}

	var zeroDays subscriptionInput
	if err := decodeInto(t, &zeroDays, `{"default_valid_days":0}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := applySubscriptionPatch(sub, &zeroDays); err == nil {
		t.Fatal("default_valid_days 0 must be rejected")
	}

	var emptyName subscriptionInput
	if err := decodeInto(t, &emptyName, `{"name":"   "}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := applySubscriptionPatch(sub, &emptyName); err == nil {
		t.Fatal("blank name must be rejected")
	}

	var blankTemplate subscriptionInput
	if err := decodeInto(t, &blankTemplate, `{"template_id":""}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := applySubscriptionPatch(sub, &blankTemplate); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if sub.TemplateID != nil {
		t.Fatalf("blank template id must clear the template, got %v", *sub.TemplateID)
	}
}

func TestSubscriptionAffectsMembersCountsExplicitNull(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{`{"quota_bytes":null}`, true},
		{`{"default_valid_days":null}`, true},
		// template_id only affects presentation, so it does not force the
		// authorization preview flow - but the null is still applied.
		{`{"template_id":null}`, false},
		{`{"quota_bytes":10}`, true},
		{`{"timezone":"UTC"}`, true},
		{`{"node_group_ids":[]}`, true},
		{`{"access_enabled":false}`, true},
		{`{"description":"only a description"}`, false},
		{`{"name":"only a name"}`, false},
		{`{}`, false},
	}
	for _, tc := range cases {
		var in subscriptionInput
		if err := decodeInto(t, &in, tc.body); err != nil {
			t.Fatalf("%s: decode: %v", tc.body, err)
		}
		if got := in.affectsMembers(); got != tc.want {
			t.Fatalf("%s: affectsMembers = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func memberFixture() *domain.Member {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	return &domain.Member{
		ID: "mbr_1", Username: "alice", Role: "member", Active: true, ProxyEnabled: true,
		SubscriptionID: strPtr("sub_1"), ExpiresAt: timePtr(end),
		QuotaOverrideEnabled: true, QuotaOverrideBytes: int64Ptr(2048),
		PeriodStart: &start, PeriodEnd: &end, PeriodTimezone: "Asia/Shanghai", PeriodAnchorDay: 1,
		OverQuota: true,
	}
}

func TestMemberPatchClearsExpiryOverrideAndBinding(t *testing.T) {
	m := memberFixture()
	var in memberPatchInput
	if err := decodeInto(t, &in, `{"expires_at":null}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !in.ExpiresAt.Set || !in.ExpiresAt.Null {
		t.Fatal("expires_at null not detected")
	}
	if _, err := applyMemberPatch(m, &in); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if m.ExpiresAt != nil {
		t.Fatalf("expires_at must be cleared, got %v", m.ExpiresAt)
	}

	m = memberFixture()
	var override memberPatchInput
	if err := decodeInto(t, &override, `{"quota_override_bytes":null}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := applyMemberPatch(m, &override); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if m.QuotaOverrideBytes != nil || m.QuotaOverrideEnabled {
		t.Fatalf("override must be cleared: %v %v", m.QuotaOverrideBytes, m.QuotaOverrideEnabled)
	}

	m = memberFixture()
	var value memberPatchInput
	if err := decodeInto(t, &value, `{"quota_override_bytes":4096}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := applyMemberPatch(m, &value); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if m.QuotaOverrideBytes == nil || *m.QuotaOverrideBytes != 4096 || !m.QuotaOverrideEnabled {
		t.Fatalf("override value = %v/%v", m.QuotaOverrideBytes, m.QuotaOverrideEnabled)
	}

	m = memberFixture()
	var unbind memberPatchInput
	if err := decodeInto(t, &unbind, `{"subscription_id":null}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := applyMemberPatch(m, &unbind); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if m.SubscriptionID != nil || m.PeriodStart != nil || m.PeriodEnd != nil ||
		m.PeriodTimezone != "" || m.PeriodAnchorDay != 0 || m.PendingPeriodRule != nil || m.OverQuota {
		t.Fatalf("unbind must clear the cycle: %+v", m)
	}

	m = memberFixture()
	var emptySub memberPatchInput
	if err := decodeInto(t, &emptySub, `{"subscription_id":""}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := applyMemberPatch(m, &emptySub); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if m.SubscriptionID != nil {
		t.Fatal("empty subscription_id must unbind")
	}

	m = memberFixture()
	var expiryValue memberPatchInput
	if err := decodeInto(t, &expiryValue, `{"expires_at":"2027-01-01T00:00:00Z"}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := applyMemberPatch(m, &expiryValue); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if m.ExpiresAt == nil || !m.ExpiresAt.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("expires_at = %v", m.ExpiresAt)
	}
}

func TestMemberPatchOmittedFieldsAndValidation(t *testing.T) {
	m := memberFixture()
	before := *m
	var omitted memberPatchInput
	if err := decodeInto(t, &omitted, `{"active":true}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if omitted.ExpiresAt.Set || omitted.QuotaOverride.Set || omitted.SubscriptionID.Set {
		t.Fatal("omitted nullable fields must not be marked as set")
	}
	deactivated, err := applyMemberPatch(m, &omitted)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if deactivated {
		t.Fatal("active:true must not report a deactivation")
	}
	if m.ExpiresAt == nil || !m.ExpiresAt.Equal(*before.ExpiresAt) {
		t.Fatal("omitted expires_at must not change the value")
	}
	if m.SubscriptionID == nil || *m.SubscriptionID != "sub_1" {
		t.Fatal("omitted subscription_id must not unbind")
	}

	var deactivate memberPatchInput
	if err := decodeInto(t, &deactivate, `{"active":false}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, err := applyMemberPatch(m, &deactivate); err != nil || !got {
		t.Fatalf("deactivation must be reported: %v %v", got, err)
	}

	var badRole memberPatchInput
	if err := decodeInto(t, &badRole, `{"role":"root"}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := applyMemberPatch(m, &badRole); err == nil {
		t.Fatal("invalid role must be rejected")
	}

	var negative memberPatchInput
	if err := decodeInto(t, &negative, `{"quota_override_bytes":-5}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := applyMemberPatch(m, &negative); err == nil {
		t.Fatal("negative override must be rejected")
	}

	enabledOnly := memberFixture()
	enabledOnly.QuotaOverrideBytes = nil
	var enableNoValue memberPatchInput
	if err := decodeInto(t, &enableNoValue, `{"quota_override_enabled":true}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := applyMemberPatch(enabledOnly, &enableNoValue); err == nil {
		t.Fatal("an enabled override without a value must be rejected")
	}

	var blankUser memberPatchInput
	if err := decodeInto(t, &blankUser, `{"username":"  "}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := applyMemberPatch(m, &blankUser); err == nil {
		t.Fatal("blank username must be rejected")
	}
}

func TestServerPatchNullSemantics(t *testing.T) {
	expiry := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	srv := &domain.Server{ID: "srv_1", Name: "tokyo", ExpiresAt: &expiry, CostMinor: int64Ptr(500)}

	var clear serverPatchInput
	if err := decodeInto(t, &clear, `{"expires_at":null,"cost_minor":null}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := applyServerPatch(srv, &clear); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if srv.ExpiresAt != nil || srv.CostMinor != nil {
		t.Fatalf("null must clear both values: %v %v", srv.ExpiresAt, srv.CostMinor)
	}

	var set serverPatchInput
	if err := decodeInto(t, &set, `{"expires_at":"2027-02-01T00:00:00Z","cost_minor":900}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := applyServerPatch(srv, &set); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if srv.ExpiresAt == nil || !srv.ExpiresAt.Equal(time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("expires_at = %v", srv.ExpiresAt)
	}
	if srv.CostMinor == nil || *srv.CostMinor != 900 {
		t.Fatalf("cost_minor = %v", srv.CostMinor)
	}

	var omitted serverPatchInput
	if err := decodeInto(t, &omitted, `{"region":"jp"}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if omitted.ExpiresAt.Set || omitted.CostMinor.Set {
		t.Fatal("omitted fields must not be marked as set")
	}
	if err := applyServerPatch(srv, &omitted); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if srv.ExpiresAt == nil || srv.CostMinor == nil {
		t.Fatal("omitted fields must stay untouched")
	}

	var negative serverPatchInput
	if err := decodeInto(t, &negative, `{"cost_minor":-1}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := applyServerPatch(srv, &negative); err == nil {
		t.Fatal("negative cost must be rejected")
	}

	var emptyName serverPatchInput
	if err := decodeInto(t, &emptyName, `{"name":""}`); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := applyServerPatch(srv, &emptyName); err == nil {
		t.Fatal("blank name must be rejected")
	}
}

func TestUsageWindowParsing(t *testing.T) {
	parse := func(query string) (store.UsageFilter, error) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/usage"+query, nil)
		return usageWindow(req)
	}

	filter, err := parse("?from=2026-10-01T00:00:00Z&to=2026-11-01T00:00:00Z&member_id=mbr_1&node_id=nd_1")
	if err != nil {
		t.Fatalf("valid window: %v", err)
	}
	if filter.From == nil || !filter.From.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("from = %v", filter.From)
	}
	if filter.To == nil || !filter.To.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("to = %v", filter.To)
	}
	if filter.MemberID != "mbr_1" || filter.NodeID != "nd_1" {
		t.Fatalf("filters = %+v", filter)
	}

	if filter, err := parse("?from=2026-10-01T00:00:00.500Z"); err != nil || filter.From == nil {
		t.Fatalf("RFC3339Nano: %v %v", filter.From, err)
	}
	if filter, err := parse("?from=2026-10-01"); err != nil || filter.From == nil || filter.From.Hour() != 0 {
		t.Fatalf("date-only: %v %v", filter.From, err)
	}

	_, err = parse("?from=2026-13-40")
	if err == nil {
		t.Fatal("invalid date must be rejected")
	}
	de := domain.AsError(err)
	if de.Status != http.StatusBadRequest || de.Code != domain.CodeValidationFailed {
		t.Fatalf("invalid date error = %d %s", de.Status, de.Code)
	}
	if _, ok := de.Details["field_errors"].(map[string]string)["from"]; !ok {
		t.Fatalf("field_errors missing: %v", de.Details)
	}

	_, err = parse("?from=2026-11-01T00:00:00Z&to=2026-10-01T00:00:00Z")
	if err == nil {
		t.Fatal("from after to must be rejected")
	}
	if fields, ok := domain.AsError(err).Details["field_errors"].(map[string]string); !ok || fields["to"] == "" {
		t.Fatalf("expected a to field error: %v", domain.AsError(err).Details)
	}

	filter, err = parse("?limit=50")
	if err != nil || filter.Limit != 50 {
		t.Fatalf("limit pass-through = %+v %v", filter, err)
	}
	if _, err := parse("?limit=abc"); err != nil {
		t.Fatalf("non numeric limit must be ignored, not rejected: %v", err)
	}
}

func TestDecorateMemberSetsFlatDerivedValues(t *testing.T) {
	quota := int64(4096)
	m := &domain.Member{ID: "mbr_1", Username: "alice"}
	res := &store.AccessResult{
		Decision: access.Decision{
			AlgorithmVersion:    access.AlgorithmVersion,
			AllowedNodeIDs:      []string{"nd_1", "nd_2"},
			Reasons:             []string{access.ReasonOK},
			ProxyAccess:         access.ProxyEnabled,
			EffectiveQuotaBytes: &quota,
		},
		Evidence: &store.Evidence{Member: m, UsedBytes: 512},
	}
	decorateMember(m, res)
	if m.UsedBytes != 512 || m.EffectiveQuotaBytes == nil || *m.EffectiveQuotaBytes != 4096 {
		t.Fatalf("derived usage/quota = %d %v", m.UsedBytes, m.EffectiveQuotaBytes)
	}
	if m.ProxyAccess != access.ProxyEnabled || m.OverQuota {
		t.Fatalf("proxy access = %s over=%v", m.ProxyAccess, m.OverQuota)
	}
	if len(m.AllowedNodeIDs) != 2 || len(m.AccessReasons) != 1 {
		t.Fatalf("lists = %v %v", m.AllowedNodeIDs, m.AccessReasons)
	}

	// A nil result must still emit arrays, never null.
	empty := &domain.Member{ID: "mbr_2"}
	decorateMember(empty, nil)
	raw, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"access_reasons":[]`, `"allowed_node_ids":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("member JSON missing %s: %s", want, raw)
		}
	}

	over := &domain.Member{ID: "mbr_3"}
	decorateMember(over, &store.AccessResult{
		Decision: access.Decision{ProxyAccess: access.ProxyOverQuota, Reasons: []string{access.ReasonOverQuota}},
		Evidence: &store.Evidence{UsedBytes: 10},
	})
	if !over.OverQuota {
		t.Fatalf("over quota flag not derived: %+v", over)
	}
}

func TestValidatePreviewValuesKeepsExplicitNulls(t *testing.T) {
	values := domain.NewObject()
	if err := values.Set("id", "sub_1"); err != nil {
		t.Fatal(err)
	}
	patch := domain.NewObject()
	if err := patch.Set("quota_bytes", nil); err != nil {
		t.Fatal(err)
	}
	if err := patch.Set("default_valid_days", nil); err != nil {
		t.Fatal(err)
	}
	if err := patch.Set("template_id", nil); err != nil {
		t.Fatal(err)
	}
	if err := values.Set("patch", patch); err != nil {
		t.Fatal(err)
	}
	if err := validatePreviewValues(store.OpUpdateSubscription, values); err != nil {
		t.Fatalf("null patch values must pass through: %v", err)
	}

	missingPatch := domain.NewObject()
	_ = missingPatch.Set("id", "sub_1")
	if err := validatePreviewValues(store.OpUpdateSubscription, missingPatch); err == nil {
		t.Fatal("missing values.patch must be rejected")
	}

	missingID := domain.NewObject()
	_ = missingID.Set("patch", domain.NewObject())
	if err := validatePreviewValues(store.OpUpdateNodeGroup, missingID); err == nil {
		t.Fatal("missing values.id must be rejected")
	}

	badPatch := domain.NewObject()
	_ = badPatch.Set("id", "ng_1")
	_ = badPatch.Set("patch", []string{"not", "an", "object"})
	if err := validatePreviewValues(store.OpUpdateNodeGroup, badPatch); err == nil {
		t.Fatal("non object patch must be rejected")
	}

	sub := domain.NewObject()
	_ = sub.Set("subscription_id", nil)
	if err := validatePreviewValues(store.OpAssignSubscription, sub); err == nil {
		t.Fatal("null subscription_id must be rejected for assign_subscription")
	}

	active := domain.NewObject()
	_ = active.Set("active", false)
	if err := validatePreviewValues(store.OpSetActive, active); err != nil {
		t.Fatalf("active:false must be accepted: %v", err)
	}
	if err := validatePreviewValues(store.OpSetActive, domain.NewObject()); err == nil {
		t.Fatal("missing active must be rejected")
	}

	expiry := domain.NewObject()
	_ = expiry.Set("expires_at", nil)
	if err := validatePreviewValues(store.OpSetExpiry, expiry); err != nil {
		t.Fatalf("expires_at null must be accepted (clears): %v", err)
	}
	if err := validatePreviewValues(store.OpSetExpiry, domain.NewObject()); err == nil {
		t.Fatal("missing expires_at must be rejected")
	}
}
