package auth

import (
	"context"
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
