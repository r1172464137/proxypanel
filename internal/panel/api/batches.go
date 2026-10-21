package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"proxypanel/internal/panel/domain"
	"proxypanel/internal/panel/store"
)

func (s *Server) handleBatchPreview(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Operation string        `json:"operation"`
		MemberIDs []string      `json:"member_ids"`
		Values    domain.Object `json:"values"`
		Reason    string        `json:"reason"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	if strings.TrimSpace(in.Operation) == "" {
		s.writeError(w, r, domain.Validation("operation is required"))
		return
	}
	if in.Values == nil {
		in.Values = domain.NewObject()
	}
	if err := validatePreviewValues(strings.ToLower(strings.TrimSpace(in.Operation)), in.Values); err != nil {
		s.writeError(w, r, err)
		return
	}
	preview, err := s.Store.CreatePreview(r.Context(), store.PreviewRequest{
		Operation: in.Operation, MemberIDs: in.MemberIDs, Values: in.Values,
		Reason: in.Reason, CreatedBy: memberFrom(r.Context()).Username,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, "batch.preview", "preview", preview.ID, nil)
	s.writeData(w, http.StatusOK, preview)
}

func (s *Server) handleBatchSubmit(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		s.writeError(w, r, domain.Validation("Idempotency-Key header is required"))
		return
	}
	var in struct {
		PreviewID       string `json:"preview_id"`
		RulesVersion    int64  `json:"rules_version"`
		MemberSetDigest string `json:"member_set_digest"`
		Confirm         bool   `json:"confirm"`
	}
	if err := decodeJSON(r, &in); err != nil {
		s.writeError(w, r, err)
		return
	}
	res, err := s.Store.SubmitBatch(r.Context(), store.BatchSubmitRequest{
		PreviewID: in.PreviewID, IdempotencyKey: key, RulesVersion: in.RulesVersion,
		MemberSetDigest: in.MemberSetDigest, Confirm: in.Confirm, CreatedBy: memberFrom(r.Context()).Username,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !res.Replay {
		s.audit(r, "batch.submit", "batch", res.Batch.ID, nil)
	}
	s.writeData(w, http.StatusOK, map[string]any{
		"id": res.Batch.ID, "preview_id": res.Batch.PreviewID, "operation": res.Batch.Operation,
		"state": res.Batch.State, "results": res.Results, "summary": res.Summary,
		"member_count": len(res.Results), "idempotent_replay": res.Replay,
		"created_at": res.Batch.CreatedAt,
	})
}

func (s *Server) handleGetBatch(w http.ResponseWriter, r *http.Request) {
	res, err := s.Store.GetBatch(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeData(w, http.StatusOK, map[string]any{
		"batch": res.Batch, "results": res.Results, "summary": res.Summary,
		"id": res.Batch.ID, "state": res.Batch.State, "operation": res.Batch.Operation,
	})
}

// previewRequired rejects a direct edit that would silently change access for
// bound members (contract: PATCH must ask for the preview flow).
func previewRequired(w http.ResponseWriter, r *http.Request, detail string) {
	e := domain.E(domain.CodePreviewRequired, 409,
		"this edit affects existing members; use /api/v1/admin/batches/preview and /batches")
	e.Details = map[string]any{"reason": detail, "preview_endpoint": "/api/v1/admin/batches/preview"}
	e.Retryable = false
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusConflict)
	body := map[string]any{"code": e.Code, "message": e.Message, "retryable": false,
		"details": e.Details, "request_id": requestID(r.Context())}
	_ = writeJSONBody(w, map[string]any{"error": body})
}

// validatePreviewValues rejects shapes the store cannot apply and, importantly,
// keeps explicit JSON nulls intact: inside values.patch a null clears a value
// (unlimited quota, no template), so it must reach the store untouched.
func validatePreviewValues(operation string, values domain.Object) error {
	switch operation {
	case store.OpUpdateSubscription, store.OpUpdateNodeGroup:
		if id, ok := values.String("id"); !ok || strings.TrimSpace(id) == "" {
			return domain.Validation("values.id is required for %s", operation)
		}
		raw, ok := values.Get("patch")
		if !ok {
			return domain.Validation("values.patch is required for %s", operation)
		}
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(raw, &probe); err != nil || probe == nil {
			return domain.Validation("values.patch must be a JSON object")
		}
	case store.OpAssignSubscription:
		if id, ok := values.String("subscription_id"); !ok || strings.TrimSpace(id) == "" {
			return domain.Validation("values.subscription_id is required for assign_subscription")
		}
	case store.OpSetActive:
		if _, ok := values.Bool("active"); !ok {
			return domain.Validation("values.active is required for set_active")
		}
	case store.OpSetExpiry:
		if _, ok := values.Get("expires_at"); !ok {
			return domain.Validation("values.expires_at is required for set_expiry (null clears it)")
		}
	}
	return nil
}
