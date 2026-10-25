package trustedhome

import (
	"path/filepath"
	"strings"
	"testing"
)

func stubHome(t *testing.T, home string) {
	t.Helper()
	original := Home
	t.Cleanup(func() { Home = original })
	Home = func() (string, error) { return home, nil }
}

func TestResolveUsesThePasswdHomeNotHOME(t *testing.T) {
	stubHome(t, "/home/operator")
	t.Setenv("HOME", "/somewhere/the/agent/chose")
	t.Setenv("XDG_CONFIG_HOME", "")
	got, err := Resolve("XDG_CONFIG_HOME", ".config", "spinclass", "profile-grant-signers")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/operator", ".config", "spinclass", "profile-grant-signers"); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestResolveAcceptsAnOverrideThatMatchesTheDefault(t *testing.T) {
	stubHome(t, "/home/operator")
	t.Setenv("XDG_STATE_HOME", "/home/operator/.local/state/")
	if _, err := Resolve("XDG_STATE_HOME", filepath.Join(".local", "state"), "spinclass", "grants"); err != nil {
		t.Fatalf("an override equal to the default must be accepted: %v", err)
	}
}

func TestResolveRefusesAnOverrideThatPointsElsewhere(t *testing.T) {
	stubHome(t, "/home/operator")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/agent-config")
	_, err := Resolve("XDG_CONFIG_HOME", ".config", "spinclass", "profile-grant-signers")
	if err == nil || !strings.Contains(err.Error(), "/tmp/agent-config") || !strings.Contains(err.Error(), "/home/operator/.config") {
		t.Fatalf("got %v, want a refusal naming the override and the passwd-home base", err)
	}
}
