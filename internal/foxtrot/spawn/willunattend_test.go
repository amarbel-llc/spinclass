package spawn

import (
	"path/filepath"
	"testing"
)

// WillBeUnattended mirrors the launch's dontAsk decision for an unconfined
// worker (spinclass#476): true only with the trusted dontAsk key and a
// spawn-entry that takes the splice, false on every doubt.
func TestWillBeUnattended(t *testing.T) {
	const (
		trustedDontAsk = "[session-entry]\nunattended-permission-mode = \"dontAsk\"\n"
		trustedAuto    = "[session-entry]\nunattended-permission-mode = \"auto\"\n"
		badEntry       = "spawn-entry = [\"my-harness\", \"{prompt}\"]\n"
	)
	cases := []struct {
		name       string
		global     string
		repoLayer  string
		want       bool
		explicitFn func(home string) string // repo argument
	}{
		{name: "nothing configured", want: false},
		{name: "auto key", global: trustedAuto, want: false},
		{name: "trusted dontAsk", global: trustedDontAsk, want: true},
		{name: "dontAsk only in the repo's own sweatfile is untrusted", repoLayer: trustedDontAsk, want: false},
		{name: "dontAsk but an entry with no provider-args separator", global: trustedDontAsk, repoLayer: "[session-entry]\n" + badEntry, want: false},
		{name: "unresolvable target repo", global: trustedDontAsk, want: false, explicitFn: func(string) string { return "no-such-repo-anywhere" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			driver := makeMainCheckout(t, filepath.Join(home, "work", "driver"))
			if tc.global != "" {
				writeSweatfileAt(t, filepath.Join(home, ".config", "spinclass", "sweatfile"), tc.global)
			}
			if tc.repoLayer != "" {
				writeSweatfileAt(t, filepath.Join(driver, "sweatfile"), tc.repoLayer)
			}
			repo := ""
			if tc.explicitFn != nil {
				repo = tc.explicitFn(home)
			}
			if got := WillBeUnattended(home, driver, repo); got != tc.want {
				t.Errorf("WillBeUnattended = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDriverUnattendedReadsTheMarker(t *testing.T) {
	t.Setenv(unattendedEnvVar, "")
	if DriverUnattended() {
		t.Error("an empty marker is attended")
	}
	t.Setenv(unattendedEnvVar, "1")
	if !DriverUnattended() {
		t.Error("the marker set means unattended")
	}
	t.Setenv(unattendedEnvVar, "yes")
	if !DriverUnattended() {
		t.Error("any non-empty value counts as unattended")
	}
}
