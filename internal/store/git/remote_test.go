package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"code.linenisgreat.com/spinclass/internal/store"
)

// remoteFix is a confinedFixture whose main checkout has a bare "origin" on disk
// (a file path, so no network and no credentials are involved) holding main.
type remoteFix struct {
	fixture
	root, remote string
}

func newRemoteFixture(t *testing.T) remoteFix {
	t.Helper()
	f := confinedFixture(t)
	root := filepath.Dir(f.repo)
	remote := filepath.Join(root, "remote.git")
	mustRun(t, root, "init", "--bare", "-b", "main", remote)
	mustRun(t, f.repo, "remote", "add", "origin", remote)
	mustRun(t, f.repo, "push", "origin", "main")
	return remoteFix{fixture: f, root: root, remote: remote}
}

func (r remoteFix) unconfined() store.Session {
	return r.session(store.ConfinementUnconfined, store.Identity{})
}

func (r remoteFix) confined() store.Session {
	return r.session(store.ConfinementConfined, store.Identity{})
}

// refOrEmpty resolves ref in dir, "" when it does not exist.
func refOrEmpty(dir, ref string) string {
	cmd := exec.Command("git", "rev-parse", "--verify", "-q", ref)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (r remoteFix) remoteRef(ref string) string { return refOrEmpty(r.remote, ref) }

// pushFromElsewhere makes a commit on top of the remote's main in a second
// clone and force-pushes it to branch, as another party would.
func (r remoteFix) pushFromElsewhere(t *testing.T, branch, message string) string {
	t.Helper()
	other := filepath.Join(r.root, "elsewhere")
	if _, err := os.Stat(other); err != nil {
		mustRun(t, r.root, "clone", r.remote, other)
	}
	mustRun(t, other, "-c", "user.name=o", "-c", "user.email=o@example.com", "commit", "--allow-empty", "-m", message)
	mustRun(t, other, "push", "--force", "origin", "HEAD:refs/heads/"+branch)
	return gitOutput(t, other, "rev-parse", "HEAD")
}

func TestFetchUpdatesRemoteTrackingRefs(t *testing.T) {
	r := newRemoteFixture(t)
	want := r.pushFromElsewhere(t, "main", "elsewhere")
	if got := refOrEmpty(r.repo, "refs/remotes/origin/main"); got == want {
		t.Fatal("the remote-tracking ref must be stale before the fetch")
	}
	for _, s := range []store.Session{r.unconfined(), r.confined()} {
		if _, err := doAs(t, s, "remote", "fetch", map[string]any{"remote": "origin"}); err != nil {
			t.Fatal(err)
		}
		if got := refOrEmpty(r.repo, "refs/remotes/origin/main"); got != want {
			t.Fatalf("origin/main = %s, want %s", got, want)
		}
		mustRun(t, r.repo, "update-ref", "-d", "refs/remotes/origin/main")
	}
}

func TestPushOwnedBranch(t *testing.T) {
	r := newRemoteFixture(t)
	if _, err := doAs(t, r.unconfined(), "remote", "push", map[string]any{"set_upstream": true}); err != nil {
		t.Fatal(err)
	}
	head := gitOutput(t, r.worktree, "rev-parse", "HEAD")
	if got := r.remoteRef("refs/heads/" + sessionBranch); got != head {
		t.Fatalf("remote %s = %s, want %s", sessionBranch, got, head)
	}
	if got := gitOutput(t, r.worktree, "config", "branch."+sessionBranch+".remote"); got != "origin" {
		t.Errorf("set_upstream: remote = %q", got)
	}

	if _, err := doAs(t, r.confined(), "remote", "push", map[string]any{"branch": sessionBranch, "remote_branch": sessionBranch + "--elsewhere"}); err != nil {
		t.Fatalf("a confined caller pushes its own refs: %v", err)
	}
	if got := r.remoteRef("refs/heads/" + sessionBranch + "--elsewhere"); got != head {
		t.Errorf("remote_branch push landed at %q, want %s", got, head)
	}
}

func TestPushLeaseForms(t *testing.T) {
	r := newRemoteFixture(t)
	s := r.unconfined()
	push := func(extra map[string]any) error {
		_, err := doAs(t, s, "remote", "push", extra)
		return err
	}
	if err := push(nil); err != nil {
		t.Fatal(err)
	}
	diverge := func() string {
		mustRun(t, r.worktree, "reset", "--hard", "HEAD~1")
		mustRun(t, r.worktree, "commit", "--allow-empty", "-m", "diverged")
		return gitOutput(t, r.worktree, "rev-parse", "HEAD")
	}

	pushed := r.remoteRef("refs/heads/" + sessionBranch)
	local := diverge()
	if err := push(nil); err == nil {
		t.Fatal("a plain push of a diverged branch must be rejected")
	}
	if err := push(map[string]any{"force_with_lease": true, "lease_ref_sha": r.remoteRef("refs/heads/main")}); err == nil {
		t.Fatal("a lease on the wrong commit must be rejected")
	}
	if got := r.remoteRef("refs/heads/" + sessionBranch); got != pushed {
		t.Fatalf("rejected pushes moved the remote to %s", got)
	}
	if err := push(map[string]any{"force_with_lease": true}); err != nil {
		t.Fatalf("the boolean lease works while a remote-tracking ref exists: %v", err)
	}
	if got := r.remoteRef("refs/heads/" + sessionBranch); got != local {
		t.Fatalf("remote = %s, want %s", got, local)
	}

	// moxy #390: with no remote-tracking ref the boolean form has nothing to
	// compare against; the explicit form names what the remote must be at, and
	// needs no tracking ref.
	pushed = r.remoteRef("refs/heads/" + sessionBranch)
	local = diverge()
	mustRun(t, r.worktree, "update-ref", "-d", "refs/remotes/origin/"+sessionBranch)
	moved := r.pushFromElsewhere(t, sessionBranch, "moved under us")
	if err := push(map[string]any{"force_with_lease": true, "lease_ref_sha": pushed}); err == nil {
		t.Fatal("an explicit lease on a commit the remote has moved off must be rejected")
	}
	if got := r.remoteRef("refs/heads/" + sessionBranch); got != moved {
		t.Fatalf("the rejected push moved the remote to %s", got)
	}
	if err := push(map[string]any{"force_with_lease": true, "lease_ref_sha": moved}); err != nil {
		t.Fatalf("the explicit lease form: %v", err)
	}
	if got := r.remoteRef("refs/heads/" + sessionBranch); got != local {
		t.Fatalf("remote = %s, want %s", got, local)
	}
}

func TestPushArgvCarriesTheLeaseAsGiven(t *testing.T) {
	r := newRemoteFixture(t)
	for _, tc := range []struct {
		name    string
		args    args
		want    []string
		wantErr string
	}{
		{"explicit", args{ForceWithLease: true, LeaseRefSHA: "abc1234"}, []string{"push", "--force-with-lease=refs/heads/" + sessionBranch + ":abc1234", "origin", "refs/heads/" + sessionBranch + ":refs/heads/" + sessionBranch}, ""},
		{"boolean", args{ForceWithLease: true}, []string{"push", "--force-with-lease", "origin", "refs/heads/" + sessionBranch + ":refs/heads/" + sessionBranch}, ""},
		{"explicit on the destination name", args{ForceWithLease: true, LeaseRefSHA: "abc1234", RemoteBranch: "refs/heads/x"}, []string{"push", "--force-with-lease=refs/heads/x:abc1234", "origin", "refs/heads/" + sessionBranch + ":refs/heads/x"}, ""},
		{"sha without a lease", args{LeaseRefSHA: "abc1234"}, nil, "needs force_with_lease"},
		{"includes without a lease", args{ForceIfIncludes: true}, nil, "needs force_with_lease"},
		{"not a hash", args{ForceWithLease: true, LeaseRefSHA: "main"}, nil, "not a commit hash"},
		{"force and lease", args{Force: true, ForceWithLease: true}, nil, "mutually exclusive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildPushArgv(r.worktree, tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil || strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("got %q (%v), want %q", got, err, tc.want)
			}
		})
	}
}

func TestPushDeleteRemovesTheRemoteBranch(t *testing.T) {
	r := newRemoteFixture(t)
	topic := sessionBranch + "--topic"
	if _, err := doAs(t, r.unconfined(), "remote", "push", map[string]any{"branch": topic}); err != nil {
		t.Fatal(err)
	}
	if r.remoteRef("refs/heads/"+topic) == "" {
		t.Fatal("the topic branch must have been pushed")
	}
	_, err := doAs(t, r.confined(), "remote", "push", map[string]any{"branch": topic, "delete": true})
	if !isOwnershipRefusal(err) || r.remoteRef("refs/heads/"+topic) == "" {
		t.Fatalf("a confined caller may not delete a remote ref, err = %v", err)
	}
	if _, err := doAs(t, r.unconfined(), "remote", "push", map[string]any{"branch": topic, "delete": true}); err != nil {
		t.Fatal(err)
	}
	if got := r.remoteRef("refs/heads/" + topic); got != "" {
		t.Fatalf("the remote branch must be gone, still at %s", got)
	}
}

func TestPushOfTheDefaultBranchIsRefusedForConfinedAndUnresolvedCallers(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool string
		sub  string
		args map[string]any
	}{
		{"branch main", "remote", "push", map[string]any{"branch": "main"}},
		{"branch main with force", "remote", "push", map[string]any{"branch": "main", "force": true}},
		{"owned branch onto main", "remote", "push", map[string]any{"remote_branch": "main"}},
		{"owned branch onto refs/heads/master", "remote", "push", map[string]any{"remote_branch": "refs/heads/master"}},
		{"delete main", "remote", "push", map[string]any{"branch": "main", "delete": true}},
		{"push-stack through main", "remote", "push-stack", map[string]any{"branches": []string{sessionBranch, "main"}}},
	} {
		for _, confinement := range []store.Confinement{store.ConfinementConfined, store.ConfinementUnresolved} {
			t.Run(tc.name+"/"+confinementNames[confinement], func(t *testing.T) {
				r := newRemoteFixture(t)
				before := r.remoteRef("refs/heads/main")
				_, err := doAs(t, r.session(confinement, store.Identity{}), tc.tool, tc.sub, tc.args)
				if err == nil || !strings.Contains(err.Error(), "default branch") {
					t.Fatalf("want a default-branch refusal, got %v", err)
				}
				if got := r.remoteRef("refs/heads/main"); got != before {
					t.Errorf("remote main moved from %s to %s", before, got)
				}
			})
		}
	}
}

func TestPushStackNeverTouchesTheDefaultBranchForAnyCaller(t *testing.T) {
	r := newRemoteFixture(t)
	before := r.remoteRef("refs/heads/main")
	_, err := doAs(t, r.unconfined(), "remote", "push-stack", map[string]any{"branches": []string{"main"}})
	if err == nil || !strings.Contains(err.Error(), "default branch") {
		t.Fatalf("want a default-branch refusal, got %v", err)
	}
	if r.remoteRef("refs/heads/main") != before {
		t.Error("remote main moved")
	}
}

func TestPushStack(t *testing.T) {
	r := newRemoteFixture(t)
	base := sessionBranch + "--base"
	mustRun(t, r.worktree, "branch", base, "HEAD~1")
	stack := map[string]any{"branches": []string{base, sessionBranch}}

	out, err := doAs(t, r.unconfined(), "remote", "push-stack", stack)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"phase":"push","results":[{"branch":"` + base + `","status":"ok"`; !strings.HasPrefix(out, want) {
		t.Fatalf("report = %s", out)
	}
	for _, branch := range []string{base, sessionBranch} {
		if got, want := r.remoteRef("refs/heads/"+branch), refOrEmpty(r.worktree, branch); got != want {
			t.Errorf("remote %s = %s, want %s", branch, got, want)
		}
	}

	// Another party moves the base; the stale lease fails the dry run, so the
	// stack's top, which has a new local commit, must not be pushed either.
	r.pushFromElsewhere(t, base, "moved under us")
	mustRun(t, r.worktree, "commit", "--allow-empty", "-m", "new top")
	topBefore := r.remoteRef("refs/heads/" + sessionBranch)
	_, err = doAs(t, r.unconfined(), "remote", "push-stack", stack)
	if err == nil || !strings.HasPrefix(err.Error(), `{"phase":"preflight"`) || !strings.Contains(err.Error(), `"status":"failed"`) {
		t.Fatalf("want a failed preflight report, got %v", err)
	}
	if got := r.remoteRef("refs/heads/" + sessionBranch); got != topBefore {
		t.Errorf("a failed preflight pushed the top branch: %s", got)
	}
}

func TestPullMergesTheRemoteBranch(t *testing.T) {
	for _, confinement := range []store.Confinement{store.ConfinementUnconfined, store.ConfinementConfined} {
		t.Run(confinementNames[confinement], func(t *testing.T) {
			r := newRemoteFixture(t)
			upstream := r.pushFromElsewhere(t, "main", "upstream work")
			if _, err := doAs(t, r.session(confinement, store.Identity{}), "remote", "pull", map[string]any{"remote": "origin", "branch": "main"}); err != nil {
				t.Fatal(err)
			}
			if parents := strings.Fields(gitOutput(t, r.worktree, "rev-list", "--parents", "-n1", "HEAD")); len(parents) != 3 {
				t.Fatalf("pull must make a merge commit, got %v", parents)
			}
			if base := gitOutput(t, r.worktree, "merge-base", upstream, "HEAD"); base != upstream {
				t.Error("the pulled commit must be an ancestor of HEAD")
			}
		})
	}
}

// No git run by these tests may leave a detached process behind: one that
// outlives its command keeps writing into the fixture's TempDir after the test
// returns, and its RemoveAll then fails with "directory not empty" (#421). Auto
// maintenance is the one git detaches by default, after the commits, fetches
// and merges a pull makes. git's trace2 events record the detach from the
// process that does it, before the command that spawned it returns, so the
// trace shows it without racing the background process.
func TestFixtureGitLeavesNoDetachedMaintenance(t *testing.T) {
	r := newRemoteFixture(t)
	r.pushFromElsewhere(t, "main", "upstream work")
	trace := filepath.Join(t.TempDir(), "trace2.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	if _, err := doAs(t, r.confined(), "remote", "pull", map[string]any{"remote": "origin", "branch": "main"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatalf("git wrote no trace2 events: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		detachedChild := strings.Contains(line, `"event":"child_start"`) && strings.Contains(line, `"maintenance"`) && strings.Contains(line, `"--detach"`)
		if detachedChild || strings.Contains(line, `"label":"detach"`) {
			t.Errorf("git detached background maintenance: %s", line)
		}
	}
}

func TestRemoteVerbsAcceptOnlyConfiguredRemoteNames(t *testing.T) {
	r := newRemoteFixture(t)
	for _, tc := range []struct{ tool, sub string }{
		{"remote", "fetch"}, {"remote", "pull"}, {"remote", "push"}, {"remote", "push-stack"}, {"tag", "push"},
	} {
		t.Run(tc.tool+" "+tc.sub, func(t *testing.T) {
			args := map[string]any{"remote": r.remote, "name": sessionBranch + "--t", "branches": []string{sessionBranch}}
			_, err := doAs(t, r.unconfined(), tc.tool, tc.sub, args)
			if err == nil || !strings.Contains(err.Error(), "not a configured remote") {
				t.Fatalf("want a configured-remote refusal, got %v", err)
			}
			if got := New().Classify(r.own(), op(t, tc.tool, tc.sub, args)); got != store.Ask {
				t.Errorf("a URL remote must ask, got %v", got)
			}
		})
	}
}

type recordedRun struct {
	dir  string
	env  []string
	argv []string
}

func stubRemoteRunner(t *testing.T) *[]recordedRun {
	t.Helper()
	var mu sync.Mutex
	var runs []recordedRun
	original := remoteRunner
	t.Cleanup(func() { remoteRunner = original })
	remoteRunner = func(_ context.Context, dir string, env []string, argv ...string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		runs = append(runs, recordedRun{dir, env, argv})
		return "", nil
	}
	return &runs
}

func TestRemoteVerbsRunInTheSessionWorktreeThroughTheRemoteRunner(t *testing.T) {
	r := newRemoteFixture(t)
	runs := stubRemoteRunner(t)
	s := r.unconfined()
	for _, tc := range []struct {
		tool, sub string
		args      map[string]any
		wantRuns  int
	}{
		{"remote", "fetch", nil, 1},
		{"remote", "pull", map[string]any{"remote": "origin"}, 1},
		{"remote", "push", nil, 1},
		{"remote", "push-stack", map[string]any{"branches": []string{sessionBranch}}, 2},
		{"tag", "push", map[string]any{"name": sessionBranch + "--t"}, 1},
		{"clone", "", map[string]any{"source": r.remote, "destination": "c"}, 1},
	} {
		*runs = nil
		if _, err := doAs(t, s, tc.tool, tc.sub, tc.args); err != nil {
			t.Fatalf("%s %s: %v", tc.tool, tc.sub, err)
		}
		if len(*runs) != tc.wantRuns {
			t.Fatalf("%s %s: %d runner calls, want %d", tc.tool, tc.sub, len(*runs), tc.wantRuns)
		}
		for _, run := range *runs {
			if run.dir != r.worktree {
				t.Errorf("%s %s ran in %s, want the session worktree %s", tc.tool, tc.sub, run.dir, r.worktree)
			}
		}
	}

	*runs = nil
	for _, tc := range []struct {
		tool, sub string
		args      map[string]any
	}{
		{"tag", "list", nil},
		{"branch", "log", nil},
		{"commit", "", map[string]any{"message": "m", "allow_empty": true}},
		{"tag", "create", map[string]any{"name": sessionBranch + "--l", "lightweight": true}},
	} {
		if _, err := doAs(t, s, tc.tool, tc.sub, tc.args); err != nil {
			t.Fatalf("%s %s: %v", tc.tool, tc.sub, err)
		}
	}
	if len(*runs) != 0 {
		t.Errorf("local verbs must not use the remote runner, got %v", *runs)
	}
}

func TestRemoteEnvDropsTheAmbientAgent(t *testing.T) {
	got := remoteEnv(
		[]string{
			"A=1", "SSH_AUTH_SOCK=/run/agent", "SSH_AGENT_PID=7", "SSH_AUTH_SOCK_LIKE=keep", "GIT_TERMINAL_PROMPT=1",
			"GIT_SSH_COMMAND=ssh -o IdentityAgent=/run/agent", "GIT_SSH=/bin/ssh",
		},
		[]string{"GIT_EDITOR=true"},
	)
	joined := strings.Join(got, "\n")
	if !strings.Contains("\n"+joined+"\n", "\nGIT_SSH_COMMAND="+agentlessSSHCommand+"\n") || strings.Count(joined, "GIT_SSH_COMMAND=") != 1 {
		t.Errorf("ssh must be pinned to the agentless command exactly once, got %q", got)
	}
	for _, banned := range []string{"SSH_AUTH_SOCK=", "SSH_AGENT_PID=", "GIT_SSH=", "GIT_SSH_COMMAND=ssh -o IdentityAgent=/run"} {
		if strings.Contains("\n"+joined, "\n"+banned) {
			t.Errorf("%s survived in %q", banned, got)
		}
	}
	for _, want := range []string{"A=1", "SSH_AUTH_SOCK_LIKE=keep", "GIT_EDITOR=true"} {
		if !strings.Contains("\n"+joined+"\n", "\n"+want+"\n") {
			t.Errorf("%s missing from %q", want, got)
		}
	}
	if last := got[len(got)-1]; last != "GIT_TERMINAL_PROMPT=0" {
		t.Errorf("prompting must be switched off last so it wins, got %q", last)
	}
}

// The real environment git is handed, observed from a pre-push hook.
func TestPushRunsWithoutTheAgentAndInTheWorktree(t *testing.T) {
	r := newRemoteFixture(t)
	t.Setenv("SSH_AUTH_SOCK", "/nonexistent/agent.sock")
	t.Setenv("SSH_AGENT_PID", "4242")
	record := filepath.Join(r.root, "pre-push-record")
	hooksDir := filepath.Join(r.root, "push-hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n{ echo \"cwd=$(pwd -P)\"; env; } > \"" + record + "\"\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	mustRun(t, r.repo, "config", "core.hooksPath", hooksDir)

	if _, err := doAs(t, r.unconfined(), "remote", "push", nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the pre-push hook did not run: %v", err)
	}
	lines := "\n" + string(raw)
	for _, banned := range []string{"\nSSH_AUTH_SOCK=", "\nSSH_AGENT_PID="} {
		if strings.Contains(lines, banned) {
			t.Errorf("git saw %s", strings.TrimSpace(banned))
		}
	}
	for _, want := range []string{"\nGIT_TERMINAL_PROMPT=0\n", "\ncwd=" + r.worktree + "\n"} {
		if !strings.Contains(lines, want) {
			t.Errorf("hook record lacks %q:\n%s", strings.TrimSpace(want), raw)
		}
	}
}

func TestClassifyRemoteVerdicts(t *testing.T) {
	r := newRemoteFixture(t)
	topicWorktree := filepath.Join(r.repo, ".worktrees", "topic")
	mustRun(t, r.repo, "worktree", "add", topicWorktree, sessionBranch+"--topic")
	owned := sessionBranch + "--v1"
	runCases(t, r.fixture, []classifyCase{
		{name: "fetch", tool: "remote", sub: "fetch", args: map[string]any{}, want: store.Allow},
		{name: "fetch a configured remote", tool: "remote", sub: "fetch", args: map[string]any{"remote": "origin", "all": false}, want: store.Allow},
		{name: "fetch a URL", tool: "remote", sub: "fetch", args: map[string]any{"remote": r.remote}, want: store.Ask},
		{name: "pull", tool: "remote", sub: "pull", args: map[string]any{"remote": "origin", "branch": "main"}, want: store.Allow},
		{name: "pull in a foreign worktree", tool: "remote", sub: "pull", args: map[string]any{}, want: store.Ask, foreignDir: true},
		{name: "push", tool: "remote", sub: "push", args: map[string]any{"remote": "origin"}, want: store.Allow},
		{name: "push to a URL", tool: "remote", sub: "push", args: map[string]any{"remote": r.remote}, want: store.Ask},
		{name: "push with an explicit lease", tool: "remote", sub: "push", args: map[string]any{"force_with_lease": true, "lease_ref_sha": "abcd1234"}, want: store.Allow},
		{name: "push --force", tool: "remote", sub: "push", args: map[string]any{"force": true}, want: store.Ask},
		{name: "push the default branch", tool: "remote", sub: "push", args: map[string]any{"branch": "main"}, want: store.Ask},
		{name: "push a foreign branch", tool: "remote", sub: "push", args: map[string]any{"branch": "B2"}, want: store.Ask},
		{name: "push --delete of an owned ref", tool: "remote", sub: "push", args: map[string]any{"branch": sessionBranch + "--topic", "delete": true}, want: store.Ask},
		{name: "push-stack", tool: "remote", sub: "push-stack", args: map[string]any{"branches": []string{sessionBranch, sessionBranch + "--topic"}}, want: store.Allow},
		{name: "push-stack through the default branch", tool: "remote", sub: "push-stack", args: map[string]any{"branches": []string{sessionBranch, "main"}}, want: store.Ask},
		{name: "tag push owned", tool: "tag", sub: "push", args: map[string]any{"name": owned, "remote": "origin"}, want: store.Allow},
		{name: "tag push foreign", tool: "tag", sub: "push", args: map[string]any{"name": "foo"}, want: store.Ask},
		{name: "tag push --delete", tool: "tag", sub: "push", args: map[string]any{"name": owned, "delete": true}, want: store.Ask},
		{name: "merge", tool: "merge", args: map[string]any{"branch": "B2"}, want: store.Allow},
		{name: "merge --squash", tool: "merge", args: map[string]any{"branch": "B2", "squash": true}, want: store.Allow},
		{name: "merge in a foreign worktree", tool: "merge", args: map[string]any{"branch": "B2"}, want: store.Ask, foreignDir: true},
		{name: "merge with an option-like branch", tool: "merge", args: map[string]any{"branch": "--abort"}, want: store.Ask},
		{name: "clone into the worktree", tool: "clone", args: map[string]any{"source": r.remote, "destination": "c"}, want: store.Allow},
		{name: "clone into .tmp", tool: "clone", args: map[string]any{"source": r.remote, "destination": ".tmp/c"}, want: store.Allow},
		{name: "clone leaving the worktree", tool: "clone", args: map[string]any{"source": r.remote, "destination": "../c"}, want: store.Ask},
		{name: "clone to an absolute path elsewhere", tool: "clone", args: map[string]any{"source": r.remote, "destination": filepath.Join(r.root, "c")}, want: store.Ask},
		{name: "clone with no destination", tool: "clone", args: map[string]any{"source": r.remote}, want: store.Ask},
		{name: "worktree remove of an owned-branch worktree", tool: "worktree", sub: "remove", args: map[string]any{"path": topicWorktree}, want: store.Allow},
		{name: "worktree remove, forced", tool: "worktree", sub: "remove", args: map[string]any{"path": topicWorktree, "force": true}, want: store.Ask},
		{name: "worktree remove of a foreign branch's worktree", tool: "worktree", sub: "remove", args: map[string]any{"path": r.foreignWorktree}, want: store.Ask},
		{name: "worktree remove of the own worktree", tool: "worktree", sub: "remove", args: map[string]any{"path": r.worktree}, want: store.Ask},
		{name: "worktree remove of the main checkout", tool: "worktree", sub: "remove", args: map[string]any{"path": r.repo}, want: store.Ask},
		{name: "tag create owned", tool: "tag", sub: "create", args: map[string]any{"name": owned, "message": "m"}, want: store.Allow},
	})
}

// A repo_path naming some other checkout whose current branch would be owned
// must not let a write verdict reach Allow: ownership is judged against the
// session worktree, not the checkout the call is aimed at.
func TestClassifyRepoPathAtAnotherCheckoutNeverAllowsWrites(t *testing.T) {
	r := newRemoteFixture(t)
	topicWorktree := filepath.Join(r.repo, ".worktrees", "topic")
	mustRun(t, r.repo, "worktree", "add", topicWorktree, sessionBranch+"--topic")

	// A second worktree of the same repo, on an owned-looking branch.
	peerWorktree := filepath.Join(r.repo, ".worktrees", "peer")
	mustRun(t, r.repo, "worktree", "add", "-b", sessionBranch+"--peer", peerWorktree)

	// A separate repository whose current branch is the session branch itself.
	otherRepo := filepath.Join(r.root, "other-repo")
	if err := os.MkdirAll(otherRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	mustRun(t, otherRepo, "init", "-b", sessionBranch)
	mustRun(t, otherRepo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-m", "c0")

	ownedTag := sessionBranch + "--v1"
	cases := []struct {
		name      string
		tool, sub string
		args      map[string]any
	}{
		{"remote push", "remote", "push", map[string]any{"remote": "origin"}},
		{"remote pull", "remote", "pull", map[string]any{"remote": "origin", "branch": "main"}},
		{"tag push", "tag", "push", map[string]any{"name": ownedTag, "remote": "origin"}},
		{"merge", "merge", "", map[string]any{"branch": "B2"}},
		{"stage add", "stage", "add", map[string]any{"paths": []string{"."}}},
		{"commit", "commit", "", map[string]any{"message": "m", "allow_empty": true}},
		{"worktree remove", "worktree", "remove", map[string]any{"path": topicWorktree}},
		{"clone", "clone", "", map[string]any{"source": r.remote, "destination": "c"}},
	}
	withRepoPath := func(args map[string]any, dir string) map[string]any {
		out := map[string]any{"repo_path": dir}
		for k, v := range args {
			out[k] = v
		}
		return out
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := r.own()
			if got := New().Classify(s, op(t, tc.tool, tc.sub, tc.args)); got != store.Allow {
				t.Fatalf("baseline without repo_path = %v, want allow (test would be vacuous)", got)
			}
			if got := New().Classify(s, op(t, tc.tool, tc.sub, withRepoPath(tc.args, r.worktree))); got != store.Allow {
				t.Errorf("repo_path = the session worktree = %v, want allow", got)
			}
			for dirName, dir := range map[string]string{
				"another worktree of the repo":          peerWorktree,
				"a worktree on an owned branch":         topicWorktree,
				"a separate repo on the session branch": otherRepo,
			} {
				if tc.tool == "clone" {
					// clone ignores repo_path entirely: its destination is judged
					// against the session worktree, so repo_path cannot widen it.
					if got := New().Classify(s, op(t, tc.tool, tc.sub, withRepoPath(tc.args, dir))); got != store.Allow {
						t.Errorf("clone with repo_path at %s = %v, want allow (repo_path ignored)", dirName, got)
					}
					escaping := withRepoPath(map[string]any{"source": r.remote, "destination": "../c"}, dir)
					if got := New().Classify(s, op(t, tc.tool, tc.sub, escaping)); got != store.Ask {
						t.Errorf("clone leaving the worktree with repo_path at %s = %v, want ask", dirName, got)
					}
					continue
				}
				if got := New().Classify(s, op(t, tc.tool, tc.sub, withRepoPath(tc.args, dir))); got != store.Ask {
					t.Errorf("repo_path at %s (%s) = %v, want ask", dirName, dir, got)
				}
			}
		})
	}
}

func TestCloneDestinationMustResolveInsideTheWorktree(t *testing.T) {
	r := newRemoteFixture(t)
	outside := filepath.Join(r.root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(r.worktree, "escape")); err != nil {
		t.Fatal(err)
	}
	runCases(t, r.fixture, []classifyCase{
		{name: "through a symlink out of the worktree", tool: "clone", args: map[string]any{"source": r.remote, "destination": "escape/c"}, want: store.Ask},
		{name: "dot-dot that comes back in", tool: "clone", args: map[string]any{"source": r.remote, "destination": "a/../c"}, want: store.Allow},
	})
}

func TestCloneInsideTheWorktree(t *testing.T) {
	r := newRemoteFixture(t)
	if _, err := doAs(t, r.unconfined(), "clone", "", map[string]any{"source": r.remote, "destination": ".tmp/cloned"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.worktree, ".tmp", "cloned", ".git")); err != nil {
		t.Fatalf("the clone must land in the worktree: %v", err)
	}
	if _, err := doAs(t, r.unconfined(), "clone", "", map[string]any{"source": "--upload-pack=x", "destination": "d"}); err == nil {
		t.Error("an option-like source must be refused")
	}
}

func TestConfinedCallersMayNotClone(t *testing.T) {
	for _, confinement := range []store.Confinement{store.ConfinementConfined, store.ConfinementUnresolved} {
		r := newRemoteFixture(t)
		_, err := doAs(t, r.session(confinement, store.Identity{}), "clone", "", map[string]any{"source": r.remote, "destination": "c"})
		if err == nil || !strings.Contains(err.Error(), "may not clone") {
			t.Fatalf("%s: want a clone refusal, got %v", confinementNames[confinement], err)
		}
		if _, statErr := os.Stat(filepath.Join(r.worktree, "c")); statErr == nil {
			t.Errorf("%s: the refused clone created a directory", confinementNames[confinement])
		}
	}
}

func TestTagCreateDeleteAndPush(t *testing.T) {
	r := newRemoteFixture(t)
	s := r.unconfined()
	name := func(suffix string) string { return sessionBranch + "--" + suffix }

	if _, err := doAs(t, s, "tag", "create", map[string]any{"name": name("light"), "lightweight": true}); err != nil {
		t.Fatal(err)
	}
	if got, want := refOrEmpty(r.worktree, "refs/tags/"+name("light")), gitOutput(t, r.worktree, "rev-parse", "HEAD"); got != want {
		t.Errorf("lightweight tag at %s, want HEAD %s", got, want)
	}
	if _, err := doAs(t, s, "tag", "create", map[string]any{"name": name("ann"), "message": "m", "sign": false}); err != nil {
		t.Fatal(err)
	}
	if got := gitOutput(t, r.worktree, "cat-file", "-t", "refs/tags/"+name("ann")); got != "tag" {
		t.Errorf("annotated tag object type = %s", got)
	}

	// Signing is the default and the session may have no key: git's own error
	// comes back and nothing is created.
	if _, err := doAs(t, s, "tag", "create", map[string]any{"name": name("signed"), "message": "m"}); err == nil {
		t.Error("a signed tag with no signing key must fail")
	}
	if refOrEmpty(r.worktree, "refs/tags/"+name("signed")) != "" {
		t.Error("a failed signed tag must not exist")
	}
	for _, args := range []map[string]any{
		{"name": name("x"), "lightweight": true, "message": "m"},
		{"name": name("x"), "lightweight": true, "sign": true},
		{"name": name("x")},
		{"message": "m"},
	} {
		if _, err := doAs(t, s, "tag", "create", args); err == nil {
			t.Errorf("tag create %v must be refused", args)
		}
	}

	if _, err := doAs(t, s, "tag", "push", map[string]any{"name": name("light")}); err != nil {
		t.Fatal(err)
	}
	if r.remoteRef("refs/tags/"+name("light")) == "" {
		t.Fatal("the tag must be on the remote")
	}
	if _, err := doAs(t, s, "tag", "push", map[string]any{"name": name("light"), "delete": true}); err != nil {
		t.Fatal(err)
	}
	if r.remoteRef("refs/tags/"+name("light")) != "" {
		t.Error("the remote tag must be deleted")
	}
	if _, err := doAs(t, s, "tag", "delete", map[string]any{"name": name("light")}); err != nil {
		t.Fatal(err)
	}
	if refOrEmpty(r.worktree, "refs/tags/"+name("light")) != "" {
		t.Error("the local tag must be deleted")
	}
}

func TestConfinedTagVerbsStayInTheOwnedNamespace(t *testing.T) {
	r := newRemoteFixture(t)
	s := r.confined()
	if _, err := doAs(t, s, "tag", "create", map[string]any{"name": sessionBranch + "--ok", "lightweight": true}); err != nil {
		t.Fatalf("an owned tag: %v", err)
	}
	for _, tc := range []struct {
		sub  string
		args map[string]any
	}{
		{"create", map[string]any{"name": "v9", "lightweight": true}},
		{"delete", map[string]any{"name": "foo"}},
		{"push", map[string]any{"name": "foo"}},
		{"push", map[string]any{"name": sessionBranch + "--t", "delete": true}},
	} {
		before := refState(t, r.fixture)
		if _, err := doAs(t, s, "tag", tc.sub, tc.args); !isOwnershipRefusal(err) {
			t.Errorf("tag %s %v: want an ownership refusal, got %v", tc.sub, tc.args, err)
		}
		if refState(t, r.fixture) != before {
			t.Errorf("tag %s %v changed refs", tc.sub, tc.args)
		}
	}
}

func TestMerge(t *testing.T) {
	f := confinedFixture(t)
	topic := sessionBranch + "--m"
	mustRun(t, f.worktree, "checkout", "-b", topic, "main")
	commitFile(t, f.worktree, "m.txt", "m\n", "topic work")
	mustRun(t, f.worktree, "checkout", sessionBranch)

	t.Run("no-ff makes a merge commit", func(t *testing.T) {
		mustRun(t, f.worktree, "branch", "-f", sessionBranch+"--keep", "HEAD")
		if _, err := doAs(t, f.session(store.ConfinementConfined, store.Identity{}), "merge", "", map[string]any{"branch": topic, "no_ff": true}); err != nil {
			t.Fatal(err)
		}
		if parents := strings.Fields(gitOutput(t, f.worktree, "rev-list", "--parents", "-n1", "HEAD")); len(parents) != 3 {
			t.Fatalf("want a merge commit, got %v", parents)
		}
		mustRun(t, f.worktree, "reset", "--hard", sessionBranch+"--keep")
	})
	t.Run("squash stages without committing", func(t *testing.T) {
		head := gitOutput(t, f.worktree, "rev-parse", "HEAD")
		if _, err := doAs(t, f.session(store.ConfinementUnconfined, store.Identity{}), "merge", "", map[string]any{"branch": topic, "squash": true}); err != nil {
			t.Fatal(err)
		}
		if gitOutput(t, f.worktree, "rev-parse", "HEAD") != head {
			t.Error("squash must not commit")
		}
		if got := gitOutput(t, f.worktree, "diff", "--cached", "--name-only"); got != "m.txt" {
			t.Errorf("staged = %q", got)
		}
		mustRun(t, f.worktree, "reset", "--hard", head)
	})
	t.Run("a conflict is reported and can be aborted", func(t *testing.T) {
		commitFile(t, f.worktree, "c.txt", "mine\n", "mine")
		mustRun(t, f.worktree, "checkout", "-b", sessionBranch+"--c", "main")
		commitFile(t, f.worktree, "c.txt", "theirs\n", "theirs")
		mustRun(t, f.worktree, "checkout", sessionBranch)
		_, err := doAs(t, f.session(store.ConfinementUnconfined, store.Identity{}), "merge", "", map[string]any{"branch": sessionBranch + "--c"})
		var conflict *ConflictError
		if !errors.As(err, &conflict) || len(conflict.Paths) != 1 || conflict.Paths[0] != "c.txt" {
			t.Fatalf("want a ConflictError naming c.txt, got %v", err)
		}
		if _, err := doAs(t, f.session(store.ConfinementUnconfined, store.Identity{}), "merge", "", map[string]any{"abort": true}); err != nil {
			t.Fatal(err)
		}
		if refOrEmpty(f.worktree, "MERGE_HEAD") != "" {
			t.Error("the merge must be aborted")
		}
	})
	t.Run("missing branch and conflicting controls are errors", func(t *testing.T) {
		for _, args := range []map[string]any{{}, {"abort": true, "continue": true}} {
			if _, err := doAs(t, f.session(store.ConfinementUnconfined, store.Identity{}), "merge", "", args); err == nil {
				t.Errorf("merge %v must fail", args)
			}
		}
	})
}

func TestMergeIsRefusedOnTheDefaultBranch(t *testing.T) {
	f := confinedFixture(t)
	for _, confinement := range []store.Confinement{store.ConfinementUnconfined, store.ConfinementConfined, store.ConfinementUnresolved} {
		before := gitOutput(t, f.repo, "rev-parse", "HEAD")
		s := store.Session{Dir: f.repo, Branch: sessionBranch, Confinement: confinement}
		_, err := doAs(t, s, "merge", "", map[string]any{"branch": sessionBranch})
		if err == nil || !strings.Contains(err.Error(), "default branch") && !isOwnershipRefusal(err) {
			t.Fatalf("%s: want a refusal, got %v", confinementNames[confinement], err)
		}
		if gitOutput(t, f.repo, "rev-parse", "HEAD") != before {
			t.Errorf("%s: main moved", confinementNames[confinement])
		}
	}
}

func TestWorktreeRemove(t *testing.T) {
	r := newRemoteFixture(t)
	topicWorktree := filepath.Join(r.repo, ".worktrees", "topic")
	mustRun(t, r.repo, "worktree", "add", topicWorktree, sessionBranch+"--topic")

	_, err := doAs(t, r.unconfined(), "worktree", "remove", map[string]any{"path": r.worktree})
	if err == nil || !strings.Contains(err.Error(), "runs in") {
		t.Fatalf("the worktree the call runs in is never removable, got %v", err)
	}
	if _, statErr := os.Stat(r.worktree); statErr != nil {
		t.Fatal("the own worktree must still exist")
	}
	_, err = doAs(t, r.session(store.ConfinementUnconfined, store.Identity{}), "worktree", "remove", map[string]any{"path": r.worktree + "/."})
	if err == nil {
		t.Error("the own worktree through a dot path must be refused too")
	}

	before := refState(t, r.fixture)
	if _, err := doAs(t, r.confined(), "worktree", "remove", map[string]any{"path": r.foreignWorktree}); !isOwnershipRefusal(err) {
		t.Fatalf("a confined caller may not remove a foreign worktree, got %v", err)
	}
	if refState(t, r.fixture) != before {
		t.Error("a refused removal changed refs")
	}
	if _, statErr := os.Stat(r.foreignWorktree); statErr != nil {
		t.Error("the foreign worktree must still exist")
	}

	if _, err := doAs(t, r.confined(), "worktree", "remove", map[string]any{"path": topicWorktree}); err != nil {
		t.Fatalf("an owned-branch worktree is removable: %v", err)
	}
	if _, statErr := os.Stat(topicWorktree); statErr == nil {
		t.Error("the removed worktree must be gone")
	}
}

func TestACallerWithoutASessionWorktreeOwnsNothing(t *testing.T) {
	r := newRemoteFixture(t)
	s := r.unconfined()
	s.NoSession = true
	for _, tc := range []struct {
		tool, sub string
		args      map[string]any
	}{
		{"commit", "", map[string]any{"message": "m", "allow_empty": true}},
		{"branch", "create", map[string]any{"name": sessionBranch + "--x"}},
		{"stage", "add", map[string]any{"paths": []string{"."}}},
		{"tag", "create", map[string]any{"name": sessionBranch + "--x", "lightweight": true}},
		{"remote", "push", nil},
		{"remote", "pull", nil},
		{"merge", "", map[string]any{"branch": "main"}},
		{"clone", "", map[string]any{"source": r.remote, "destination": "c"}},
		{"worktree", "remove", map[string]any{"path": r.foreignWorktree}},
		{"stash", "save", nil},
	} {
		t.Run(tc.tool+" "+tc.sub, func(t *testing.T) {
			before := refState(t, r.fixture)
			_, err := doAs(t, s, tc.tool, tc.sub, tc.args)
			if err == nil || !strings.Contains(err.Error(), "sc start") {
				t.Fatalf("want a refusal pointing at sc start, got %v", err)
			}
			if refState(t, r.fixture) != before {
				t.Error("a refused write changed refs")
			}
			if got := New().Classify(s, op(t, tc.tool, tc.sub, tc.args)); got != store.Ask {
				t.Errorf("Classify = %v, want ask", got)
			}
		})
	}
	for _, confinement := range []store.Confinement{store.ConfinementConfined, store.ConfinementUnresolved} {
		s.Confinement = confinement
		if _, err := doAs(t, s, "commit", "", map[string]any{"message": "m", "allow_empty": true}); err == nil || !strings.Contains(err.Error(), "sc start") {
			t.Errorf("%s: want a refusal pointing at sc start, got %v", confinementNames[confinement], err)
		}
	}
	if _, err := doAs(t, s, "branch", "log", map[string]any{"oneline": true}); err != nil {
		t.Errorf("reads are not refused: %v", err)
	}
	if _, err := doAs(t, s, "remote", "fetch", nil); err != nil {
		t.Errorf("fetch is a read: %v", err)
	}
}
