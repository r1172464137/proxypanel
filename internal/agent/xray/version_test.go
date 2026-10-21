package xray

import (
	"strings"
	"testing"
)

func TestParseCoreVersion(t *testing.T) {
	version, err := ParseCoreVersion("26.3.27")
	if err != nil {
		t.Fatalf("ParseCoreVersion: %v", err)
	}
	if version.Major != 26 || version.Minor != 3 || version.Patch != 27 {
		t.Fatalf("unexpected version: %+v", version)
	}
	if version.String() != "26.3.27" {
		t.Fatalf("String() = %q", version.String())
	}
	for _, raw := range []string{"", "26", "26.x", "26.3.27.1", "abc"} {
		if _, err := ParseCoreVersion(raw); err == nil {
			t.Fatalf("%q must not parse", raw)
		}
	}
}

func TestCheckCoreVersionOnlyAcceptsVerifiedVersions(t *testing.T) {
	for _, verified := range []string{"26.3.27", "26.3.23"} {
		if _, err := CheckCoreVersion(verified); err != nil {
			t.Fatalf("the verified version %s must be accepted: %v", verified, err)
		}
	}
	// 26.3.26 is deliberately not verified, and the closed list must never turn
	// into "any 26.3.x": a neighbouring patch release has to be checked with the
	// real core before it is accepted.
	for _, raw := range []string{"26.3.26", "26.3.22", "25.9.11", "1.8.24", "27.0.1", "26.10.17"} {
		_, err := CheckCoreVersion(raw)
		if err == nil {
			t.Fatalf("%q must be refused", raw)
		}
		if !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("error for %q should say it is unsupported: %v", raw, err)
		}
	}
	if _, err := CheckCoreVersion(""); err == nil {
		t.Fatal("an empty version must be refused")
	}
	// The parsed version from `xray version` output is accepted, for the
	// baseline core and for the second verified release.
	for _, raw := range []string{
		"Xray 26.3.27 (Xray, Penetrates Everything.) d2758a0 (go1.26.1 windows/amd64)",
		"Xray 26.3.23 (Xray, Penetrates Everything.) cb7bfeb (go1.26.1 windows/amd64)",
	} {
		if _, err := CheckCoreVersion(ParseVersion(raw)); err != nil {
			t.Fatalf("real version output must be accepted: %v", err)
		}
	}
}
