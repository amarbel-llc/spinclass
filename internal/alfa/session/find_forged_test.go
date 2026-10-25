package session

import (
	"errors"
	"path/filepath"
	"testing"
)

// A worker forges its display copy's key, branch and worktree to equal a victim's
// target. Resolving the victim's target (by session key or by worktree basename)
// must reach the victim's frontend record, never the worker, and must not report
// an ambiguity the forgery manufactured.
func TestFindByTargetIgnoresAForgedKeyCollision(t *testing.T) {
	w1, s2 := twoSessions(t)
	captureReports(t)
	forged := forgedVictimCopy(s2)
	forged.SessionKey = s2.Key()
	forgeDisplayCopy(t, w1.WorktreePath, forged)

	for _, target := range []string{s2.Key(), "victim"} {
		got, err := FindByTarget(target)
		if err != nil {
			t.Fatalf("FindByTarget(%q): %v", target, err)
		}
		if got.WorktreePath != s2.WorktreePath || got.Branch != "victim" || got.Principal != "victim-principal" || got.IsTombstone() {
			t.Errorf("FindByTarget(%q) = %s (%s, principal %q), want the victim's frontend record", target, got.Key(), got.WorktreePath, got.Principal)
		}
		if got.Principal == "evil-principal" || len(got.Holders) != 0 || got.Entry != nil {
			t.Errorf("FindByTarget(%q) returned forged authority fields: %+v", target, got)
		}
	}

	// The worker's own target still resolves to the worker.
	own, err := FindByTarget(w1.Key())
	if err != nil || own.WorktreePath != w1.WorktreePath || own.Principal == "evil-principal" {
		t.Errorf("FindByTarget(%q) = %+v, %v; want the worker's own record", w1.Key(), own, err)
	}
}

// A worker forges a key that no real session has. The forged copy must not
// conjure a target: the frontend resolves only keys it recorded.
func TestFindByTargetDoesNotResolveAKeyOnlyAForgedCopyNames(t *testing.T) {
	w1, _ := twoSessions(t)
	captureReports(t)
	forged := w1
	forged.SessionKey = filepath.Base(w1.RepoPath) + "/ghost"
	forged.Branch = "ghost"
	forged.WorktreePath = filepath.Join(w1.RepoPath, ".worktrees", "ghost")
	forgeDisplayCopy(t, w1.WorktreePath, forged)

	for _, target := range []string{forged.SessionKey, "ghost"} {
		if got, err := FindByTarget(target); !errors.Is(err, ErrTargetNotFound) {
			t.Errorf("FindByTarget(%q) = %+v, %v; want ErrTargetNotFound", target, got, err)
		}
	}
}
