package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/remote"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// TestCompleteWorktreeTargetsInRepoSorted: when cwd is inside a git
// repo, the completer scopes to that repo's sessions and orders them
// active-first, alphabetical-second.
func TestCompleteWorktreeTargetsInRepoSorted(t *testing.T) {
	testgit.RequireGit(t)
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repoA := filepath.Join(root, "alpha")
	repoB := filepath.Join(root, "beta")
	for _, r := range []string{repoA, repoB} {
		testgit.MustInit(t, r)
	}

	// Two sessions in repoA (one inactive, one active), one in repoB.
	live := filepath.Join(repoA, ".worktrees", "active-feature")
	stale := filepath.Join(repoA, ".worktrees", "stale-feature")
	other := filepath.Join(repoB, ".worktrees", "other")
	for _, p := range []string{live, stale, other} {
		testgit.MustWorktreeAdd(t, repoOf(p), p, filepath.Base(p))
	}

	for _, st := range []session.State{
		{
			RepoPath:     repoA,
			WorktreePath: stale,
			Branch:       "stale-feature",
			SessionState: session.StateInactive,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		},
		{
			RepoPath:     repoA,
			WorktreePath: live,
			Branch:       "active-feature",
			PID:          os.Getpid(), // the test process itself — definitely alive
			SessionState: session.StateActive,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		},
		{
			RepoPath:     repoB,
			WorktreePath: other,
			Branch:       "other",
			SessionState: session.StateInactive,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		},
	} {
		if err := session.Write(st); err != nil {
			t.Fatal(err)
		}
	}

	t.Chdir(repoA)
	got := completeWorktreeTargets()

	if _, ok := got["other"]; ok {
		t.Errorf("completer leaked repoB session 'other' into repoA scope: %v", got)
	}
	if len(got) != 2 {
		t.Errorf("got %d entries, want 2: %v", len(got), got)
	}

	// Labels match the picker's Detail format:
	// "<state> · <relative-time> · @<branch> · <repo>".
	want := map[string]string{
		"active-feature": "active · just now · @active-feature · alpha",
		"stale-feature":  "inactive · just now · @stale-feature · alpha",
	}
	for id, label := range want {
		if got[id] != label {
			t.Errorf("label for %q: got %q, want %q", id, got[id], label)
		}
	}
}

// TestCompleteWorktreeTargetsOutsideRepoIncludesRepoBasenameInLabel:
// outside any repo, every non-abandoned session is offered, with the
// repo basename appended to the label so duplicates across repos
// disambiguate.
func TestCompleteWorktreeTargetsOutsideRepoIncludesRepoBasenameInLabel(t *testing.T) {
	testgit.RequireGit(t)
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repoA := filepath.Join(root, "alpha")
	repoB := filepath.Join(root, "beta")
	for _, r := range []string{repoA, repoB} {
		testgit.MustInit(t, r)
	}

	wtA := filepath.Join(repoA, ".worktrees", "shared-name")
	wtB := filepath.Join(repoB, ".worktrees", "shared-name")
	testgit.MustWorktreeAdd(t, repoA, wtA, "shared-a")
	testgit.MustWorktreeAdd(t, repoB, wtB, "shared-b")

	for _, st := range []session.State{
		{
			RepoPath:     repoA,
			WorktreePath: wtA,
			Branch:       "shared-a",
			Description:  "alpha description",
			SessionState: session.StateInactive,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		},
		{
			RepoPath:     repoB,
			WorktreePath: wtB,
			Branch:       "shared-b",
			SessionState: session.StateInactive,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		},
	} {
		if err := session.Write(st); err != nil {
			t.Fatal(err)
		}
	}

	// Pick a directory outside any git repo and set GIT_CEILING_DIRECTORIES
	// so DetectRepo can't accidentally walk up to a host repo.
	outside := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", outside)
	t.Chdir(outside)

	got := completeWorktreeTargets()
	if len(got) != 1 {
		t.Errorf("got %d entries, want 1 (both sessions share the worktree dirname): %v", len(got), got)
	}
	for _, label := range got {
		if !strings.Contains(label, "· alpha") && !strings.Contains(label, "· beta") {
			t.Errorf("outside-repo label %q is missing repo basename", label)
		}
	}
}

func repoOf(wtPath string) string {
	return filepath.Dir(filepath.Dir(wtPath))
}

// TestCompleteWorktreeTargetsNestedRepos: from a repo that contains
// nested repos (~/eng over ~/eng/repos/*), completion offers the
// containing repo's sessions by bare dirname and the nested repos'
// sessions by session key — the same strings `sc list` prints, which
// FindByTarget accepts. Bare dirnames are only unambiguous within one
// repo, so nested-repo sessions must NOT appear under their bare name.
func TestCompleteWorktreeTargetsNestedRepos(t *testing.T) {
	testgit.RequireGit(t)
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	eng := filepath.Join(root, "eng")
	alpha := filepath.Join(eng, "repos", "alpha")
	for _, r := range []string{eng, alpha} {
		testgit.MustInit(t, r)
	}

	engWT := filepath.Join(eng, ".worktrees", "eng-feature")
	alphaWT := filepath.Join(alpha, ".worktrees", "feature-a")
	testgit.MustWorktreeAdd(t, eng, engWT, "eng-feature")
	testgit.MustWorktreeAdd(t, alpha, alphaWT, "feature-a")

	for _, st := range []session.State{
		{
			RepoPath:     eng,
			WorktreePath: engWT,
			Branch:       "eng-feature",
			SessionKey:   "eng/eng-feature",
			SessionState: session.StateInactive,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		},
		{
			RepoPath:     alpha,
			WorktreePath: alphaWT,
			Branch:       "feature-a",
			SessionKey:   "alpha/feature-a",
			SessionState: session.StateInactive,
			Entrypoint:   []string{"/bin/sh"},
			StartedAt:    time.Now().UTC(),
		},
	} {
		if err := session.Write(st); err != nil {
			t.Fatal(err)
		}
	}

	t.Chdir(eng)
	got := completeWorktreeTargets()

	if _, ok := got["eng-feature"]; !ok {
		t.Errorf("containing repo's session missing under bare dirname: %v", got)
	}
	if _, ok := got["alpha/feature-a"]; !ok {
		t.Errorf("nested repo's session missing under session key: %v", got)
	}
	if _, ok := got["feature-a"]; ok {
		t.Errorf("nested repo's session must not be offered as a bare dirname: %v", got)
	}
	if len(got) != 2 {
		t.Errorf("got %d entries, want 2: %v", len(got), got)
	}
}

// TestCompleteRemoteTargetsCacheOnly: remote completion entries come from
// the per-remote cache files ONLY. A recording `ssh` stub sits first on
// PATH; if completion ever networks, the stub's record file appears and
// the test fails — completion must stay instant and offline-safe.
func TestCompleteRemoteTargetsCacheOnly(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	stubDir := t.TempDir()
	record := filepath.Join(stubDir, "ssh-invoked")
	script := "#!/bin/sh\necho \"$@\" >> " + record + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(stubDir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub ssh: %v", err)
	}
	t.Setenv("PATH", stubDir+":"+os.Getenv("PATH"))

	if err := remote.WriteCache("devbox", []session.ListRow{
		{ID: "crisp-catalpa", SessionKey: "spinclass/crisp-catalpa", State: "active", Description: "fix login bug", Repo: "spinclass"},
		{ID: "molten-mango", SessionKey: "clown/molten-mango", State: "inactive", Description: "", Repo: "clown"},
	}); err != nil {
		t.Fatalf("seed devbox cache: %v", err)
	}

	got := completeRemoteTargets([]sweatfile.Remote{
		{Name: "devbox", SSH: "devbox.example"},
		{Name: "lab"}, // never listed: no cache file, silently no entries
	})

	// Labels match the picker's remote Detail format:
	// "remote(<name>) · <state> · cached".
	want := map[string]string{
		"devbox:crisp-catalpa": "remote(devbox) · active · cached",
		"devbox:molten-mango":  "remote(devbox) · inactive · cached",
	}
	if len(got) != len(want) {
		t.Errorf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for key, label := range want {
		if got[key] != label {
			t.Errorf("entry %q: got %q, want %q", key, got[key], label)
		}
	}

	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Errorf("completion invoked ssh (record file exists, stat err = %v) — must be cache-only", err)
	}
}

// TestCompleteRemoteTargetsNoRemotes: no configured remotes (including a
// failed config load upstream, which degrades to nil) yields no entries
// and never errors.
func TestCompleteRemoteTargetsNoRemotes(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if got := completeRemoteTargets(nil); len(got) != 0 {
		t.Errorf("nil remotes: got %v, want no entries", got)
	}
}

// TestRemoteAttachPlanRejectsNoAttach: --no-attach has no remote meaning
// (there is nothing local to skip attaching to), so combining it with a
// routed remote target must error rather than silently attach.
func TestRemoteAttachPlanRejectsNoAttach(t *testing.T) {
	remotes := []sweatfile.Remote{{Name: "devbox", SSH: "sasha@devbox.lan"}}

	argv, handled, err := remoteAttachPlan("devbox:crisp-catalpa", true, remotes)
	if !handled {
		t.Fatal("remote target with --no-attach: want handled=true (routed), got fallthrough")
	}
	if err == nil {
		t.Fatalf("remote target with --no-attach: want error, got argv %v", argv)
	}

	// Without the flag the same target routes normally.
	argv, handled, err = remoteAttachPlan("devbox:crisp-catalpa", false, remotes)
	if !handled || err != nil || len(argv) == 0 {
		t.Fatalf("remote target without flag: handled=%v err=%v argv=%v", handled, err, argv)
	}

	// Local targets are never handled, flag or not.
	if _, handled, _ := remoteAttachPlan("crisp-catalpa", true, remotes); handled {
		t.Fatal("plain local target: want fallthrough, got handled")
	}
}

// TestRemoteResumeArgv: the resume routing seam. A host:-prefixed target
// whose prefix names a configured remote yields the attach argv (default
// ssh template, or the remote's own template with {ssh}/{id} substituted);
// anything else — plain local targets, unconfigured prefixes, no remotes —
// falls through to local resolution (ok=false).
func TestRemoteResumeArgv(t *testing.T) {
	remotes := []sweatfile.Remote{
		{Name: "devbox", SSH: "sasha@devbox.lan"},
		{Name: "lab", Attach: []string{"mosh", "{ssh}", "--", "spinclass", "resume", "{id}"}},
	}

	cases := []struct {
		name    string
		target  string
		remotes []sweatfile.Remote
		want    []string
		wantOK  bool
	}{
		{
			name:    "match with default template uses Dest()",
			target:  "devbox:crisp-catalpa",
			remotes: remotes,
			want:    []string{"ssh", "-t", "sasha@devbox.lan", "spinclass", "resume", "crisp-catalpa"},
			wantOK:  true,
		},
		{
			name:    "match with custom template substitutes {ssh} and {id}",
			target:  "lab:molten-mango",
			remotes: remotes,
			want:    []string{"mosh", "lab", "--", "spinclass", "resume", "molten-mango"},
			wantOK:  true,
		},
		{
			name:    "parseable prefix but no configured remote falls through",
			target:  "other:crisp-catalpa",
			remotes: remotes,
			wantOK:  false,
		},
		{
			name:    "plain local target falls through",
			target:  "crisp-catalpa",
			remotes: remotes,
			wantOK:  false,
		},
		{
			name:   "no remotes configured falls through",
			target: "devbox:crisp-catalpa",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := remoteResumeArgv(tc.target, tc.remotes)
			if ok != tc.wantOK {
				t.Fatalf("ok: got %v, want %v (argv %v)", ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				if got != nil {
					t.Fatalf("fallthrough must return nil argv, got %v", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("argv: got %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("argv[%d]: got %q, want %q (full %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// TestRejectRemoteTarget: close and merge are v1-unsupported for remote
// targets. A host:-prefixed target naming a configured remote yields the
// exact rejection error; everything else (unconfigured prefix, plain
// target, empty target) is nil — behavior unchanged.
func TestRejectRemoteTarget(t *testing.T) {
	remotes := []sweatfile.Remote{{Name: "devbox"}}

	if err := rejectRemoteTarget("devbox:crisp-catalpa", remotes); err == nil {
		t.Fatal("configured remote target: want rejection error, got nil")
	} else if err.Error() != "remote targets support resume only (v1)" {
		t.Fatalf("rejection text: got %q, want %q", err.Error(), "remote targets support resume only (v1)")
	}

	for _, target := range []string{"other:crisp-catalpa", "crisp-catalpa", ""} {
		if err := rejectRemoteTarget(target, remotes); err != nil {
			t.Errorf("target %q: want nil (unchanged behavior), got %v", target, err)
		}
	}

	if err := rejectRemoteTarget("devbox:crisp-catalpa", nil); err != nil {
		t.Errorf("no remotes configured: want nil, got %v", err)
	}
}
