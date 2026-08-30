package cli

import "testing"

func TestToolVersionPrefersReleaseBuildVersion(t *testing.T) {
	previous := buildVersion
	buildVersion = "v0.2.0"
	t.Cleanup(func() { buildVersion = previous })
	if got := toolVersion(); got != "v0.2.0" {
		t.Fatalf("toolVersion() = %q", got)
	}
}
