package auth

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// The record keeps where the token came from, so it can be answered later from
// state, and an existing session reads it back as reused.
func TestMintRecordsItsSourceAndAResumeSaysReused(t *testing.T) {
	repoPath, wtPath, id := setupRepo(t)
	outcome, err := Mint(context.Background(), authSweatfile("echo tok", "true"), id, "")
	if err != nil || !outcome.Minted || outcome.Source != SourceMint {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	st, err := session.Read(repoPath, "feature-x")
	if err != nil || st.Credential == nil || st.Credential.Source != SourceMint || st.Credential.PoolSlot != "" {
		t.Fatalf("record = %+v err=%v, want source %q", st, err, SourceMint)
	}
	got := RecordedStatus(repoPath, "feature-x", wtPath)
	if !got.Token || !got.Reused || got.Source != SourceMint || got.MintedAt.IsZero() {
		t.Errorf("RecordedStatus = %+v, want the reused token", got)
	}
	// The origin is ssh and the mint's rewrite makes git push to it over https.
	if got.PushTo != (PushDest{HTTP: true}) {
		t.Errorf("PushTo = %+v, want the push URL as git rewrites it", got.PushTo)
	}
	RemoveCredential(wtPath)
	if got := RecordedStatus(repoPath, "feature-x", wtPath); got.Token || !strings.Contains(got.Reason, "credential file is gone") {
		t.Errorf("with the file gone: %+v", got)
	}
}

func TestPoolMintRecordsTheSlotAndItsDeadline(t *testing.T) {
	repoPath, _, id := setupRepo(t)
	deadline := future()
	writePool(t, "https://forge.example.com", testSlot{session: "pool/spinclass/07", repo: "owner/repo", deadline: deadline})
	outcome, err := Mint(context.Background(), poolSweatfile("", "true"), id, "")
	if err != nil || !outcome.Minted {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	line := StatusFromMint(outcome, nil).Line(true, "repo/feature-x")
	if want := "push_token: yes (pool slot pool/spinclass/07; expires " + deadline + ")"; line != want {
		t.Errorf("line = %q, want %q", line, want)
	}
	st, err := session.Read(repoPath, "feature-x")
	if err != nil || st.Credential == nil || st.Credential.Source != SourcePool || st.Credential.PoolSlot != "pool/spinclass/07" || st.Credential.ExpiresAt == nil {
		t.Fatalf("record = %+v err=%v", st, err)
	}
}

// The line may be all a caller sees, so it carries the cause in Mint's own
// words: never the command's output or the error's text.
func TestAFailedMintsCauseIsInTheLine(t *testing.T) {
	for name, tc := range map[string]struct{ cmd, want string }{
		"status": {"echo STDERR-MARKER >&2; exit 7", "push_token: no (the mint failed: the mint command exited with status 7); "},
		"empty":  {"true", "push_token: no (the mint failed: the mint command printed no token); "},
		"agent":  {"echo 'Permission denied (publickey)' >&2; exit 1", "status 1 (the ssh agent offered no usable key or could not be reached)"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, id := setupRepo(t)
			t.Setenv("SSH_AUTH_SOCK", "")
			noBackoff(t)
			_, err := Mint(context.Background(), authSweatfile(tc.cmd, "true"), id, "")
			line := StatusFromMint(MintOutcome{}, err).Line(false, "repo/feature-x")
			if err == nil || !strings.Contains(line, tc.want) || strings.Contains(line, "STDERR-MARKER") || strings.Contains(line, "publickey") {
				t.Errorf("line = %q (failed: %v), want %q and no command output", line, err != nil, tc.want)
			}
		})
	}
}

// A token that was made but not set up is "unknown", with how to revoke it.
func TestAPartlyFailedMintSaysATokenMayExist(t *testing.T) {
	_, _, id := setupRepo(t)
	// core.worktree in the common config makes the git config step refuse.
	_, err := Mint(context.Background(), authSweatfile(`git config core.worktree "$PWD" && echo tok`, "true"), id, "")
	if err == nil || !strings.Contains(err.Error(), "may still be valid at the forge") || !strings.Contains(err.Error(), "SPINCLASS_SESSION_ID="+id.SessionKey) {
		t.Fatalf("err = %v", err)
	}
	line := StatusFromMint(MintOutcome{}, err).Line(true, id.SessionKey)
	for _, want := range []string{"push_token: unknown (a token was made, but setting up the worktree's git config failed", "may be valid at the forge", "revoke-command with SPINCLASS_SESSION_ID=" + id.SessionKey} {
		if !strings.Contains(line, want) {
			t.Errorf("line = %q lacks %q", line, want)
		}
	}
}

func TestARecordThatCannotBeReadIsNotSaidToHaveNoToken(t *testing.T) {
	repoPath, wtPath, id := setupRepo(t)
	if _, err := Mint(context.Background(), authSweatfile("echo tok", "true"), id, ""); err != nil {
		t.Fatal(err)
	}
	corruptSessionRecords(t)
	line := RecordedStatus(repoPath, "feature-x", wtPath).Line(true, id.SessionKey)
	if !strings.HasPrefix(line, "push_token: unknown (spinclass could not read this session's record") {
		t.Errorf("line = %q", line)
	}
}

// A skip reason quotes the parse error, which quotes the origin: its userinfo
// does not come with it.
func TestASkipReasonNeverCarriesURLUserinfo(t *testing.T) {
	repoPath, _, id := setupRepo(t)
	if out, err := exec.Command("git", "-C", repoPath, "remote", "set-url", "origin", "https://bob:userinfo-marker@forge.example.com:notaport/o/r.git").CombinedOutput(); err != nil {
		t.Fatalf("set-url: %v\n%s", err, out)
	}
	outcome, err := Mint(context.Background(), authSweatfile("echo tok", "true"), id, "")
	if err != nil || outcome.Kind != NoForgeOrigin || !strings.Contains(outcome.Skipped, "<redacted>@forge.example.com") {
		t.Fatalf("kind %q err %v; redacted: %v", outcome.Kind, err, strings.Contains(outcome.Skipped, "<redacted>@"))
	}
	if strings.Contains(outcome.Skipped, "userinfo-marker") {
		t.Error("the skip reason carries the origin's userinfo")
	}
}

func corruptSessionRecords(t *testing.T) {
	t.Helper()
	records, err := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "spinclass", "records", "*.json"))
	if err != nil || len(records) == 0 {
		t.Fatalf("no session record to corrupt (%v)", err)
	}
	for _, r := range records {
		if err := os.WriteFile(r, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPushDestinationsReadsPushurlAndRewrites(t *testing.T) {
	repoPath, wtPath, _ := setupRepo(t)
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repoPath}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("remote", "set-url", "origin", "https://forge.example.com/o/r.git")
	if got := PushDestinations(wtPath); got != (PushDest{HTTP: true}) {
		t.Errorf("https origin = %+v", got)
	}
	run("remote", "set-url", "--push", "origin", "git@forge.example.com:o/r.git")
	if got := PushDestinations(wtPath); got != (PushDest{SSH: true}) {
		t.Errorf("ssh pushurl = %+v", got)
	}
	run("remote", "set-url", "--push", "origin", "/srv/mirror.git")
	if got := PushDestinations(wtPath); got != (PushDest{Other: true}) {
		t.Errorf("path pushurl = %+v", got)
	}
	run("remote", "remove", "origin")
	if got := PushDestinations(wtPath); got != (PushDest{}) {
		t.Errorf("no origin = %+v", got)
	}
}

func TestAMintThatFailsUnderAKeyHolderAgentNamesIt(t *testing.T) {
	_, _, id := setupRepo(t)
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(keyholder.StateBase(), "abc", "agent", "agent.sock"))
	_, err := Mint(context.Background(), authSweatfile("false", "true"), id, "")
	if err == nil || !strings.Contains(err.Error(), "[auth] mint-command failed (the ssh agent here") {
		t.Fatalf("err = %v, want the key holder agent named", err)
	}
}

func TestARevokeThatFailsUnderAKeyHolderAgentNamesIt(t *testing.T) {
	_, _, id := setupRepo(t)
	if _, err := Mint(context.Background(), authSweatfile("echo tok", "false"), id, ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(keyholder.StateBase(), "abc", "agent", "agent.sock"))
	_, err := Revoke(context.Background(), id, nil)
	if err == nil || !strings.Contains(err.Error(), "[auth] revoke-command failed (the ssh agent here") {
		t.Fatalf("err = %v, want the key holder agent named", err)
	}
}
