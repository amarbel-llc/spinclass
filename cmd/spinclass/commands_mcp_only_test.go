package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/attestation"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/job"
	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
	"code.linenisgreat.com/spinclass/internal/testgit"
)

// TestResolveGatedSession exercises the resolved/reject outcomes of the
// extracted merge/check session-gate preamble: a worktree session
// (ok=true, gitErr=nil, implicit=false, repoPath/branch from git), a live
// implicit main-checkout session (ok=true, gitErr=nil, implicit=true,
// repoPath/branch from the state file), and a bare dir that is neither
// (ok=false with the canonical reject message). All three run with the
// attestation gate dormant — no [[pre-merge-skills]] in the loaded hierarchy —
// so the gate passes and the routing identity is what's under test.
//
// The third outcome — git-fail (ok=true, gitErr set, gs zero) — is not
// exercised here: it requires a cwd where worktree.IsWorktree is true but
// git.CommonDir/BranchCurrent then fails, which means a deliberately corrupt
// .git pointer that's brittle to construct. The merge handlers' fatality on
// gitErr is the only consumer of that outcome and is covered by the live merge
// path; the helper just forwards git's error verbatim.
//
// The gate's dormancy is load-bearing and non-obvious here: the sweatfile
// cascade walks the repo dir's ancestors up to $HOME, and $TMPDIR (and thus
// t.TempDir) points *inside* this session's worktree under ~/eng, whose
// sweatfile declares [[pre-merge-skills]]. To keep the gate dormant each subtest
// pins $HOME to the repo's own parent dir, so chainAncestors stops immediately
// and never reaches ~/eng — independent of where t.TempDir lands.
func TestResolveGatedSession(t *testing.T) {
	t.Run("worktree", func(t *testing.T) {
		testgit.RequireGit(t)
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		base := t.TempDir()
		t.Setenv("HOME", base) // bound the cascade walk at the repo's parent
		repo := filepath.Join(base, "repo")
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatalf("mkdir repo: %v", err)
		}
		testgit.MustInit(t, repo)
		wt := filepath.Join(repo, ".worktrees", "feature")
		testgit.MustWorktreeAdd(t, repo, wt, "feature")
		// resolveGatedSession reads the sweatfile hierarchy from cwd; chdir into
		// the worktree so the dormant-gate path resolves there.
		t.Chdir(wt)

		gs, failMsg, ok, gitErr := resolveGatedSession(wt)
		if !ok {
			t.Fatalf("expected ok, got reject: %q", failMsg)
		}
		if gitErr != nil {
			t.Errorf("worktree session: unexpected gitErr: %v", gitErr)
		}
		if gs.implicit {
			t.Errorf("worktree session: implicit = true, want false")
		}
		if gs.repoPath == "" {
			t.Errorf("worktree session: repoPath empty, want git-common-dir")
		}
		if gs.branch != "feature" {
			t.Errorf("worktree session: branch = %q, want feature", gs.branch)
		}
	})

	t.Run("implicit", func(t *testing.T) {
		testgit.RequireGit(t)
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		base := t.TempDir()
		t.Setenv("HOME", base) // dormant gate — see the TestResolveGatedSession doc comment
		// `git init` (not worktree add): .git is a DIRECTORY, so
		// worktree.IsWorktree is false and the implicit lookup fires — while
		// git.CommonDir still resolves to this repo (not the enclosing session
		// worktree TMPDIR lands in), keeping the cascade bounded by base/HOME.
		repo := filepath.Join(base, "repo")
		testgit.MustInit(t, repo)
		st := session.State{
			Kind:         session.KindImplicit,
			PID:          os.Getpid(), // alive: this test process
			SessionState: session.StateActive,
			RepoPath:     repo,
			WorktreePath: repo,
			Branch:       "master",
			SessionKey:   "myrepo/master-cafe1234",
		}
		if err := session.WriteImplicit(st, "cafe1234"); err != nil {
			t.Fatalf("WriteImplicit: %v", err)
		}
		t.Chdir(repo)

		gs, failMsg, ok, gitErr := resolveGatedSession(repo)
		if !ok {
			t.Fatalf("expected ok, got reject: %q", failMsg)
		}
		if gitErr != nil {
			t.Errorf("implicit session: unexpected gitErr: %v", gitErr)
		}
		if !gs.implicit {
			t.Errorf("implicit session: implicit = false, want true")
		}
		if gs.repoPath != repo {
			t.Errorf("implicit session: repoPath = %q, want %q", gs.repoPath, repo)
		}
		if gs.branch != "master" {
			t.Errorf("implicit session: branch = %q, want master", gs.branch)
		}
	})

	t.Run("reject", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		// Bare dir: not a worktree, no implicit session written. No $HOME pin is
		// needed here (unlike the other subtests) — the reject fires before the
		// attestation gate, so the sweatfile cascade that ~/eng's
		// [[pre-merge-skills]] would taint is never reached on this path.
		dir := t.TempDir()
		t.Chdir(dir)

		gs, failMsg, ok, gitErr := resolveGatedSession(dir)
		if ok {
			t.Fatalf("expected reject, got ok (gs=%+v)", gs)
		}
		if gitErr != nil {
			t.Errorf("reject: unexpected gitErr: %v", gitErr)
		}
		if failMsg != "not inside a worktree session" {
			t.Errorf("reject message = %q, want %q", failMsg, "not inside a worktree session")
		}
	})
}

// TestMergeAsyncRefusalPreservesAttestation pins spinclass#265 deliverable 2:
// a merge-this-session-async invoked while a background job is already running
// must refuse WITHOUT consuming the buffered pre-merge attestation. Before the
// fix, resolveGatedSession consumed the attestation (an active gate clears it)
// before the handler reached the already-running refusal, forcing a full
// re-attestation of unchanged content on the retry.
//
// The active gate is load-bearing: attestation.Check only consumes when
// [[pre-merge-skills]] is present, so a repo-root sweatfile declaring one is
// what makes the pre-fix consume reproduce. EvalSymlinks keeps the test's
// constructed paths in agreement with git's realpath output, so the attestation
// key (git.CommonDir(cwd), branch) the old path would have consumed is the same
// key this test writes and reads back.
func TestMergeAsyncRefusalPreservesAttestation(t *testing.T) {
	testgit.RequireGit(t)
	// Force clown off so job.Start neither allocates a ringmaster job nor
	// shells out — the blocking goroutine is all this test needs from it.
	t.Setenv("CLOWN_BIN", "")
	_ = os.Unsetenv("CLOWN_BIN")
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("evalsymlinks tempdir: %v", err)
	}
	t.Setenv("HOME", base) // bound the sweatfile cascade at the repo's parent

	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feature")
	testgit.MustWorktreeAdd(t, repo, wt, "feature")

	// Activate the attestation gate: a repo-root sweatfile declaring one
	// pre-merge skill. Without it, the pre-fix consume is a no-op and the
	// regression would not reproduce.
	sweat := "[[pre-merge-skills]]\nname = \"eng:code-reviewer\"\nrationale = \"Mandatory.\"\n"
	if err := os.WriteFile(filepath.Join(repo, "sweatfile"), []byte(sweat), 0o644); err != nil {
		t.Fatalf("write sweatfile: %v", err)
	}

	t.Chdir(wt)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Key the attestation exactly as the gate would: (git.CommonDir(cwd),
	// BranchCurrent(cwd)). These are the canonical values resolveGatedSession
	// feeds attestation.Check.
	repoPath, err := git.CommonDir(cwd)
	if err != nil {
		t.Fatalf("common dir: %v", err)
	}
	branch, err := git.BranchCurrent(cwd)
	if err != nil {
		t.Fatalf("branch: %v", err)
	}

	// Buffer a fresh attestation on a worktree session state findable by
	// (repoPath, branch).
	st := session.State{
		PID:          os.Getpid(),
		SessionState: session.StateActive,
		RepoPath:     repoPath,
		WorktreePath: filepath.Join(repoPath, ".worktrees", branch),
		Branch:       branch,
		SessionKey:   filepath.Base(repoPath) + "/" + branch,
		StartedAt:    time.Now().UTC(),
		PreMergeAttestation: &session.PreMergeAttestation{
			RecordedAt: time.Now().UTC(),
			Skills:     []session.AttestedSkill{{Name: "eng:code-reviewer", Used: true, Reasoning: "reviewed"}},
		},
	}
	if err := session.Write(st); err != nil {
		t.Fatalf("write session state: %v", err)
	}

	// Occupy the single background-job slot with a job that blocks until the
	// test releases it, so job.IsRunning(cwd) is true across the handler call.
	release := make(chan struct{})
	if _, err := job.Start(cwd, job.KindMerge, false, "test-blocker", func(_ context.Context, _ io.Writer) (string, bool) {
		<-release
		return "", false
	}); err != nil {
		t.Fatalf("start blocking job: %v", err)
	}
	t.Cleanup(func() {
		close(release)
		<-job.WaitDone(cwd)
	})

	res, herr := handleMergeThisSessionAsync(context.Background(), json.RawMessage(`{}`), nil)
	if herr != nil {
		t.Fatalf("handler returned a transport error: %v", herr)
	}
	if !res.IsErr {
		t.Fatalf("expected an error result (job already running), got success: %s", res.Text)
	}
	if !strings.Contains(res.Text, "already running") {
		t.Errorf("error text = %q, want it to name the already-running refusal", res.Text)
	}

	// The heart of deliverable 2: the refusal must NOT have consumed the
	// buffered attestation.
	got, rerr := session.Read(repoPath, branch)
	if rerr != nil {
		t.Fatalf("read session state after refusal: %v", rerr)
	}
	if got.PreMergeAttestation == nil {
		t.Fatal("attestation was consumed by a refused merge-async (spinclass#265 deliverable 2 regression)")
	}
}

// TestCurrentSessionKeyImplicitFallback verifies that when not inside a
// worktree and $SPINCLASS_SESSION_ID is unset, currentSessionKey resolves the
// key from a live implicit (main-checkout) session's state file rather than
// erroring — the path chat-send/chat-read need from a main checkout (#118).
func TestCurrentSessionKeyImplicitFallback(t *testing.T) {
	t.Setenv("SPINCLASS_SESSION_ID", "") // force the fallback path
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	// A bare tempdir has no .git file, so worktree.IsWorktree is false and the
	// implicit fallback fires before any git call.
	repo := t.TempDir()
	st := session.State{
		Kind:         session.KindImplicit,
		PID:          os.Getpid(),
		SessionState: session.StateActive,
		RepoPath:     repo,
		WorktreePath: repo,
		Branch:       "master",
		SessionKey:   "myrepo/master-cafe1234",
	}
	if err := session.WriteImplicit(st, "cafe1234"); err != nil {
		t.Fatalf("WriteImplicit: %v", err)
	}
	t.Chdir(repo)

	key, err := currentSessionKey()
	if err != nil {
		t.Fatalf("expected implicit fallback to resolve, got error: %v", err)
	}
	if key != "myrepo/master-cafe1234" {
		t.Fatalf("key = %q, want myrepo/master-cafe1234", key)
	}
}

// TestCurrentSessionKeyHealsUntrackedWorktree pins #163: from a worktree
// with no spinclass state (harness run directly, never via sc start/resume),
// chat/spawn sender resolution lazily registers the worktree session — the
// worktree sibling of #141's implicit lazy materialization — rather than
// failing on the missing index. The session must persist so it is a listable,
// addressable reply target.
func TestCurrentSessionKeyHealsUntrackedWorktree(t *testing.T) {
	testgit.RequireGit(t)
	t.Setenv("SPINCLASS_SESSION_ID", "") // force worktree-branch derivation
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "myrepo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")
	t.Chdir(wt)

	// Precondition: no state for this worktree.
	if _, err := session.Read(repo, "feat"); err == nil {
		t.Fatal("precondition: expected no session state for the fresh worktree")
	}

	key, err := currentSessionKey()
	if err != nil {
		t.Fatalf("currentSessionKey from untracked worktree: %v", err)
	}
	if key != "myrepo/feat" {
		t.Errorf("key = %q, want myrepo/feat", key)
	}
	// Persisted: the worktree is now a tracked, addressable session.
	if st, rerr := session.Read(repo, "feat"); rerr != nil || st.SessionKey != "myrepo/feat" {
		t.Errorf("expected the worktree session registered, got %v err=%v", st, rerr)
	}
}

// TestCurrentSessionKeyNoSessionStillErrors verifies the fallbacks do not mask
// the genuine "not a session" case: outside a worktree, env unset, no live
// implicit session, and a cwd that is not even a git repo (so lazy
// materialization is gated too), currentSessionKey still errors.
func TestCurrentSessionKeyNoSessionStillErrors(t *testing.T) {
	t.Setenv("SPINCLASS_SESSION_ID", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repo := t.TempDir() // not a git repo, no implicit session written
	t.Chdir(repo)

	if _, err := currentSessionKey(); err == nil {
		t.Fatal("expected error when no implicit session and not a worktree")
	}
}

// TestAppendNotPushedNote pins #158's result-text contract: a successful
// local-only merge (gitSync=false) must SAY it didn't push, so worker
// completion reports stop implying origin has the work.
func TestAppendNotPushedNote(t *testing.T) {
	const text = "✓ rebase x\n✓ merge x"
	t.Run("local-only success appends", func(t *testing.T) {
		got := appendNotPushedNote(text, false, nil)
		if !strings.Contains(got, "NOT pushed") || !strings.Contains(got, "local_only") {
			t.Errorf("note missing: %q", got)
		}
	})
	t.Run("pushed (gitSync true) appends nothing", func(t *testing.T) {
		if got := appendNotPushedNote(text, true, nil); got != text {
			t.Errorf("got %q, want unchanged", got)
		}
	})
	t.Run("failed merge appends nothing", func(t *testing.T) {
		if got := appendNotPushedNote(text, false, errTest); got != text {
			t.Errorf("got %q, want unchanged", got)
		}
	})
	t.Run("empty text appends nothing", func(t *testing.T) {
		if got := appendNotPushedNote("", false, nil); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

// errTest is a sentinel for table cases needing any non-nil error.
var errTest = errors.New("test error")

// lazyKeyFixture builds a main-checkout git repo whose parent dir is $HOME
// (bounding the sweatfile cascade the disable-implicit-sessions gate walks)
// and chdirs into it — the cwd shape currentSessionKey's lazy
// materialization path (#141) requires.
func lazyKeyFixture(t *testing.T) (repo string) {
	t.Helper()
	testgit.RequireGit(t)
	t.Setenv("SPINCLASS_SESSION_ID", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	base := t.TempDir()
	t.Setenv("HOME", base)
	repo = filepath.Join(base, "repo")
	testgit.MustInit(t, repo)
	t.Chdir(repo)
	return repo
}

// implicitStateFiles returns the state-<rand>.json paths at the checkout.
func implicitStateFiles(t *testing.T, checkout string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(checkout, ".spinclass", "state-*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return matches
}

// TestCurrentSessionKeyLazyMaterialization verifies the #141 path: at a
// main-checkout repo root with no live implicit session (the SessionStart
// hook never fired), currentSessionKey materializes one lazily — the key is
// <repo>/<rand>, the state file records this process as the liveness PID,
// and repeated calls return the identical key (chat-send/chat-read sender
// identity and the read cursor depend on that stability).
func TestCurrentSessionKeyLazyMaterialization(t *testing.T) {
	repo := lazyKeyFixture(t)

	key, err := currentSessionKey()
	if err != nil {
		t.Fatalf("expected lazy materialization to resolve, got error: %v", err)
	}
	if !strings.HasPrefix(key, "repo/") || len(key) == len("repo/") {
		t.Fatalf("key = %q, want repo/<rand>", key)
	}

	files := implicitStateFiles(t, repo)
	if len(files) != 1 {
		t.Fatalf("state files = %v, want exactly one", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var st session.State
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("unmarshal state file: %v", err)
	}
	if st.SessionKey != key {
		t.Errorf("state SessionKey = %q, want %q", st.SessionKey, key)
	}
	if st.PID != os.Getpid() {
		t.Errorf("state PID = %d, want this process %d", st.PID, os.Getpid())
	}
	if st.Kind != session.KindImplicit {
		t.Errorf("state Kind = %q, want %q", st.Kind, session.KindImplicit)
	}

	key2, err := currentSessionKey()
	if err != nil {
		t.Fatalf("second call errored: %v", err)
	}
	if key2 != key {
		t.Fatalf("second call key = %q, want %q (identity must be stable)", key2, key)
	}
}

// TestCurrentSessionKeyLazySticksAcrossSiblingStateFile verifies the lazily
// materialized identity survives a sibling state file appearing later (e.g. a
// SessionStart hook re-fire on resume/clear/compact): FindImplicitAtCwd picks
// the first live file in glob order, so without the in-process cache a
// lexically-smaller sibling rand would flip the sender identity mid-session.
func TestCurrentSessionKeyLazySticksAcrossSiblingStateFile(t *testing.T) {
	repo := lazyKeyFixture(t)

	key, err := currentSessionKey()
	if err != nil {
		t.Fatalf("expected lazy materialization to resolve, got error: %v", err)
	}

	sibling := session.State{
		Kind:         session.KindImplicit,
		PID:          os.Getpid(), // alive: this test process
		SessionState: session.StateActive,
		RepoPath:     repo,
		WorktreePath: repo,
		Branch:       "main",
		SessionKey:   "repo/00000000000000000000", // sorts before any hex rand
	}
	if err := session.WriteImplicit(sibling, "00000000000000000000"); err != nil {
		t.Fatalf("WriteImplicit sibling: %v", err)
	}

	key2, err := currentSessionKey()
	if err != nil {
		t.Fatalf("call after sibling appeared errored: %v", err)
	}
	if key2 != key {
		t.Fatalf("key flipped to %q after sibling state file, want %q", key2, key)
	}
}

// TestCurrentSessionKeyLazyRespectsDisableKnob verifies lazy materialization
// honors the FDR 0014 rollback knob exactly like the SessionStart hook: with
// [hooks].disable-implicit-sessions set in the cascade, currentSessionKey
// errors and writes nothing.
func TestCurrentSessionKeyLazyRespectsDisableKnob(t *testing.T) {
	repo := lazyKeyFixture(t)
	sweat := "[hooks]\ndisable-implicit-sessions = true\n"
	if err := os.WriteFile(filepath.Join(repo, "sweatfile"), []byte(sweat), 0o644); err != nil {
		t.Fatalf("write sweatfile: %v", err)
	}

	if _, err := currentSessionKey(); err == nil {
		t.Fatal("expected error with disable-implicit-sessions set")
	}
	if files := implicitStateFiles(t, repo); len(files) != 0 {
		t.Fatalf("state files = %v, want none (knob must gate the write)", files)
	}
}

// TestCurrentSessionKeyLazyNotAtSubdir verifies the repo-root gate is shared
// with the SessionStart hook: a subdirectory of a main checkout neither
// resolves nor materializes (state files live only at checkout roots).
func TestCurrentSessionKeyLazyNotAtSubdir(t *testing.T) {
	repo := lazyKeyFixture(t)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	t.Chdir(sub)

	if _, err := currentSessionKey(); err == nil {
		t.Fatal("expected error at a checkout subdirectory")
	}
	if files := implicitStateFiles(t, repo); len(files) != 0 {
		t.Fatalf("state files = %v, want none", files)
	}
	if files := implicitStateFiles(t, sub); len(files) != 0 {
		t.Fatalf("state files under subdir = %v, want none", files)
	}
}

// TestHandleUpdateDescriptionImplicit verifies that
// update-this-session-description falls back to a live implicit
// (main-checkout) session when not inside a worktree, writing the description
// into its state-<rand>.json — the behavior FDR 0014 documents (#137).
func TestHandleUpdateDescriptionImplicit(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	// A bare tempdir has no .git file, so worktree.IsWorktree is false and the
	// implicit fallback fires before any git call.
	repo := t.TempDir()
	st := session.State{
		Kind:         session.KindImplicit,
		PID:          os.Getpid(),
		SessionState: session.StateActive,
		RepoPath:     repo,
		WorktreePath: repo,
		Branch:       "master",
		SessionKey:   "myrepo/master-cafe1234",
	}
	if err := session.WriteImplicit(st, "cafe1234"); err != nil {
		t.Fatalf("WriteImplicit: %v", err)
	}
	t.Chdir(repo)

	res, err := handleUpdateDescription(context.Background(), json.RawMessage(`{"description":"triage the flaky test"}`), nil)
	if err != nil {
		t.Fatalf("handleUpdateDescription: %v", err)
	}
	if res.IsErr {
		t.Fatalf("expected success for implicit session, got error result: %s", res.Text)
	}

	got, _, ferr := session.FindImplicitAtCwd(repo)
	if ferr != nil || got == nil {
		t.Fatalf("FindImplicitAtCwd after update: state=%v err=%v", got, ferr)
	}
	if got.Description != "triage the flaky test" {
		t.Errorf("persisted description = %q, want %q", got.Description, "triage the flaky test")
	}
}

// TestHandleUpdateDescriptionNoSessionStillErrors verifies the fallback does
// not mask the genuine "not a session" case: outside a worktree with no live
// implicit session, the tool still returns the canonical error.
func TestHandleUpdateDescriptionNoSessionStillErrors(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	dir := t.TempDir() // no implicit session written
	t.Chdir(dir)

	res, err := handleUpdateDescription(context.Background(), json.RawMessage(`{"description":"anything"}`), nil)
	if err != nil {
		t.Fatalf("handleUpdateDescription: %v", err)
	}
	if !res.IsErr {
		t.Fatalf("expected error result with no session, got success: %s", res.Text)
	}
	if !strings.Contains(res.Text, "not inside a worktree session") {
		t.Errorf("error text = %q, want it to contain %q", res.Text, "not inside a worktree session")
	}
}

func TestSummarizeHookCommand_SingleLine(t *testing.T) {
	got := summarizeHookCommand("just test")
	if got != "just test" {
		t.Errorf("got %q, want %q", got, "just test")
	}
}

func TestSummarizeHookCommand_LeadingTrailingWhitespace(t *testing.T) {
	got := summarizeHookCommand("  just test  ")
	if got != "just test" {
		t.Errorf("got %q, want %q", got, "just test")
	}
}

func TestSummarizeHookCommand_MultiLineFirstLineOnly(t *testing.T) {
	script := "set -euo pipefail\njust test\njust lint"
	got := summarizeHookCommand(script)
	if got != "set -euo pipefail ..." {
		t.Errorf("got %q, want %q", got, "set -euo pipefail ...")
	}
}

func TestSummarizeHookCommand_BlankLinesBeforeContent(t *testing.T) {
	got := summarizeHookCommand("\n\n  just test\n")
	if got != "just test" {
		t.Errorf("got %q, want %q", got, "just test")
	}
}

func TestSummarizeHookCommand_OnlyWhitespace(t *testing.T) {
	got := summarizeHookCommand("   \n\n  ")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestBuildMergeThisSessionDescription_NoHook(t *testing.T) {
	got := buildMergeThisSessionDescription("")
	if strings.Contains(got, "[hooks].pre-merge command runs") {
		t.Errorf("description should not advertise pre-merge command when hook is unset:\n%s", got)
	}
	if !strings.Contains(got, "Merge the current session's worktree") {
		t.Errorf("description missing baseline text:\n%s", got)
	}
	if !strings.Contains(got, "plain verdict lines") {
		t.Errorf("description should advertise the plain verdict-line result shape (TAP is retired for merge/check):\n%s", got)
	}
	if strings.Contains(got, "TAP") {
		t.Errorf("description still claims TAP output:\n%s", got)
	}
}

func TestBuildMergeThisSessionDescription_WithHook(t *testing.T) {
	got := buildMergeThisSessionDescription("just test")
	if !strings.Contains(got, "`just test`") {
		t.Errorf("description should embed the hook command:\n%s", got)
	}
	if !strings.Contains(got, "do not need to pre-flight") {
		t.Errorf("description should advise agents to skip pre-flight:\n%s", got)
	}
}

func TestBuildCheckThisSessionDescription_NoHook(t *testing.T) {
	got := buildCheckThisSessionDescription("")
	if strings.Contains(got, "configured pre-merge command is") {
		t.Errorf("description should not advertise pre-merge command when hook is unset:\n%s", got)
	}
	if !strings.Contains(got, "plain verdict lines") {
		t.Errorf("description should advertise the plain verdict-line result shape (TAP is retired for merge/check):\n%s", got)
	}
	if strings.Contains(got, "TAP") || strings.Contains(got, "test point") {
		t.Errorf("description still claims TAP output:\n%s", got)
	}
}

func TestBuildCheckThisSessionDescription_WithHook(t *testing.T) {
	got := buildCheckThisSessionDescription("just check")
	if !strings.Contains(got, "`just check`") {
		t.Errorf("description should embed the hook command:\n%s", got)
	}
}

func TestBuildNothingButTheTruthDescription_EmbedsSkillsAndRationale(t *testing.T) {
	skills := []sweatfile.PreMergeSkill{
		{Name: "eng:code-reviewer", Rationale: "Mandatory second-pass review."},
		{Name: "simplify", Rationale: "Prune premature abstractions."},
	}
	got := buildNothingButTheTruthDescription(skills)
	for _, want := range []string{
		"eng:code-reviewer",
		"Mandatory second-pass review.",
		"simplify",
		"Prune premature abstractions.",
		"nothing-but-the-truth", // not the tool name itself, but reasoning that the description must surface "merge-this-session or check-this-session"
	} {
		if want == "nothing-but-the-truth" {
			continue // not actually required in the body
		}
		if !strings.Contains(got, want) {
			t.Errorf("description missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "merge-this-session") || !strings.Contains(got, "check-this-session") {
		t.Errorf("description should mention both consuming tools:\n%s", got)
	}
}

func TestRenderValidationError_NamesMissingAndUnknown(t *testing.T) {
	required := []sweatfile.PreMergeSkill{
		{Name: "eng:code-reviewer", Rationale: "Mandatory."},
		{Name: "simplify", Rationale: "Prune."},
	}
	verr := attestation.ValidationError{
		Missing:        []string{"simplify"},
		Unrecognised:   []string{"stranger"},
		EmptyReasoning: []string{"eng:code-reviewer"},
	}
	got := renderValidationError(required, verr)
	for _, want := range []string{
		"attestation rejected",
		"missing entries for required skills: simplify",
		"unrecognised skill names: stranger",
		"empty reasoning for: eng:code-reviewer",
		"eng:code-reviewer: Mandatory.",
		"simplify: Prune.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderValidationError missing %q:\n%s", want, got)
		}
	}
}
