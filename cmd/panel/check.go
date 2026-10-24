package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"proxypanel/internal/panel/store"
)

// runCheck deliberately bypasses transport, master-key, bootstrap and migration
// setup. A diagnosis must remain available while normal startup is blocked.
func runCheck(ctx context.Context, args []string, out, stderr io.Writer) int {
	flags := flag.NewFlagSet("panel check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	preflight := flags.Bool("preflight", false, "inspect before migrations")
	asJSON := flags.Bool("json", true, "output JSON (default)")
	evidence := flags.String("evidence", os.Getenv("PANEL_UPGRADE_EVIDENCE_FILE"), "local read-only evidence manifest")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected check arguments")
		return 2
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	st, err := store.OpenReadOnly(ctx, store.Options{DatabaseURL: os.Getenv("DATABASE_URL"), MigrationsDir: env("PANEL_MIGRATIONS_DIR", "migrations"), UpgradeEvidenceFile: *evidence})
	var r *store.UpgradeReadiness
	if err == nil {
		defer st.Close()
		r, err = st.CheckUpgradeReadiness(ctx, *preflight)
	}
	if err != nil {
		r = &store.UpgradeReadiness{Status: "unknown", Checks: []store.ReadinessCheck{{Code: "INSPECTION_UNAVAILABLE", Status: "unknown", Count: 1, Remedy: "Verify database access, schema and migration files, then rerun the read-only check."}}}
	}
	if *asJSON {
		if err := json.NewEncoder(out).Encode(r); err != nil {
			return 3
		}
	} else {
		fmt.Fprintf(out, "upgrade readiness: %s\n", r.Status)
		if r.StopVerification != "" {
			fmt.Fprintln(out, r.StopVerification)
		}
		for _, c := range r.Checks {
			fmt.Fprintf(out, "%s %s count=%d: %s\n", c.Status, c.Code, c.Count, c.Remedy)
		}
	}
	switch r.Status {
	case "blocked":
		return 2
	case "unknown":
		return 3
	default:
		return 0
	}
}
