package basebranch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// fixture builds a bare upstream carrying one commit on main plus a clone of
// it, isolated from the host's git config. Everything is local paths — these
// tests must never touch a network.
func fixture(t *testing.T) (upstream, clone string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CEILING_DIRECTORIES", root)

	upstream = filepath.Join(root, "upstream.git")
	mustGit(t, root, "init", "-q", "--bare", "-b", "main", upstream)

	seed := filepath.Join(root, "seed")
	mustGit(t, root, "init", "-q", "-b", "main", seed)
	mustGit(t, seed, "config", "user.email", "t@t")
	mustGit(t, seed, "config", "user.name", "t")
	mustWrite(t, filepath.Join(seed, "file.txt"), "initial\n")
	mustGit(t, seed, "add", "file.txt")
	mustGit(t, seed, "commit", "-q", "-m", "init")
	mustGit(t, seed, "remote", "add", "origin", upstream)
	mustGit(t, seed, "push", "-q", "-u", "origin", "main")

	clone = filepath.Join(root, "clone")
	mustGit(t, root, "clone", "-q", upstream, clone)
	mustGit(t, clone, "config", "user.email", "t@t")
	mustGit(t, clone, "config", "user.name", "t")
	return upstream, clone
}

// advanceUpstream lands a commit on the bare upstream's main from a throwaway
// clone, standing in for another session's merge. It edits the seeded file so
// the resulting fast-forward genuinely conflicts with local modifications —
// an empty commit would fast-forward straight past a dirty tree and quietly
// defeat the dirty-holder test.
func advanceUpstream(t *testing.T, upstream string) string {
	t.Helper()
	tmp := t.TempDir()
	work := filepath.Join(tmp, "other")
	mustGit(t, tmp, "clone", "-q", upstream, work)
	mustGit(t, work, "config", "user.email", "t@t")
	mustGit(t, work, "config", "user.name", "t")
	mustWrite(t, filepath.Join(work, "file.txt"), "from upstream\n")
	mustGit(t, work, "commit", "-q", "-am", "upstream work")
	mustGit(t, work, "push", "-q", "origin", "main")
	return mustGit(t, work, "rev-parse", "HEAD")
}

func localMain(t *testing.T, repo string) string {
	t.Helper()
	return mustGit(t, repo, "rev-parse", "refs/heads/main")
}

// A repo with no remote has nothing to be stale against, so it must pass
// without error — and still hand back the default branch's sha, because
// cutting from the wrong branch is a separate defect that has nothing to do
// with remotes. Ordering matters here: this check runs before anything can
// reach the network, which is what keeps every remote-less fixture repo in the
// wider suite on a purely local path.
func TestFreshenNoRemote(t *testing.T) {
	_, clone := fixture(t)
	mustGit(t, clone, "remote", "remove", "origin")

	res, err := Freshen(context.Background(), clone, false, true)
	if err != nil {
		t.Fatalf("a repo with no remote must not error: %v", err)
	}
	if res.Action != SkippedNoRemote {
		t.Errorf("Action = %v, want SkippedNoRemote", res.Action)
	}
	if want := localMain(t, clone); res.BaseSha != want {
		t.Errorf("BaseSha = %q, want the local default's sha %q", res.BaseSha, want)
	}
}

// The happy path, with the default branch checked out in the calling
// checkout — the common layout, and the one that has to go through a merge
// rather than a plain ref update.
func TestFreshenAdvancesBehindDefault(t *testing.T) {
	upstream, clone := fixture(t)
	want := advanceUpstream(t, upstream)

	res, err := Freshen(context.Background(), clone, false, true)
	if err != nil {
		t.Fatalf("Freshen: %v", err)
	}
	if res.Action != Advanced {
		t.Errorf("Action = %v (%s), want Advanced", res.Action, res.Reason)
	}
	if res.BaseSha != want {
		t.Errorf("BaseSha = %q, want the fetched tip %q", res.BaseSha, want)
	}
	if got := localMain(t, clone); got != want {
		t.Errorf("local main = %q, want %q — the branch itself was not advanced", got, want)
	}
}

// With the checkout parked on a feature branch, no worktree holds the default
// branch, so the ref moves directly. This is also the case that motivates
// returning a base at all: HEAD here is the feature branch, and a session cut
// from HEAD would silently inherit it.
func TestFreshenAdvancesWhenDefaultNotCheckedOut(t *testing.T) {
	upstream, clone := fixture(t)
	want := advanceUpstream(t, upstream)
	mustGit(t, clone, "checkout", "-q", "-b", "feature")
	featureSha := mustGit(t, clone, "rev-parse", "HEAD")

	res, err := Freshen(context.Background(), clone, false, true)
	if err != nil {
		t.Fatalf("Freshen: %v", err)
	}
	if res.Action != Advanced {
		t.Errorf("Action = %v (%s), want Advanced", res.Action, res.Reason)
	}
	if res.BaseSha == featureSha {
		t.Error("BaseSha is the feature branch's HEAD; a session would inherit the wrong branch")
	}
	if res.BaseSha != want {
		t.Errorf("BaseSha = %q, want the fetched tip %q", res.BaseSha, want)
	}
	if got := mustGit(t, clone, "branch", "--show-current"); got != "feature" {
		t.Errorf("the checkout moved to %q; Freshen must not change what is checked out", got)
	}
}

// The default branch checked out in some OTHER worktree. The fast-forward has
// to happen THERE — merging in the calling checkout would advance whatever
// branch that one holds instead, which is a silent corruption rather than a
// failure.
func TestFreshenAdvancesDefaultHeldByAnotherWorktree(t *testing.T) {
	upstream, clone := fixture(t)
	want := advanceUpstream(t, upstream)

	// Free main, then check it out somewhere else.
	mustGit(t, clone, "checkout", "-q", "-b", "feature")
	featureSha := mustGit(t, clone, "rev-parse", "HEAD")
	holder := filepath.Join(filepath.Dir(clone), "main-wt")
	mustGit(t, clone, "worktree", "add", "-q", holder, "main")

	res, err := Freshen(context.Background(), clone, false, true)
	if err != nil {
		t.Fatalf("Freshen: %v", err)
	}
	if res.Action != Advanced {
		t.Fatalf("Action = %v (%s), want Advanced", res.Action, res.Reason)
	}
	if got := mustGit(t, holder, "rev-parse", "HEAD"); got != want {
		t.Errorf("the holding worktree is at %q, want %q — the fast-forward ran somewhere else", got, want)
	}
	if got := mustGit(t, clone, "rev-parse", "HEAD"); got != featureSha {
		t.Error("the calling checkout's branch moved; the merge ran in the wrong worktree")
	}
}

// A local default branch that is AHEAD of upstream contains everything upstream
// has, so it is not stale and must never be an error. This is not a corner
// case: it is the state of every repo immediately after a --local-only merge,
// so treating it as staleness would make the override knob look mandatory.
func TestFreshenAheadIsNotStale(t *testing.T) {
	_, clone := fixture(t)
	mustGit(t, clone, "commit", "-q", "--allow-empty", "-m", "unpushed local work")
	local := localMain(t, clone)

	res, err := Freshen(context.Background(), clone, false /*allowStale*/, true /*required*/)
	if err != nil {
		t.Fatalf("being ahead of upstream must not be an error: %v", err)
	}
	if res.Action != SkippedAhead {
		t.Errorf("Action = %v (%s), want SkippedAhead", res.Action, res.Reason)
	}
	if res.BaseSha != local {
		t.Errorf("BaseSha = %q, want the local tip %q", res.BaseSha, local)
	}
}

// Divergence means the base is genuinely missing upstream commits, so creation
// refuses it — but resume tolerates it, and the override always does.
func TestFreshenDivergedIsFatalUnlessTolerated(t *testing.T) {
	upstream, clone := fixture(t)
	advanceUpstream(t, upstream)
	mustGit(t, clone, "commit", "-q", "--allow-empty", "-m", "conflicting local work")
	local := localMain(t, clone)

	for _, tc := range []struct {
		name                string
		allowStale, require bool
		wantErr             bool
	}{
		{"creation refuses", false, true, true},
		{"override permits", true, true, false},
		{"resume tolerates", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Freshen(context.Background(), clone, tc.allowStale, tc.require)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				if !errors.Is(err, ErrStaleBase) {
					t.Errorf("error %v does not wrap ErrStaleBase", err)
				}
				if !strings.Contains(err.Error(), "allow-stale-base") {
					t.Errorf("error %q does not name the override", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Action != SkippedStale {
				t.Errorf("Action = %v (%s), want SkippedStale", res.Action, res.Reason)
			}
			if res.BaseSha != local {
				t.Errorf("BaseSha = %q, want the local tip %q", res.BaseSha, local)
			}
		})
	}
}

// An unreachable remote leaves the base unverifiable. Treating unknown as
// stale is the whole reason the override exists.
func TestFreshenUnreachableRemote(t *testing.T) {
	_, clone := fixture(t)
	gone := filepath.Join(t.TempDir(), "gone.git")
	mustGit(t, clone, "remote", "set-url", "origin", gone)
	local := localMain(t, clone)

	if _, err := Freshen(context.Background(), clone, false, true); err == nil {
		t.Error("an unreachable remote must fail session creation")
	} else if !errors.Is(err, ErrStaleBase) {
		t.Errorf("error %v does not wrap ErrStaleBase", err)
	}

	res, err := Freshen(context.Background(), clone, true /*allowStale*/, true)
	if err != nil {
		t.Fatalf("the override must permit an unreachable remote: %v", err)
	}
	if res.BaseSha != local {
		t.Errorf("BaseSha = %q, want the local tip %q", res.BaseSha, local)
	}

	if _, err := Freshen(context.Background(), clone, false, false /*required*/); err != nil {
		t.Errorf("resume must tolerate an unreachable remote: %v", err)
	}
}

// A dirty worktree holding the default branch blocks the fast-forward, and
// that blocks creation. The failure has to name the path so the operator knows
// which tree to clean — it need not be the checkout they invoked from.
func TestFreshenDirtyHolderIsFatal(t *testing.T) {
	upstream, clone := fixture(t)
	advanceUpstream(t, upstream)
	mustWrite(t, filepath.Join(clone, "file.txt"), "uncommitted local edit\n")

	_, err := Freshen(context.Background(), clone, false, true)
	if err == nil {
		t.Fatal("a dirty checkout blocking the fast-forward must fail session creation")
	}
	if !errors.Is(err, ErrStaleBase) {
		t.Errorf("error %v does not wrap ErrStaleBase", err)
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("error %q does not explain that the tree is dirty", err)
	}

	if _, err := Freshen(context.Background(), clone, true, true); err != nil {
		t.Errorf("the override must permit a dirty holder: %v", err)
	}
}

// Both main and master present: origin/HEAD decides, so this is NOT ambiguous
// and must still resolve. Without the tie-breaker there is nobody to ask —
// spawn-session's stdio is a log file — so it degrades to no base at all
// rather than prompting.
func TestFreshenAmbiguousDefault(t *testing.T) {
	t.Run("origin/HEAD breaks the tie", func(t *testing.T) {
		upstream, clone := fixture(t)
		want := advanceUpstream(t, upstream)
		mustGit(t, clone, "branch", "master")

		res, err := Freshen(context.Background(), clone, false, true)
		if err != nil {
			t.Fatalf("Freshen: %v", err)
		}
		if res.Branch != "main" {
			t.Errorf("Branch = %q, want main (origin/HEAD names it)", res.Branch)
		}
		if res.BaseSha != want {
			t.Errorf("BaseSha = %q, want the fetched tip %q", res.BaseSha, want)
		}
	})

	t.Run("no tie-breaker degrades to HEAD", func(t *testing.T) {
		_, clone := fixture(t)
		mustGit(t, clone, "branch", "master")
		mustGit(t, clone, "symbolic-ref", "-d", "refs/remotes/origin/HEAD")

		res, err := Freshen(context.Background(), clone, false, true)
		if err != nil {
			t.Fatalf("an undecidable default branch must not fail: %v", err)
		}
		if res.Action != SkippedAmbiguous {
			t.Errorf("Action = %v (%s), want SkippedAmbiguous", res.Action, res.Reason)
		}
		if res.BaseSha != "" {
			t.Errorf("BaseSha = %q, want \"\" so the caller falls back to HEAD", res.BaseSha)
		}
	})
}

// Calling twice must be a no-op the second time round: session creation runs
// this on every start, so a repeat that reported work would mean the branch is
// being moved when it is already current.
func TestFreshenIsIdempotent(t *testing.T) {
	upstream, clone := fixture(t)
	want := advanceUpstream(t, upstream)

	if _, err := Freshen(context.Background(), clone, false, true); err != nil {
		t.Fatalf("first Freshen: %v", err)
	}
	res, err := Freshen(context.Background(), clone, false, true)
	if err != nil {
		t.Fatalf("second Freshen: %v", err)
	}
	if res.Action != AlreadyCurrent {
		t.Errorf("Action = %v (%s), want AlreadyCurrent", res.Action, res.Reason)
	}
	if res.BaseSha != want {
		t.Errorf("BaseSha = %q, want %q", res.BaseSha, want)
	}
}
