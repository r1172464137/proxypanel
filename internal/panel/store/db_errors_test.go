package store

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"proxypanel/internal/panel/domain"
)

func TestDBErrPostgresConnectionFailuresAreRetryable(t *testing.T) {
	for _, code := range []string{"57P01", "57P02", "57P03", "08000", "08001", "08003", "08004", "08006", "08007", "08P01"} {
		t.Run(code, func(t *testing.T) {
			driver := &pgconn.PgError{Code: code, Message: "synthetic-private-driver-text", Detail: "synthetic-private-detail"}
			err := dbErr(fmt.Errorf("query failed: %w", driver))
			de := domain.AsError(err)
			if de.Code != domain.CodeDBUnavailable || de.Status != http.StatusServiceUnavailable || !de.Retryable {
				t.Fatalf("SQLSTATE %s: want retryable 503 DB_UNAVAILABLE; got %#v", code, de)
			}
			if strings.Contains(de.Error(), "synthetic-private") || de.Details != nil {
				t.Fatalf("driver text escaped to wire error: %#v", de)
			}
		})
	}
}

func TestDBErrRetainsConstraintAndTransactionSemantics(t *testing.T) {
	for _, tc := range []struct {
		state, code string
		status      int
		retryable   bool
	}{
		{"23505", domain.CodeConflict, http.StatusConflict, false},
		{"23503", domain.CodeValidationFailed, http.StatusBadRequest, false},
		{"23514", domain.CodeValidationFailed, http.StatusBadRequest, false},
		{"40001", domain.CodeConflict, http.StatusConflict, true},
		{"40P01", domain.CodeConflict, http.StatusConflict, true},
		{"57014", domain.CodeDBUnavailable, http.StatusServiceUnavailable, true},
	} {
		t.Run(tc.state, func(t *testing.T) {
			de := domain.AsError(dbErr(&pgconn.PgError{Code: tc.state}))
			if de.Code != tc.code || de.Status != tc.status || de.Retryable != tc.retryable {
				t.Fatalf("SQLSTATE %s: got %#v", tc.state, de)
			}
		})
	}
	if de := domain.AsError(dbErr(sql.ErrNoRows)); de.Code != domain.CodeNotFound || de.Status != http.StatusNotFound {
		t.Fatalf("no rows must remain 404, got %#v", de)
	}
	original := domain.Forbidden("owned scope required")
	if got := dbErr(fmt.Errorf("wrapped: %w", original)); got != original {
		t.Fatalf("existing domain errors must retain their exact semantics: %#v", got)
	}
}
