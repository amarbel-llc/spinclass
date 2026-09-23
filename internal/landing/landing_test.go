package landing

import (
	"context"
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

// fixture builds a bare upstream with one commit on main and a clone of it,
// isolated from the host's git config.
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
	mustGit(t, seed, "push", "-q", upstream, "main")

	clone = filepath.Join(root, "clone")
	mustGit(t, root, "clone", "-q", upstream, clone)
	mustGit(t, clone, "config", "user.email", "t@t")
	mustGit(t, clone, "config", "user.name", "t")
	return upstream, clone
}

// advanceUpstream lands a commit editing file.txt on upstream main from a
// throwaway clone (another session's merge), then fetches it into clone.
func advanceUpstream(t *testing.T, upstream, clone string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "other")
	mustGit(t, filepath.Dir(work), "clone", "-q", upstream, work)
	mustGit(t, work, "config", "user.email", "t@t")
	mustGit(t, work, "config", "user.name", "t")
	mustWrite(t, filepath.Join(work, "file.txt"), "from upstream\n")
	mustGit(t, work, "commit", "-q", "-am", "upstream work")
	mustGit(t, work, "push", "-q", "origin", "main")
	if _, err := Remote(clone, "main", "origin").Fetch(context.Background(), clone); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	return mustGit(t, work, "rev-parse", "HEAD")
}

func localMain(t *testing.T, repo string) string {
	t.Helper()
	return mustGit(t, repo, "rev-parse", "refs/heads/main")
}

func TestTargetRefs(t *testing.T) {
	r := Remote("/r", "master", "origin")
	if r.Ref() != "refs/remotes/origin/master" || r.Label() != "origin/master" || r.IsSelf() {
		t.Errorf("remote target: ref=%q label=%q self=%v", r.Ref(), r.Label(), r.IsSelf())
	}
	s := Self("/r", "master")
	if s.Ref() != "refs/heads/master" || s.Label() != "master" || !s.IsSelf() {
		t.Errorf("self target: ref=%q label=%q self=%v", s.Ref(), s.Label(), s.IsSelf())
	}
	if got := ForMerge("/r", "master", false); !got.IsSelf() {
		t.Errorf("ForMerge(gitSync=false) = %+v, want self", got)
	}
}

func TestAdvanceLocalThroughHolder(t *testing.T) {
	upstream, clone := fixture(t)
	want := advanceUpstream(t, upstream, clone)
	target := Remote(clone, "main", "origin")

	adv := target.AdvanceLocal(target.Ref())
	if adv.Outcome != Advanced {
		t.Fatalf("Outcome = %v (%s), want Advanced", adv.Outcome, adv.Reason)
	}
	if got := localMain(t, clone); got != want {
		t.Errorf("local main = %q, want %q", got, want)
	}
	if again := target.AdvanceLocal(target.Ref()); again.Outcome != Current {
		t.Errorf("second advance Outcome = %v, want Current", again.Outcome)
	}
}

func TestAdvanceLocalWithoutHolder(t *testing.T) {
	upstream, clone := fixture(t)
	want := advanceUpstream(t, upstream, clone)
	mustGit(t, clone, "checkout", "-q", "-b", "feature")

	adv := Remote(clone, "main", "origin").AdvanceLocal(want)
	if adv.Outcome != Advanced || adv.Holder != "" {
		t.Fatalf("Outcome = %v holder=%q (%s), want Advanced with no holder", adv.Outcome, adv.Holder, adv.Reason)
	}
	if got := localMain(t, clone); got != want {
		t.Errorf("local main = %q, want %q", got, want)
	}
	if got := mustGit(t, clone, "branch", "--show-current"); got != "feature" {
		t.Errorf("checkout moved to %q", got)
	}
}

func TestAdvanceLocalSkips(t *testing.T) {
	t.Run("ahead", func(t *testing.T) {
		_, clone := fixture(t)
		mustGit(t, clone, "commit", "-q", "--allow-empty", "-m", "local only")
		local := localMain(t, clone)
		target := Remote(clone, "main", "origin")
		adv := target.AdvanceLocal(target.Ref())
		if adv.Outcome != Ahead || !adv.Skipped() {
			t.Fatalf("Outcome = %v, want Ahead", adv.Outcome)
		}
		if got := localMain(t, clone); got != local {
			t.Error("an ahead local branch was moved")
		}
	})

	t.Run("diverged", func(t *testing.T) {
		upstream, clone := fixture(t)
		advanceUpstream(t, upstream, clone)
		mustGit(t, clone, "commit", "-q", "--allow-empty", "-m", "conflicting local work")
		local := localMain(t, clone)
		target := Remote(clone, "main", "origin")
		adv := target.AdvanceLocal(target.Ref())
		if adv.Outcome != Diverged {
			t.Fatalf("Outcome = %v, want Diverged", adv.Outcome)
		}
		if got := localMain(t, clone); got != local {
			t.Error("a diverged local branch was moved")
		}
	})

	t.Run("dirty overlap", func(t *testing.T) {
		upstream, clone := fixture(t)
		advanceUpstream(t, upstream, clone)
		mustWrite(t, filepath.Join(clone, "file.txt"), "uncommitted local edit\n")
		target := Remote(clone, "main", "origin")
		adv := target.AdvanceLocal(target.Ref())
		if adv.Outcome != Blocked {
			t.Fatalf("Outcome = %v (%s), want Blocked", adv.Outcome, adv.Reason)
		}
		reason := adv.SkipReason()
		for _, want := range []string{"uncommitted changes", "merge --ff-only", ManPage} {
			if !strings.Contains(reason, want) {
				t.Errorf("SkipReason %q lacks %q", reason, want)
			}
		}
		if got, _ := os.ReadFile(filepath.Join(clone, "file.txt")); string(got) != "uncommitted local edit\n" {
			t.Error("the operator's uncommitted edit was touched")
		}
	})
}

func TestSelfLand(t *testing.T) {
	_, clone := fixture(t)
	mustGit(t, clone, "checkout", "-q", "-b", "feature")
	mustGit(t, clone, "commit", "-q", "--allow-empty", "-m", "feature work")
	sha := mustGit(t, clone, "rev-parse", "HEAD")
	mustGit(t, clone, "checkout", "-q", "main")

	self := Self(clone, "main")
	if _, err := self.Land("", sha); err != nil {
		t.Fatalf("self Land: %v", err)
	}
	if got := localMain(t, clone); got != sha {
		t.Errorf("local main = %q, want the landed %q", got, sha)
	}
	if adv := self.AdvanceLocal(sha); adv.Skipped() {
		t.Errorf("self AdvanceLocal skipped: %s", adv.Reason)
	}
}

func TestSelfLandRefusesNonFastForward(t *testing.T) {
	_, clone := fixture(t)
	base := localMain(t, clone)
	mustGit(t, clone, "commit", "-q", "--allow-empty", "-m", "on main")
	mustGit(t, clone, "checkout", "-q", "-b", "side", base)
	mustGit(t, clone, "commit", "-q", "--allow-empty", "-m", "on side")
	side := mustGit(t, clone, "rev-parse", "HEAD")
	mustGit(t, clone, "checkout", "-q", "main")

	if _, err := Self(clone, "main").Land("", side); err == nil {
		t.Fatal("a non-fast-forward self landing must be an error")
	}
}

func TestSelfFetchIsNoop(t *testing.T) {
	if _, err := Self("/nonexistent", "main").Fetch(context.Background(), "/nonexistent"); err != nil {
		t.Errorf("self Fetch: %v", err)
	}
}
