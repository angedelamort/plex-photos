package unit

import (
	"testing"

	"plex-photos/auth"
)

func TestPlexConfigConfigured(t *testing.T) {
	cases := []struct {
		name string
		cfg  auth.PlexConfig
		want bool
	}{
		{"empty", auth.PlexConfig{}, false},
		{"only server", auth.PlexConfig{ServerURL: "http://plex:32400"}, false},
		{"only machine", auth.PlexConfig{MachineID: "abc"}, false},
		{"both", auth.PlexConfig{ServerURL: "http://plex:32400", MachineID: "abc"}, true},
		{"whitespace", auth.PlexConfig{ServerURL: "  ", MachineID: "  "}, false},
	}
	for _, tc := range cases {
		if got := tc.cfg.Configured(); got != tc.want {
			t.Errorf("%s: Configured() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSetupStateHotApply(t *testing.T) {
	st := auth.NewSetupState(auth.PlexConfig{})
	if st.Configured() {
		t.Fatal("new empty state should not be configured")
	}

	st.Set(auth.PlexConfig{ServerURL: "http://plex:32400", MachineID: "m1", PublicBaseURL: "http://x"})
	if !st.Configured() {
		t.Fatal("state should be configured after Set")
	}
	if got := st.Config(); got.MachineID != "m1" {
		t.Errorf("MachineID = %q, want m1", got.MachineID)
	}
}
