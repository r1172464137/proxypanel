// Test-only PostgreSQL owner: never uses public as its search_path.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"

	"github.com/jackc/pgx/v5"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func main() {
	ctx := context.Background()
	mode := os.Args[1]
	file := os.Args[2]
	cfg := map[string]string{}
	if mode == "init" {
		u, e := url.Parse(os.Getenv("PANEL_TEST_DATABASE_URL"))
		must(e)
		if u.Scheme != "postgres" && u.Scheme != "postgresql" {
			panic("URL PostgreSQL DSN required")
		}
		b := make([]byte, 16)
		_, e = rand.Read(b)
		must(e)
		schema := "pr_browser_" + hex.EncodeToString(b)
		q := u.Query()
		q.Set("search_path", "pg_catalog")
		u.RawQuery = q.Encode()
		c, e := pgx.Connect(ctx, u.String())
		must(e)
		defer c.Close(ctx)
		var current string
		must(c.QueryRow(ctx, "select current_schema()").Scan(&current))
		if current != "pg_catalog" {
			panic("bootstrap scope not pg_catalog")
		}
		_, e = c.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
		must(e)
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		cfg["DATABASE_URL"] = u.String()
		cfg["schema"] = schema
		for _, k := range []string{"PANEL_MASTER_KEY", "PANEL_ADMIN_PASSWORD"} {
			_, e = rand.Read(b)
			must(e)
			cfg[k] = hex.EncodeToString(b) + "Pr9!"
		}
		raw, e := json.Marshal(cfg)
		must(e)
		must(os.WriteFile(file, raw, 0600))
	} else {
		raw, e := os.ReadFile(file)
		must(e)
		must(json.Unmarshal(raw, &cfg))
	}
	schema := cfg["schema"]
	if !regexp.MustCompile(`^pr_browser_[a-f0-9]{32}$`).MatchString(schema) {
		panic("invalid ownership")
	}
	c, e := pgx.Connect(ctx, cfg["DATABASE_URL"])
	must(e)
	defer c.Close(ctx)
	var current string
	must(c.QueryRow(ctx, "select current_schema()").Scan(&current))
	if current != schema {
		panic("exclusive schema proof failed")
	}
	switch mode {
	case "init":
		fmt.Println(`{"exclusive_schema_proved":true,"public_search_path":false}`)
	case "drop":
		_, e = c.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		must(e)
		fmt.Println(`{"schema_dropped":true}`)
	case "sql":
		sql, e := io.ReadAll(os.Stdin)
		must(e)
		rows, e := c.Query(ctx, string(sql))
		must(e)
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			vals, e := rows.Values()
			must(e)
			row := map[string]any{}
			for i, f := range rows.FieldDescriptions() {
				row[f.Name] = vals[i]
			}
			out = append(out, row)
		}
		must(rows.Err())
		must(json.NewEncoder(os.Stdout).Encode(out))
	default:
		if mode != "init" {
			panic("unknown mode")
		}
	}
}
