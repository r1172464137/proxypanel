package store

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOperationsQueryKeyset(t *testing.T) {
	q, args := operationsQuery("SELECT id FROM events WHERE true", "created_at", "id", true, 999, []string{"2020-01-01T00:00:00Z", "tie-id"}, map[string]string{"state": "open", "q": `%_\`})
	for _, want := range []string{`state=$1`, `message ILIKE $2 ESCAPE '\'`, `(created_at,id COLLATE "C")<($3,$4 COLLATE "C")`, `ORDER BY created_at DESC,id COLLATE "C" DESC LIMIT $5`} {
		if !strings.Contains(q, want) {
			t.Fatalf("missing %s: %s", want, q)
		}
	}
	want := []any{"open", `%\%\_\\%`, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), "tie-id", 201}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args=%#v want=%#v", args, want)
	}
	q, args = operationsQuery("SELECT id FROM agents WHERE NOT archived", `lower(name) COLLATE "C"`, "id", false, 1, []string{"tied", "id"}, nil)
	if !strings.Contains(q, `(lower(name) COLLATE "C",id COLLATE "C")>($1,$2 COLLATE "C")`) || args[2] != 2 {
		t.Fatalf("name keyset=%s %#v", q, args)
	}
}
