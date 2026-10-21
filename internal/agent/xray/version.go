package xray

import (
	"fmt"
	"strconv"
	"strings"
)

// SupportedCoreVersions is the closed list of Xray releases this agent was
// verified against. Unknown versions are refused instead of being assumed
// compatible. 26.3.23 was added only after the real core passed the local
// config, per-user statistics and revocation checks (see
// docs/verification/docker-lifecycle.md); the list stays closed and is never
// widened by a version range.
var SupportedCoreVersions = []string{"26.3.27", "26.3.23"}

// CoreVersion is a parsed Xray version triple.
type CoreVersion struct {
	Raw   string
	Major int
	Minor int
	Patch int
}

// String renders the numeric triple.
func (v CoreVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// ParseCoreVersion parses a version token such as "26.3.27" (a trailing
// pre-release suffix after '-' or '+' is ignored).
func ParseCoreVersion(raw string) (CoreVersion, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return CoreVersion{}, fmt.Errorf("core version is empty")
	}
	base := trimmed
	if idx := strings.IndexAny(base, "-+ "); idx >= 0 {
		base = base[:idx]
	}
	parts := strings.Split(base, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return CoreVersion{}, fmt.Errorf("core version %q is not a version triple", raw)
	}
	nums := make([]int, 3)
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return CoreVersion{}, fmt.Errorf("core version %q is not numeric", raw)
		}
		nums[i] = n
	}
	return CoreVersion{Raw: trimmed, Major: nums[0], Minor: nums[1], Patch: nums[2]}, nil
}

// CheckCoreVersion verifies that the running core is on the supported list.
func CheckCoreVersion(raw string) (CoreVersion, error) {
	version, err := ParseCoreVersion(raw)
	if err != nil {
		return CoreVersion{}, err
	}
	for _, supported := range SupportedCoreVersions {
		ref, err := ParseCoreVersion(supported)
		if err != nil {
			continue
		}
		if ref.Major == version.Major && ref.Minor == version.Minor && ref.Patch == version.Patch {
			return version, nil
		}
	}
	return version, fmt.Errorf("core version %s is not supported by this agent (verified versions: %s); install a verified core or report the incompatibility before using it",
		version.Raw, strings.Join(SupportedCoreVersions, ", "))
}
