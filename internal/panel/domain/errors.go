package domain

import (
	"errors"
	"fmt"
	"net/http"
)

// Error codes frozen by docs/specs/implementation-contract.md and the proposal
// table. Codes are part of the wire contract; do not rename.
const (
	CodeUnauthenticated           = "UNAUTHENTICATED"
	CodeCSRFFailed                = "CSRF_FAILED"
	CodeForbidden                 = "FORBIDDEN"
	CodeNotFound                  = "NOT_FOUND"
	CodeValidationFailed          = "VALIDATION_FAILED"
	CodeCursorInvalid             = "CURSOR_INVALID"
	CodeReadinessBlocked          = "READINESS_BLOCKED"
	CodeConflict                  = "CONFLICT"
	CodeLastAdmin                 = "LAST_ADMIN"
	CodePreviewRequired           = "PREVIEW_REQUIRED"
	CodePreviewStale              = "PREVIEW_STALE"
	CodePreviewConclusionChanged  = "PREVIEW_CONCLUSION_CHANGED"
	CodeIdempotencyKeyReused      = "IDEMPOTENCY_KEY_REUSED"
	CodeSubscriptionNotBindable   = "SUBSCRIPTION_NOT_BINDABLE"
	CodeProtocolNotSupported      = "PROTOCOL_NOT_SUPPORTED"
	CodeNodeInUse                 = "NODE_IN_USE"
	CodeDBUnavailable             = "DB_UNAVAILABLE"
	CodeRateLimited               = "RATE_LIMITED"
	CodeAgentAuthFailed           = "AGENT_AUTH_FAILED"
	CodeRevisionStale             = "REVISION_STALE"
	CodeSnapshotSchemaUnsupported = "SNAPSHOT_SCHEMA_UNSUPPORTED"
	CodeBatchPayloadMismatch      = "BATCH_PAYLOAD_MISMATCH"
	CodeBatchOwnershipMismatch    = "BATCH_OWNERSHIP_MISMATCH"
	CodeBatchClockSkew            = "BATCH_CLOCK_SKEW"
	CodeRecoveryMode              = "RECOVERY_MODE"
	CodeInternal                  = "INTERNAL"
)

// Error is a wire-level failure with a stable code, HTTP status and optional
// details. Secrets must never be placed in Details.
type Error struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	Retryable bool           `json:"retryable"`
	Status    int            `json:"-"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// E builds an Error.
func E(code string, status int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Status: status}
}

// WithDetail attaches a detail without leaking secrets.
func (e *Error) WithDetail(key string, value any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[key] = value
	return e
}

// WithFieldErrors attaches validation field errors.
func (e *Error) WithFieldErrors(fields map[string]string) *Error {
	if len(fields) == 0 {
		return e
	}
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details["field_errors"] = fields
	return e
}

// WithRetryable marks the error retryable.
func (e *Error) WithRetryable(v bool) *Error {
	e.Retryable = v
	return e
}

func Validation(format string, args ...any) *Error {
	return E(CodeValidationFailed, http.StatusBadRequest, format, args...)
}

func NotFound(format string, args ...any) *Error {
	return E(CodeNotFound, http.StatusNotFound, format, args...)
}

func Conflict(code, format string, args ...any) *Error {
	return E(code, http.StatusConflict, format, args...)
}

func Forbidden(format string, args ...any) *Error {
	return E(CodeForbidden, http.StatusForbidden, format, args...)
}

func Internal(err error) *Error {
	msg := "internal error"
	if err != nil {
		msg = err.Error()
	}
	return E(CodeInternal, http.StatusInternalServerError, "%s", msg)
}

// AsError converts any error into a *Error, treating unknown errors as
// internal failures so callers never leak raw driver text to clients.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var de *Error
	if errors.As(err, &de) {
		return de
	}
	return Internal(err)
}
