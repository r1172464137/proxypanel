//go:build windows

package state

import (
	"strings"
	"testing"
)

// TestStateDirACLIsRestricted proves the state directory (which holds the agent
// token) is restricted by an explicit, non-inherited DACL instead of a file
// mode, which is not an access control mechanism on Windows.
func TestStateDirACLIsRestricted(t *testing.T) {
	store := newStore(t)
	dir := store.Dir()
	trustees, protected, err := statePathACL(dir)
	if err != nil {
		t.Fatalf("statePathACL: %v", err)
	}
	if !protected {
		t.Fatal("the state directory DACL must be protected (no inherited ACEs)")
	}
	user, err := currentUserSID()
	if err != nil {
		t.Fatalf("currentUserSID: %v", err)
	}
	if len(trustees) == 0 {
		t.Fatal("the state directory has no explicit ACE")
	}
	for _, sid := range trustees {
		if sid == user || sid == "S-1-5-18" {
			continue
		}
		t.Fatalf("unexpected trustee %s on the state directory (ACL: %v)", sid, trustees)
	}
	for _, forbidden := range []string{"S-1-1-0", "S-1-5-32-545", "S-1-5-11"} {
		for _, sid := range trustees {
			if sid == forbidden {
				t.Fatalf("%s (Everyone/Users/Authenticated Users) must not have access: %v", forbidden, trustees)
			}
		}
	}
	if !containsSID(trustees, user) {
		t.Fatalf("the agent account %s lost access: %v", user, trustees)
	}
}

func containsSID(sids []string, want string) bool {
	for _, sid := range sids {
		if strings.EqualFold(sid, want) {
			return true
		}
	}
	return false
}
