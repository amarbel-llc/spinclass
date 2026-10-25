package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/store"
)

// runHardenedCredential runs `git credential <action>` with a confined
// caller's hardening, as a frontend push would, feeding desc on stdin.
func runHardenedCredential(t *testing.T, credFile, action, desc string) string {
	t.Helper()
	h, _ := confinedHardening(store.Session{
		Confinement: store.ConfinementConfined,
		Repo:        store.Repo{CredentialFile: credFile},
	})
	cmd := exec.Command("git", append(append([]string{}, h.Args...), "credential", action)...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), h.Env...)
	cmd.Env = append(cmd.Env, "HOME="+t.TempDir(), "GIT_CONFIG_NOGLOBAL=1", "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = strings.NewReader(desc)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git credential %s: %v: %s", action, err, errb.String())
	}
	return out.String()
}

// A frontend push must never rewrite the session credential file: the unit's
// InaccessiblePaths mount covers the inode it was started with, and a rewrite
// by rename would leave the new, readable file in the unit's view (round K,
// FDR 0035). git runs the helper's store action after a successful auth and
// erase after a rejected one; both must leave the same inode with the same
// contents, while get still answers from the file.
func TestHardenedCredentialHelperNeverRewritesTheFile(t *testing.T) {
	for _, name := range []string{".spinclass", "it's a .spinclass"} { // a quote and a space exercise the quoting
		t.Run(name, func(t *testing.T) { assertCredentialFileUntouched(t, filepath.Join(t.TempDir(), name)) })
	}
}

func assertCredentialFileUntouched(t *testing.T, dir string) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	credFile := filepath.Join(dir, "git-credentials")
	const line = "https://x-access-token:tok@forge.example\n"
	if err := os.WriteFile(credFile, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(credFile)
	if err != nil {
		t.Fatal(err)
	}

	got := runHardenedCredential(t, credFile, "fill", "protocol=https\nhost=forge.example\n\n")
	if !strings.Contains(got, "password=tok") || !strings.Contains(got, "username=x-access-token") {
		t.Fatalf("fill did not answer from the file: %q", got)
	}
	runHardenedCredential(t, credFile, "approve", "protocol=https\nhost=forge.example\nusername=x-access-token\npassword=tok\n\n")
	runHardenedCredential(t, credFile, "approve", "protocol=https\nhost=other.example\nusername=u\npassword=new\n\n")
	runHardenedCredential(t, credFile, "reject", "protocol=https\nhost=forge.example\nusername=x-access-token\npassword=tok\n\n")

	after, err := os.Stat(credFile)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("the credential file was replaced (new inode); a unit's mount over it would detach")
	}
	if data, _ := os.ReadFile(credFile); string(data) != line {
		t.Errorf("the credential file changed: %q", data)
	}
}
