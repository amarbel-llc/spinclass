package git

import (
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/store"
)

// newRepoWithRemoteTracking is newRepo plus a configured remote origin and the
// remote-tracking refs origin/main and origin/feature (no network involved).
func newRepoWithRemoteTracking(t *testing.T) string {
	t.Helper()
	repo := newRepo(t)
	mustRun(t, repo, "remote", "add", "origin", "/nonexistent")
	mustRun(t, repo, "update-ref", "refs/remotes/origin/main", "main")
	mustRun(t, repo, "update-ref", "refs/remotes/origin/feature", "main")
	return repo
}

func refSnapshot(t *testing.T, repo string) string {
	t.Helper()
	return gitOutput(t, repo, "for-each-ref", "--format=%(refname) %(objectname)")
}

func TestCheckoutNewBranchForms(t *testing.T) {
	cases := []struct {
		name     string
		args     map[string]any
		wantErr  string
		wantHead string
		wantAt   string // rev the new branch must point at
	}{
		{"name and start_point", map[string]any{"new_branch": true, "name": "fresh", "start_point": "origin/main"}, "", "fresh", "origin/main"},
		{"name defaults to HEAD", map[string]any{"new_branch": true, "name": "fresh"}, "", "fresh", "HEAD"},
		{"legacy ref-only plain name", map[string]any{"new_branch": true, "ref": "fresh"}, "", "fresh", "HEAD"},
		{"legacy ref-only remote-tracking", map[string]any{"new_branch": true, "ref": "origin/main"}, "pass the new branch as name", "main", ""},
		{"legacy ref-only unknown under a remote name", map[string]any{"new_branch": true, "ref": "origin/nothere"}, "pass the new branch as name", "main", ""},
		{"legacy ref-only refs path", map[string]any{"new_branch": true, "ref": "refs/remotes/origin/main"}, "pass the new branch as name", "main", ""},
		{"name equal to remote-tracking", map[string]any{"new_branch": true, "name": "origin/feature"}, "remote-tracking", "main", ""},
		{"name under a remote name", map[string]any{"new_branch": true, "name": "origin/brand-new"}, "configured remote", "main", ""},
		{"name and ref both", map[string]any{"new_branch": true, "name": "fresh", "ref": "origin/main"}, "ref is not used", "main", ""},
		{"name without new_branch", map[string]any{"name": "fresh"}, "only used with new_branch", "main", ""},
		{"plain checkout still takes ref", map[string]any{"ref": "topic"}, "", "topic", "topic"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := newRepoWithRemoteTracking(t)
			before := refSnapshot(t, repo)
			out, err := do(t, repo, "branch", "checkout", c.args)
			if c.wantErr != "" {
				wantErrContaining(t, err, c.wantErr)
				if after := refSnapshot(t, repo); after != before {
					t.Fatalf("refs changed after a refused checkout:\nbefore:\n%s\nafter:\n%s", before, after)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if got := currentBranch(t, repo); got != c.wantHead {
				t.Fatalf("current branch %q, want %q", got, c.wantHead)
			}
			if c.wantAt != "" {
				if got, want := gitOutput(t, repo, "rev-parse", c.wantHead), gitOutput(t, repo, "rev-parse", c.wantAt); got != want {
					t.Fatalf("%s at %s, want %s", c.wantHead, got, want)
				}
			}
			_ = out
		})
	}
}

func TestCheckoutNewBranchStartsFromStartPoint(t *testing.T) {
	repo := newRepoWithRemoteTracking(t)
	commitFile(t, repo, "b.txt", "b\n", "second")
	out, err := do(t, repo, "branch", "checkout", map[string]any{"new_branch": true, "name": "from-tag", "start_point": "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("checkout new_branch must return non-empty text")
	}
	if got, want := gitOutput(t, repo, "rev-parse", "from-tag"), gitOutput(t, repo, "rev-parse", "v1"); got != want {
		t.Fatalf("from-tag at %s, want v1 %s", got, want)
	}
}

func TestBranchCreateForms(t *testing.T) {
	repo := newRepoWithRemoteTracking(t)
	out, err := do(t, repo, "branch", "create", map[string]any{"name": "conform", "start_point": "origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "created branch conform at ") {
		t.Fatalf("create text %q", out)
	}
	if got, want := gitOutput(t, repo, "rev-parse", "conform"), gitOutput(t, repo, "rev-parse", "origin/main"); got != want {
		t.Fatalf("conform at %s, want %s", got, want)
	}
	before := refSnapshot(t, repo)
	for _, name := range []string{"origin/feature", "origin/new", "refs/heads/x"} {
		_, err := do(t, repo, "branch", "create", map[string]any{"name": name})
		wantErrContaining(t, err, "refusing to create a branch named")
	}
	if after := refSnapshot(t, repo); after != before {
		t.Fatalf("refs changed after refused creates:\n%s\n%s", before, after)
	}
}

func TestBranchDeleteForms(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		wantErr string
	}{
		{"short name", "doomed", ""},
		{"refs/heads form", "refs/heads/doomed", ""},
		{"remote-tracking path", "refs/remotes/origin/feature", "other refs/ prefixes are refused"},
		{"tag path", "refs/tags/v1", "other refs/ prefixes are refused"},
		{"heads without refs", "heads/doomed", "not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := newRepoWithRemoteTracking(t)
			mustRun(t, repo, "branch", "doomed")
			before := refSnapshot(t, repo)
			out, err := do(t, repo, "branch", "delete", map[string]any{"name": c.target})
			if c.wantErr != "" {
				wantErrContaining(t, err, c.wantErr)
				if after := refSnapshot(t, repo); after != before {
					t.Fatalf("refs changed after a refused delete")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(out) == "" {
				t.Fatal("delete must return non-empty text")
			}
			if refs := refSnapshot(t, repo); strings.Contains(refs, "refs/heads/doomed") {
				t.Fatalf("doomed still exists:\n%s", refs)
			}
		})
	}
}

func TestBranchDeleteOfLocalBranchNamedLikeRemoteTracking(t *testing.T) {
	repo := newRepoWithRemoteTracking(t)
	// A local branch literally named origin/feature (the #393 canary leftover).
	mustRun(t, repo, "update-ref", "refs/heads/origin/feature", "main")
	for _, form := range []string{"origin/feature", "refs/heads/origin/feature"} {
		mustRun(t, repo, "update-ref", "refs/heads/origin/feature", "main")
		if _, err := do(t, repo, "branch", "delete", map[string]any{"name": form}); err != nil {
			t.Fatalf("%s: %v", form, err)
		}
		if refs := refSnapshot(t, repo); strings.Contains(refs, "refs/heads/origin/feature") || !strings.Contains(refs, "refs/remotes/origin/feature") {
			t.Fatalf("%s: wrong ref deleted:\n%s", form, refs)
		}
	}
}

func TestClassifyBranchCreateCheckoutDeleteForms(t *testing.T) {
	f := newFixture(t)
	runGit(t, f.repo, "remote", "add", "origin", "/nonexistent")
	runGit(t, f.repo, "update-ref", "refs/remotes/origin/main", "main")
	runGit(t, f.repo, "update-ref", "refs/remotes/origin/glad-buckeye--r", "main")
	runCases(t, f, []classifyCase{
		// ownership of a new-branch checkout is the new branch's name
		{name: "checkout new by name, owned", tool: "branch", sub: "checkout", args: map[string]any{"new_branch": true, "name": "glad-buckeye--n", "start_point": "origin/main"}, want: store.Allow},
		{name: "checkout new by name, foreign", tool: "branch", sub: "checkout", args: map[string]any{"new_branch": true, "name": "B2--n", "start_point": "glad-buckeye--topic"}, want: store.Ask},
		{name: "checkout new, owned start but foreign name", tool: "branch", sub: "checkout", args: map[string]any{"new_branch": true, "name": "foo", "start_point": "glad-buckeye"}, want: store.Ask},
		{name: "checkout new, owned name but foreign start is fine", tool: "branch", sub: "checkout", args: map[string]any{"new_branch": true, "name": "glad-buckeye--n", "start_point": "B2"}, want: store.Allow},
		{name: "checkout new legacy ref, owned", tool: "branch", sub: "checkout", args: map[string]any{"new_branch": true, "ref": "glad-buckeye--n"}, want: store.Allow},
		{name: "checkout new legacy ref, remote-tracking shaped", tool: "branch", sub: "checkout", args: map[string]any{"new_branch": true, "ref": "origin/main"}, want: store.Ask},
		{name: "checkout new, name and ref both", tool: "branch", sub: "checkout", args: map[string]any{"new_branch": true, "name": "glad-buckeye--n", "ref": "main"}, want: store.Ask},
		{name: "checkout new, name equal to remote-tracking", tool: "branch", sub: "checkout", args: map[string]any{"new_branch": true, "name": "origin/glad-buckeye--r"}, want: store.Ask},
		{name: "checkout new, refs path name", tool: "branch", sub: "checkout", args: map[string]any{"new_branch": true, "name": "refs/heads/glad-buckeye--n"}, want: store.Ask},
		{name: "create remote-tracking shaped", tool: "branch", sub: "create", args: map[string]any{"name": "origin/glad-buckeye--r"}, want: store.Ask},
		// delete: short and refs/heads/ forms judged on the same ref
		{name: "delete short owned", tool: "branch", sub: "delete", args: map[string]any{"name": "glad-buckeye--topic"}, want: store.Allow},
		{name: "delete refs/heads owned", tool: "branch", sub: "delete", args: map[string]any{"name": "refs/heads/glad-buckeye--topic"}, want: store.Allow},
		{name: "delete short unowned", tool: "branch", sub: "delete", args: map[string]any{"name": "B2"}, want: store.Ask},
		{name: "delete refs/heads unowned", tool: "branch", sub: "delete", args: map[string]any{"name": "refs/heads/B2"}, want: store.Ask},
		{name: "delete remote-tracking path", tool: "branch", sub: "delete", args: map[string]any{"name": "refs/remotes/origin/glad-buckeye--r"}, want: store.Ask},
		{name: "delete tag path", tool: "branch", sub: "delete", args: map[string]any{"name": "refs/tags/glad-buckeye--t"}, want: store.Ask},
		{name: "delete double refs/heads", tool: "branch", sub: "delete", args: map[string]any{"name": "refs/heads/refs/heads/glad-buckeye--topic"}, want: store.Ask},
	})
}
