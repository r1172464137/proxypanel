//go:build windows

package state

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// securePath replaces the DACL of the agent state directory with an explicit,
// protected list that only grants the current account and SYSTEM. On Windows a
// file mode such as 0600 is not an access control mechanism, so the token and
// the stored credentials would otherwise stay readable by other local accounts.
// The ACEs are inheritable, so instance, certificate and log directories created
// below the state directory inherit the same restriction.
func securePath(path string) error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("resolve the current account: %w", err)
	}
	system, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return fmt.Errorf("resolve SYSTEM: %w", err)
	}
	entries := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(system),
			},
		},
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("build the state directory ACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		return fmt.Errorf("restrict the ACL of %s (the agent stores its token there): %w", path, err)
	}
	return nil
}

// secureFile is a no-op on Windows: files inherit the parent directory DACL set
// by securePath.
func secureFile(path string) error { return nil }

// statePathACL reports the trustees of the explicit ACEs of path and whether
// inheritance is disabled. It is used by tests and diagnostics.
func statePathACL(path string) (trustees []string, protected bool, err error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, false, err
	}
	control, _, err := sd.Control()
	if err != nil {
		return nil, false, err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return nil, false, err
	}
	if dacl == nil {
		return nil, control&windows.SE_DACL_PROTECTED != 0, nil
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return nil, false, err
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		trustees = append(trustees, sid.String())
	}
	return trustees, control&windows.SE_DACL_PROTECTED != 0, nil
}

// currentUserSID returns the SID of the account running the agent.
func currentUserSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}
