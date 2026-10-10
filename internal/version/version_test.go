package version

import "testing"

func TestString(t *testing.T) {
	// A test binary has no module version.
	if got := String(); got != "dev" {
		t.Errorf("String() = %q, want dev in a test build", got)
	}
	release = "v1.2.3"
	defer func() { release = "" }()
	if got := String(); got != "v1.2.3" {
		t.Errorf("String() = %q, want the release version", got)
	}
}
