package session

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/sessionlog"
)

const (
	frontendSecret = "FRONTEND-SECRET-8e1d"
	copySecret     = "WORKTREE-SECRET-3f7a"
)

// withSecretsInBothCopies pins a credential whose revoke command carries a
// secret in w1's frontend record, and forges w1's worktree copy with another
// secret in the same field.
func withSecretsInBothCopies(t *testing.T) State {
	t.Helper()
	w1, _ := twoSessions(t)
	front := "revoke --token=" + frontendSecret
	if err := Update(w1.RepoPath, w1.Branch, func(st *State) error {
		st.Credential = &Credential{MintedAt: time.Now().UTC(), RevokeCommand: &front}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	forged := w1
	forgedCmd := "revoke --token=" + copySecret
	forged.Credential = &Credential{MintedAt: time.Now().UTC(), RevokeCommand: &forgedCmd}
	forgeDisplayCopy(t, w1.WorktreePath, forged)
	return w1
}

// A disagreement report names the record, the field and which side differs, but
// never a value from either copy: a pinned credential or entry env can carry a
// secret, and the report goes to stderr and the lifecycle log.
func TestDisagreementReportNamesTheFieldNotItsValues(t *testing.T) {
	w1 := withSecretsInBothCopies(t)
	msgs := captureReports(t)

	if _, err := Read(w1.RepoPath, w1.Branch); err != nil {
		t.Fatal(err)
	}
	if !anyContains(*msgs, w1.Key(), "credential", "both copies set it, to different values", "the frontend record is used") {
		t.Errorf("the disagreement was not reported with record, field and side: %q", *msgs)
	}
	for _, m := range *msgs {
		if strings.Contains(m, frontendSecret) || strings.Contains(m, copySecret) {
			t.Errorf("a report carries a value: %q", m)
		}
	}
}

// The same holds end to end, through the default sink: neither secret reaches
// stderr or the lifecycle log.
func TestDisagreementSecretsReachNeitherStderrNorTheLog(t *testing.T) {
	logHome := t.TempDir()
	t.Setenv("XDG_LOG_HOME", logHome)
	_ = sessionlog.Close()
	if err := sessionlog.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessionlog.Close() })
	w1 := withSecretsInBothCopies(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	_, readErr := Read(w1.RepoPath, w1.Branch)
	os.Stderr = stderr
	_ = w.Close()
	printed, _ := io.ReadAll(r)
	if readErr != nil {
		t.Fatal(readErr)
	}

	logged, err := os.ReadFile(filepath.Join(logHome, "spinclass", "lifecycle.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "credential") {
		t.Errorf("the disagreement was not logged: %s", logged)
	}
	for where, text := range map[string]string{"stderr": string(printed), "lifecycle log": string(logged)} {
		if strings.Contains(text, frontendSecret) || strings.Contains(text, copySecret) {
			t.Errorf("%s carries a secret value:\n%s", where, text)
		}
	}
}

// Which side differs: a field only one copy sets is named as such.
func TestDisagreementReportSaysWhichSideSetsTheField(t *testing.T) {
	w1, _ := twoSessions(t)
	forged := w1
	forged.Principal = "evil-principal"
	forgeDisplayCopy(t, w1.WorktreePath, forged)
	msgs := captureReports(t)

	if _, err := Read(w1.RepoPath, w1.Branch); err != nil {
		t.Fatal(err)
	}
	if !anyContains(*msgs, "principal", "the worktree copy sets it, the frontend record does not") {
		t.Errorf("the side was not named: %q", *msgs)
	}
	if anyContains(*msgs, "evil-principal") {
		t.Errorf("a report carries the forged value: %q", *msgs)
	}
}
