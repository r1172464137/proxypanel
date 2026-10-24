package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"proxypanel/internal/panel/access"
	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/ids"
	"proxypanel/internal/panel/period"
	"proxypanel/internal/panel/secrets"
	"proxypanel/internal/panel/store"
)

func (s *Server) handleListNodeGroups(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminPage(r, "node-groups")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := s.Store.ListAdminNodeGroupsPage(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeAdminPage(s, w, r, "node-groups", p, page)
}

func (s *Server) handleCreateNodeGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		NodeIDs     []string `json:"node_ids"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	v, err := s.Store.CreateNodeGroup(r.Context(), &domain.NodeGroup{ID: newID(ids.NodeGroup),
		Name: in.Name, Description: in.Description, NodeIDs: in.NodeIDs})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "node_group.create", "node_group", v.ID, nil)
	s.writeData(w, http.StatusCreated, v)
}

func (s *Server) handleGetNodeGroup(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.GetNodeGroup(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, v)
}

func (s *Server) handlePatchNodeGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version     int64     `json:"version"`
		Name        *string   `json:"name"`
		Description *string   `json:"description"`
		NodeIDs     *[]string `json:"node_ids"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := requireVersion(in.Version); err != nil {
		s.writeError(w, r, err)
		return
	}
	if in.NodeIDs != nil {
		used, err := s.Store.NodeGroupUsage(r.Context(), r.PathValue("id"))
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if used > 0 {
			previewRequired(w, r, "node group is referenced by "+itoa(used)+" subscription(s)")
			return
		}
	}
	v, err := s.Store.UpdateNodeGroup(r.Context(), r.PathValue("id"), in.Version, func(g *domain.NodeGroup) error {
		if in.Name != nil {
			g.Name = *in.Name
		}
		if in.Description != nil {
			g.Description = *in.Description
		}
		if in.NodeIDs != nil {
			g.NodeIDs = *in.NodeIDs
		}
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "node_group.update", "node_group", v.ID, nil)
	s.writeData(w, http.StatusOK, v)
}

func (s *Server) handleDeleteNodeGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Store.DeleteNodeGroup(r.Context(), id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "node_group.delete", "node_group", id, nil)
	s.writeData(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

// nullable distinguishes "field omitted" from "explicit JSON null" for fields
// whose zero value is meaningful. A plain pointer collapses both cases to nil,
// which made PATCH bodies that clear a value silently do nothing.
type nullable[T any] struct {
	Set   bool
	Null  bool
	Value T
}

func (n *nullable[T]) UnmarshalJSON(raw []byte) error {
	n.Set = true
	if string(bytes.TrimSpace(raw)) == "null" {
		n.Null = true
		var zero T
		n.Value = zero
		return nil
	}
	return json.Unmarshal(raw, &n.Value)
}

type subscriptionInput struct {
	Version          int64            `json:"version"`
	Name             *string          `json:"name"`
	Description      *string          `json:"description"`
	NodeGroupIDs     *[]string        `json:"node_group_ids"`
	QuotaBytes       nullable[int64]  `json:"quota_bytes"`
	Timezone         *string          `json:"timezone"`
	AnchorDay        *int             `json:"anchor_day"`
	DefaultValidDays nullable[int]    `json:"default_valid_days"`
	AllowNewBindings *bool            `json:"allow_new_bindings"`
	AccessEnabled    *bool            `json:"access_enabled"`
	TemplateID       nullable[string] `json:"template_id"`
	Archived         *bool            `json:"archived"`
}

// affectsMembers reports whether the patch changes anything members inherit.
// An explicit null counts as a change: it resets the value (unlimited quota,
// no default term, no template), so the preview flow is required.
func (in *subscriptionInput) affectsMembers() bool {
	return in.NodeGroupIDs != nil || in.QuotaBytes.Set || in.Timezone != nil ||
		in.AnchorDay != nil || in.DefaultValidDays.Set || in.AccessEnabled != nil || in.Archived != nil
}

// applySubscriptionPatch applies the writable subscription fields. Omitted
// fields stay untouched; an explicit null clears the nullable ones.
func applySubscriptionPatch(v *domain.Subscription, in *subscriptionInput) error {
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			return domain.Validation("name must not be empty").
				WithFieldErrors(map[string]string{"name": "must not be empty"})
		}
		v.Name = *in.Name
	}
	if in.Description != nil {
		v.Description = *in.Description
	}
	if in.NodeGroupIDs != nil {
		v.NodeGroupIDs = *in.NodeGroupIDs
	}
	if in.QuotaBytes.Set {
		if in.QuotaBytes.Null {
			v.QuotaBytes = nil // null = unlimited
		} else {
			if in.QuotaBytes.Value < 0 {
				return domain.Validation("quota_bytes must not be negative").
					WithFieldErrors(map[string]string{"quota_bytes": "must not be negative"})
			}
			quota := in.QuotaBytes.Value
			v.QuotaBytes = &quota
		}
	}
	if in.Timezone != nil {
		v.Timezone = *in.Timezone
	}
	if in.AnchorDay != nil {
		v.AnchorDay = *in.AnchorDay
	}
	if in.DefaultValidDays.Set {
		if in.DefaultValidDays.Null {
			v.DefaultValidDays = nil // no default term: bound members keep their own expiry
		} else {
			if in.DefaultValidDays.Value <= 0 {
				return domain.Validation("default_valid_days must be a positive integer or null").
					WithFieldErrors(map[string]string{"default_valid_days": "must be positive or null"})
			}
			days := in.DefaultValidDays.Value
			v.DefaultValidDays = &days
		}
	}
	if in.AllowNewBindings != nil {
		v.AllowNewBindings = *in.AllowNewBindings
	}
	if in.AccessEnabled != nil {
		v.AccessEnabled = *in.AccessEnabled
	}
	if in.TemplateID.Set {
		if in.TemplateID.Null || strings.TrimSpace(in.TemplateID.Value) == "" {
			v.TemplateID = nil
		} else {
			id := strings.TrimSpace(in.TemplateID.Value)
			v.TemplateID = &id
		}
	}
	if in.Archived != nil {
		v.Archived = *in.Archived
	}
	if _, err := period.LoadTimezone(v.Timezone); err != nil {
		return domain.Validation("%s", err.Error())
	}
	if v.AnchorDay < 1 || v.AnchorDay > 31 {
		return domain.Validation("anchor_day must be between 1 and 31")
	}
	return nil
}

func (s *Server) handleListSubscriptions(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminPage(r, "subscriptions")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := s.Store.ListAdminSubscriptionsPage(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeAdminPage(s, w, r, "subscriptions", p, page)
}

func (s *Server) handleCreateSubscription(w http.ResponseWriter, r *http.Request) {
	var in subscriptionInput
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	v := &domain.Subscription{ID: newID(ids.Subscription), Timezone: "Asia/Shanghai", AnchorDay: 1,
		AllowNewBindings: true, AccessEnabled: true}
	if err := applySubscriptionPatch(v, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	created, err := s.Store.CreateSubscription(r.Context(), v)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "subscription.create", "subscription", created.ID, nil)
	s.writeData(w, http.StatusCreated, created)
}

func (s *Server) handleGetSubscription(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.GetSubscription(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, v)
}

func (s *Server) handlePatchSubscription(w http.ResponseWriter, r *http.Request) {
	var in subscriptionInput
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := requireVersion(in.Version); err != nil {
		s.writeError(w, r, err)
		return
	}
	cur, err := s.Store.GetSubscription(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if cur.BoundMembers > 0 && in.affectsMembers() {
		previewRequired(w, r, "subscription has "+itoa(cur.BoundMembers)+" bound member(s)")
		return
	}
	v, err := s.Store.UpdateSubscription(r.Context(), r.PathValue("id"), in.Version, func(v *domain.Subscription) error {
		oldAnchor, oldTZ := v.AnchorDay, v.Timezone
		if err := applySubscriptionPatch(v, &in); err != nil {
			return err
		}
		if v.AnchorDay != oldAnchor || v.Timezone != oldTZ {
			now := time.Now().UTC()
			v.PendingPeriodRule = &domain.PeriodRule{AnchorDay: v.AnchorDay, Timezone: v.Timezone, SubmittedAt: &now}
		}
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "subscription.update", "subscription", v.ID, nil)
	s.writeData(w, http.StatusOK, v)
}

func (s *Server) handleDeleteSubscription(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := s.Store.DeleteSubscription(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "subscription.archive", "subscription", id, nil)
	// retired is a DELETE result, not a persisted/GET subscription field.
	s.writeData(w, http.StatusOK, struct {
		*domain.Subscription
		Retired bool `json:"retired"`
	}{v, v.Archived})
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminPage(r, "members")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := s.Store.ListAdminMembersPage(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// The list keeps the flat member shape (no nesting change): every row also
	// carries the derived values computed by the single authorization algorithm
	// so the UI never re-derives quota or access itself.
	for i := range page.Items {
		res, err := s.Store.EvaluateAccessForMember(r.Context(), &page.Items[i])
		if err != nil {
			decorateMember(&page.Items[i], nil)
			continue
		}
		decorateMember(&page.Items[i], res)
	}
	writeAdminPage(s, w, r, "members", p, page)
}

// decorateMember copies decision values onto a member row. Slice fields are
// always non-nil so the JSON contract emits [] instead of null.
func decorateMember(m *domain.Member, res *store.AccessResult) {
	m.AccessReasons = []string{}
	m.AllowedNodeIDs = []string{}
	if res == nil {
		m.ProxyAccess = "unknown"
		return
	}
	m.UsedBytes = res.Evidence.UsedBytes
	m.EffectiveQuotaBytes = res.Decision.EffectiveQuotaBytes
	if len(res.Decision.Reasons) > 0 {
		m.AccessReasons = append(m.AccessReasons, res.Decision.Reasons...)
	}
	if len(res.Decision.AllowedNodeIDs) > 0 {
		m.AllowedNodeIDs = append(m.AllowedNodeIDs, res.Decision.AllowedNodeIDs...)
	}
	m.ProxyAccess = res.Decision.ProxyAccess
	m.OverQuota = res.Decision.ProxyAccess == access.ProxyOverQuota
}

func (s *Server) handleCreateMember(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username       string     `json:"username"`
		Password       string     `json:"password"`
		Role           string     `json:"role"`
		Active         *bool      `json:"active"`
		ProxyEnabled   *bool      `json:"proxy_enabled"`
		SubscriptionID *string    `json:"subscription_id"`
		ExpiresAt      *time.Time `json:"expires_at"`
		QuotaOverride  *int64     `json:"quota_override_bytes"`
		QuotaEnabled   *bool      `json:"quota_override_enabled"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	hash, err := secrets.HashPassword(in.Password)
	if err != nil {
		s.writeError(w, r, domain.Validation("%s", err.Error()))
		return
	}
	role := in.Role
	if role == "" {
		role = "member"
	}
	if role != "admin" && role != "member" {
		s.writeError(w, r, domain.Validation("role must be admin or member"))
		return
	}
	m := &domain.Member{ID: newID(ids.Member), Username: strings.TrimSpace(in.Username),
		PasswordHash: hash, Role: role, Active: true, ProxyEnabled: true,
		SubscriptionID: in.SubscriptionID, ExpiresAt: in.ExpiresAt, QuotaOverrideBytes: in.QuotaOverride}
	if in.Active != nil {
		m.Active = *in.Active
	}
	if in.ProxyEnabled != nil {
		m.ProxyEnabled = *in.ProxyEnabled
	}
	if in.QuotaEnabled != nil {
		m.QuotaOverrideEnabled = *in.QuotaEnabled
	}
	if m.QuotaOverrideEnabled && m.QuotaOverrideBytes == nil {
		s.writeError(w, r, domain.Validation("quota_override_bytes is required when quota_override_enabled is true").
			WithFieldErrors(map[string]string{"quota_override_bytes": "required when the override is enabled"}))
		return
	}
	if err := s.prepareBinding(r, m); err != nil {
		s.writeError(w, r, err)
		return
	}
	created, err := s.Store.CreateMember(r.Context(), m)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.syncCredentials(r, created.ID); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "member.create", "member", created.ID, nil)
	s.writeData(w, http.StatusCreated, created)
}

func (s *Server) handleGetMember(w http.ResponseWriter, r *http.Request) {
	res, err := s.Store.ProductStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, map[string]any{"member": res.Evidence.Member, "access": res.Decision})
}

// memberPatchInput separates omitted fields from explicit nulls so PATCH can
// clear a personal expiry, a quota override or the subscription binding.
type memberPatchInput struct {
	Version        int64               `json:"version"`
	Username       *string             `json:"username"`
	Password       *string             `json:"password"`
	Role           *string             `json:"role"`
	Active         *bool               `json:"active"`
	ProxyEnabled   *bool               `json:"proxy_enabled"`
	SubscriptionID nullable[string]    `json:"subscription_id"`
	ExpiresAt      nullable[time.Time] `json:"expires_at"`
	QuotaOverride  nullable[int64]     `json:"quota_override_bytes"`
	QuotaEnabled   *bool               `json:"quota_override_enabled"`
}

// applyMemberPatch mutates the member row. The result reports whether the
// account was deactivated (which requires revoking its sessions).
func applyMemberPatch(m *domain.Member, in *memberPatchInput) (bool, error) {
	if in.Username != nil {
		name := strings.TrimSpace(*in.Username)
		if name == "" {
			return false, domain.Validation("username must not be empty").
				WithFieldErrors(map[string]string{"username": "must not be empty"})
		}
		m.Username = name
	}
	if in.Password != nil {
		hash, err := secrets.HashPassword(*in.Password)
		if err != nil {
			return false, domain.Validation("%s", err.Error())
		}
		m.PasswordHash = hash
	}
	if in.Role != nil {
		if *in.Role != "admin" && *in.Role != "member" {
			return false, domain.Validation("role must be admin or member")
		}
		m.Role = *in.Role
	}
	if in.Active != nil {
		m.Active = *in.Active
	}
	if in.ProxyEnabled != nil {
		m.ProxyEnabled = *in.ProxyEnabled
	}
	if in.QuotaOverride.Set {
		if in.QuotaOverride.Null {
			// Explicit null restores inheritance (product.md §5.3).
			m.QuotaOverrideBytes = nil
			m.QuotaOverrideEnabled = false
		} else {
			if in.QuotaOverride.Value < 0 {
				return false, domain.Validation("quota_override_bytes must not be negative").
					WithFieldErrors(map[string]string{"quota_override_bytes": "must not be negative"})
			}
			value := in.QuotaOverride.Value
			m.QuotaOverrideBytes = &value
			m.QuotaOverrideEnabled = true
		}
	}
	if in.QuotaEnabled != nil {
		m.QuotaOverrideEnabled = *in.QuotaEnabled
		if !m.QuotaOverrideEnabled {
			m.QuotaOverrideBytes = nil
		}
	}
	if m.QuotaOverrideEnabled && m.QuotaOverrideBytes == nil {
		// An enabled override without a value would silently mean "unlimited";
		// require the operator to state it explicitly.
		return false, domain.Validation("quota_override_bytes is required when quota_override_enabled is true").
			WithFieldErrors(map[string]string{"quota_override_bytes": "required when the override is enabled"})
	}
	if in.ExpiresAt.Set {
		if in.ExpiresAt.Null {
			m.ExpiresAt = nil // cleared: no personal expiry
		} else {
			expiry := in.ExpiresAt.Value.UTC()
			m.ExpiresAt = &expiry
		}
	}
	if in.SubscriptionID.Set {
		id := strings.TrimSpace(in.SubscriptionID.Value)
		if in.SubscriptionID.Null || id == "" {
			// Unbinding drops the cycle owned by the previous subscription; the
			// usage ledger keeps the history.
			m.SubscriptionID = nil
			m.PeriodStart = nil
			m.PeriodEnd = nil
			m.PeriodTimezone = ""
			m.PeriodAnchorDay = 0
			m.PendingPeriodRule = nil
			m.OverQuota = false
		} else {
			m.SubscriptionID = &id
		}
	}
	return in.Active != nil && !*in.Active, nil
}

func (s *Server) handlePatchMember(w http.ResponseWriter, r *http.Request) {
	var in memberPatchInput
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := requireVersion(in.Version); err != nil {
		s.writeError(w, r, err)
		return
	}
	id := r.PathValue("id")
	// Binding to a concrete subscription is validated up front; explicit null
	// unbinds instead (see applyMemberPatch).
	if in.SubscriptionID.Set && !in.SubscriptionID.Null {
		if target := strings.TrimSpace(in.SubscriptionID.Value); target != "" {
			sub, err := s.Store.GetSubscription(r.Context(), target)
			if err != nil {
				s.writeError(w, r, err)
				return
			}
			if sub.Archived || !sub.AllowNewBindings {
				s.writeError(w, r, domain.Conflict(domain.CodeSubscriptionNotBindable,
					"subscription %q does not accept new bindings", sub.Name))
				return
			}
		}
	}
	v, err := s.Store.UpdateMember(r.Context(), id, in.Version, func(m *domain.Member) error {
		// Store.UpdateMember owns binding history and the unique cycle helper.
		_, err := applyMemberPatch(m, &in)
		return err
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.syncCredentials(r, v.ID); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "member.update", "member", v.ID, nil)
	s.writeData(w, http.StatusOK, v)
}

func (s *Server) handleDeleteMember(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := s.Store.UpdateMember(r.Context(), id, 0, func(m *domain.Member) error {
		m.Archived, m.Active, m.ProxyEnabled = true, false, false
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	_, _ = s.Store.PurgeCredentials(r.Context(), id, "member archived")
	s.audit(r, "member.archive", "member", id, nil)
	s.writeData(w, http.StatusOK, v)
}

func (s *Server) handleAdminTokenRotation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	plain := secrets.NewToken(32)
	if _, err := s.Store.RotateSubscriptionToken(r.Context(), id, secrets.HashToken(plain), plain); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "subscription_token.rotate", "member", id, nil)
	s.writeData(w, http.StatusOK, map[string]any{"token": plain, "url": s.subscriptionURL(plain)})
}

func (s *Server) handleAdminCredentialRotation(w http.ResponseWriter, r *http.Request) {
	s.rotateCredentials(w, r, r.PathValue("id"), memberFrom(r.Context()).Username)
}

func (s *Server) handleQuotaAdjustment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Bytes  int64  `json:"bytes"`
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		s.writeError(w, r, domain.Validation("reason is required"))
		return
	}
	id := r.PathValue("id")
	adj, err := s.Store.CreateQuotaAdjustment(r.Context(), id, in.Bytes, in.Reason, memberFrom(r.Context()).Username)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "quota_adjustment.create", "member", id, nil)
	s.writeData(w, http.StatusCreated, adj)
}

func (s *Server) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminPage(r, "templates")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := s.Store.ListAdminTemplatesPage(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeAdminPage(s, w, r, "templates", p, page)
}

func (s *Server) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string        `json:"name"`
		Format      string        `json:"format"`
		Content     domain.Object `json:"content"`
		Description string        `json:"description"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	switch in.Format {
	case "xray", "mihomo", "singbox", "v2rayn":
	default:
		s.writeError(w, r, domain.Validation("format must be xray, mihomo, singbox or v2rayn"))
		return
	}
	v, err := s.Store.CreateTemplate(r.Context(), &domain.Template{ID: newID(ids.Template), Name: in.Name,
		Format: in.Format, Content: in.Content, Description: in.Description})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "template.create", "template", v.ID, nil)
	s.writeData(w, http.StatusCreated, v)
}

func (s *Server) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.GetTemplate(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, v)
}

func (s *Server) handlePatchTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version     int64         `json:"version"`
		Name        *string       `json:"name"`
		Format      *string       `json:"format"`
		Content     domain.Object `json:"content"`
		Description *string       `json:"description"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := requireVersion(in.Version); err != nil {
		s.writeError(w, r, err)
		return
	}
	v, err := s.Store.UpdateTemplate(r.Context(), r.PathValue("id"), in.Version, func(v *domain.Template) error {
		if in.Name != nil {
			v.Name = *in.Name
		}
		if in.Format != nil {
			v.Format = *in.Format
		}
		if in.Content != nil {
			v.Content = in.Content
		}
		if in.Description != nil {
			v.Description = *in.Description
		}
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "template.update", "template", v.ID, nil)
	s.writeData(w, http.StatusOK, v)
}

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Store.DeleteTemplate(r.Context(), id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "template.delete", "template", id, nil)
	s.writeData(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

func (s *Server) handleListInvitations(w http.ResponseWriter, r *http.Request) {
	s.operationsInvitations(w, r)
}

func (s *Server) handleCreateInvitation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SubscriptionID *string    `json:"subscription_id"`
		ExpiresAt      *time.Time `json:"expires_at"`
		MaxUses        int        `json:"max_uses"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	code := secrets.NewToken(24)
	expires := time.Now().UTC().Add(7 * 24 * time.Hour)
	if in.ExpiresAt != nil {
		expires = in.ExpiresAt.UTC()
	}
	id, err := s.Store.CreateInvitation(r.Context(), secrets.HashToken(code), in.SubscriptionID, expires,
		in.MaxUses, memberFrom(r.Context()).Username)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "invitation.create", "invitation", id, nil)
	s.writeData(w, http.StatusCreated, map[string]any{"id": id, "code": code, "expires_at": expires})
}

func (s *Server) handleRevokeInvitation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Store.RevokeInvitation(r.Context(), id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "invitation.revoke", "invitation", id, nil)
	s.writeData(w, http.StatusOK, map[string]any{"revoked": true, "id": id})
}

func (s *Server) handleAdminUsage(w http.ResponseWriter, r *http.Request) {
	filter, err := usageWindow(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	scope := adminUsageScope(r, filter)
	after, err := s.adminUsageCursor(r, scope)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items, err := s.Store.ListAdminUsagePage(r.Context(), filter, after)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var next *string
	if len(items) > filter.Limit {
		items = items[:filter.Limit]
		last := items[len(items)-1]
		next, err = s.meNext("admin-usage", scope, []string{meTime(last.PeriodStart), last.MemberID, last.NodeID, meTime(last.PeriodEnd)})
		if err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	s.writeList(w, items, next)
}

// usageWindow parses the usage query. from/to filter on the cycle start and are
// honoured (they used to be ignored); malformed values are a 400.
func usageWindow(r *http.Request) (store.UsageFilter, error) {
	q := r.URL.Query()
	filter := store.UsageFilter{
		MemberID: strings.TrimSpace(q.Get("member_id")),
		NodeID:   strings.TrimSpace(q.Get("node_id")),
		Limit:    50,
	}
	fields := map[string]string{}
	for key, values := range q {
		if len(values) != 1 {
			if key == "cursor" {
				return filter, cursorInvalid("invalid")
			}
			fields[key] = "invalid"
		}
		switch key {
		case "member_id", "node_id", "from", "to", "limit", "cursor":
		default:
			fields[key] = "unknown"
		}
	}
	if raw := strings.TrimSpace(q.Get("from")); raw != "" {
		if t, err := parseQueryTime(raw); err != nil {
			fields["from"] = err.Error()
		} else {
			filter.From = &t
		}
	}
	if raw := strings.TrimSpace(q.Get("to")); raw != "" {
		if t, err := parseQueryTime(raw); err != nil {
			fields["to"] = err.Error()
		} else {
			filter.To = &t
		}
	}
	if len(fields) > 0 {
		return filter, domain.Validation("invalid usage time filter").WithFieldErrors(fields)
	}
	if filter.From != nil && filter.To != nil && !filter.To.After(*filter.From) {
		return filter, domain.Validation("to must be after from").
			WithFieldErrors(map[string]string{"to": "must be after from"})
	}
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil {
			return filter, domain.Validation("invalid limit").WithFieldErrors(map[string]string{"limit": "invalid"})
		}
		filter.Limit = n
	}
	if filter.Limit < 1 {
		filter.Limit = 1
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}
	return filter, nil
}

// parseQueryTime accepts RFC3339 (with optional sub-second precision) and the
// date-only form YYYY-MM-DD, which is interpreted as UTC midnight.
func parseQueryTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("must be RFC3339 (e.g. 2026-10-20T00:00:00Z) or YYYY-MM-DD")
}

func (s *Server) handleAdminEvents(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListEvents(r.Context(), 200)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeList(w, items, nil)
}

func (s *Server) handleAckEvent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.Store.AckEvent(r.Context(), id, memberFrom(r.Context()).Username); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "event.ack", "event", id, nil)
	s.writeData(w, http.StatusOK, map[string]any{"acked": true, "id": id})
}

func (s *Server) handleAdminTasks(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListTasks(r.Context(), r.URL.Query().Get("state"), 200)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeList(w, items, nil)
}

func (s *Server) handleRetryTask(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.RetryTask(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "task.retry", "task", v.ID, nil)
	s.writeData(w, http.StatusOK, v)
}

func (s *Server) handleAuditLogs(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListAudit(r.Context(), 200)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeList(w, items, nil)
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.GetSettings(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, v)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version          int64         `json:"version"`
		Timezone         *string       `json:"timezone"`
		RetentionDays    *int          `json:"retention_days"`
		TelegramEnabled  *bool         `json:"telegram_enabled"`
		TelegramChatID   *string       `json:"telegram_chat_id"`
		TelegramToken    *string       `json:"telegram_token"`
		TemplateDefaults domain.Object `json:"template_defaults"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := requireVersion(in.Version); err != nil {
		s.writeError(w, r, err)
		return
	}
	v, err := s.Store.UpdateSettings(r.Context(), in.Version, func(v *domain.Settings) error {
		if in.Timezone != nil {
			if _, err := period.LoadTimezone(*in.Timezone); err != nil {
				return domain.Validation("%s", err.Error())
			}
			v.Timezone = *in.Timezone
		}
		if in.RetentionDays != nil {
			if *in.RetentionDays < 1 {
				return domain.Validation("retention_days must be positive")
			}
			v.RetentionDays = *in.RetentionDays
		}
		if in.TelegramEnabled != nil {
			v.TelegramEnabled = *in.TelegramEnabled
		}
		if in.TelegramChatID != nil {
			v.TelegramChatID = *in.TelegramChatID
		}
		if in.TelegramToken != nil {
			v.TelegramToken = *in.TelegramToken
		}
		if in.TemplateDefaults != nil {
			v.TemplateDefaults = in.TemplateDefaults
		}
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "settings.update", "settings", "1", nil)
	s.writeData(w, http.StatusOK, v)
}

func (s *Server) handleGetRecovery(w http.ResponseWriter, r *http.Request) {
	state, err := s.Store.GetControlState(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, state)
}

func (s *Server) handleEnterRecovery(w http.ResponseWriter, r *http.Request) {
	state, err := s.Store.EnterRecoveryMode(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "recovery.enter", "control", "1", nil)
	s.writeData(w, http.StatusOK, state)
}

func (s *Server) handleExitRecovery(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Confirm               string `json:"confirm"`
		AgentRevisionsChecked bool   `json:"agent_revisions_checked"`
		LedgerChecked         bool   `json:"ledger_checked"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if in.Confirm != "verified" || !in.AgentRevisionsChecked || !in.LedgerChecked {
		s.writeError(w, r, domain.Validation("confirm=verified, agent_revisions_checked=true and ledger_checked=true are required"))
		return
	}
	state, err := s.Store.ExitRecoveryMode(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "recovery.exit", "control", "1", nil)
	s.writeData(w, http.StatusOK, state)
}
