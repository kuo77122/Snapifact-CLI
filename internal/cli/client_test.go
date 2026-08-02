package cli

import "testing"

func TestServerURLDefaultsToOfficialEndpoint(t *testing.T) {
	t.Setenv("SNAPIFACT_SERVER", "")
	if got := ServerURL(); got != "https://api.snapifact.dev" {
		t.Fatalf("ServerURL() = %q, want %q", got, "https://api.snapifact.dev")
	}
}
