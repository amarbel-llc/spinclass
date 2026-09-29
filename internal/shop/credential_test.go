package shop

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/worktree"
)

const authTestOrigin = "ssh://git@127.0.0.1:1/owner/repo.git"

// authRepo is the fixture for the [auth] lane through the creation funnel:
// a repo with an unreachable ssh origin (so the base-branch fetch fails fast
// and every Create passes AllowStaleBase) under a parent directory that can
// carry the trusted layer.
type authRepo struct {
	root string
	repo string
	rp   worktree.ResolvedPath
}

func setupAuthRepo(t *testing.T) authRepo {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

	repo := filepath.Join(root, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	git("config", "user.email", "test@test.com")
	git("config", "user.name", "Test")
	git("commit", "--allow-empty", "-m", "initial")
	// The origin is ssh-shaped so the rewrite assertions are meaningful. Port 1
	// refuses immediately, and the base-branch fetch runs with BatchMode and
	// GIT_TERMINAL_PROMPT=0, so it cannot hang; AllowStaleBase turns the
	// failed fetch into a SKIP.
	git("remote", "add", "origin", authTestOrigin)

	return authRepo{
		root: root,
		repo: repo,
		rp: worktree.ResolvedPath{
			AbsPath:    filepath.Join(repo, ".worktrees", "feature-x"),
			RepoPath:   repo,
			Branch:     "feature-x",
			SessionKey: "repo/feature-x",
		},
	}
}

func (a authRepo) parentLayer() string { return filepath.Join(a.root, "work", "sweatfile") }
func (a authRepo) repoLayer() string   { return filepath.Join(a.repo, "sweatfile") }
func (a authRepo) resolverArg() string { return filepath.Join(a.root, "resolver-arg") }

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

// writeResolverScript writes a resolver that records its first argument in
// dir/resolver-arg and prints json; it returns the url-resolver command.
func writeResolverScript(t *testing.T, dir, json string) string {
	t.Helper()
	path := filepath.Join(dir, "resolve.sh")
	writeFile(t, path, "printf '%s\\n' \"$1\" > "+filepath.Join(dir, "resolver-arg")+
		"\nprintf '%s\\n' '"+json+"'\n")
	return "sh " + path + " {origin}"
}

func (a authRepo) create(t *testing.T, allowNoCredential bool) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	_, err := Create(&buf, a.rp, CreateOpts{
		Format:            "tap",
		AllowStaleBase:    true,
		AllowNoCredential: allowNoCredential,
	}, nil)
	return buf.String(), err
}

func (a authRepo) wtGit(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", a.rp.AbsPath}, args...)...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestCreateResolvesOriginViaTrustedURLResolver(t *testing.T) {
	a := setupAuthRepo(t)
	cmd := writeResolverScript(t, a.root, `{"canonical_https":"https://vanity.test/repo.git"}`)
	writeFile(t, a.parentLayer(), "[auth]\nmint-command = \"echo tok\"\nrevoke-command = \"true\"\nurl-resolver = \""+cmd+"\"\n")

	out, err := a.create(t, false)
	if err != nil {
		t.Fatalf("Create: %v\n%s", err, out)
	}
	for _, want := range []string{
		"resolve origin feature-x https://vanity.test/repo.git",
		"mint credential feature-x",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	arg, err := os.ReadFile(a.resolverArg())
	if err != nil {
		t.Fatalf("resolver never ran: %v", err)
	}
	if got := strings.TrimSpace(string(arg)); got != authTestOrigin {
		t.Errorf("resolver arg = %q, want %q", got, authTestOrigin)
	}
	if got, _ := a.wtGit(t, "remote", "get-url", "origin"); got != "https://vanity.test/repo.git" {
		t.Errorf("origin url in worktree = %q", got)
	}
}

func TestCreateIgnoresRepoLayerURLResolver(t *testing.T) {
	a := setupAuthRepo(t)
	cmd := writeResolverScript(t, a.root, `{"canonical_https":"https://vanity.test/repo.git"}`)
	writeFile(t, a.parentLayer(), "[auth]\nmint-command = \"echo tok\"\nrevoke-command = \"true\"\n")
	writeFile(t, a.repoLayer(), "[auth]\nurl-resolver = \""+cmd+"\"\n")

	out, err := a.create(t, false)
	if err != nil {
		t.Fatalf("Create: %v\n%s", err, out)
	}
	var skip string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "# SKIP") && strings.Contains(line, "url-resolver") {
			skip = line
		}
	}
	if skip == "" || !strings.Contains(skip, "repo/sweatfile") {
		t.Errorf("want a SKIP naming url-resolver and repo/sweatfile, got:\n%s", out)
	}
	if _, err := os.Stat(a.resolverArg()); err == nil {
		t.Error("untrusted resolver ran")
	}
	got, err := a.wtGit(t, "config", "--worktree", "--get", "url.https://127.0.0.1/.insteadOf")
	if err != nil || got != "ssh://git@127.0.0.1:1/" {
		t.Errorf("built-in rewrite = %q, %v", got, err)
	}
}

func failingResolverLayer(a authRepo) (marker string, layer string) {
	marker = filepath.Join(a.root, "mint-marker")
	layer = "[auth]\nmint-command = \"touch " + marker + "; echo tok\"\nrevoke-command = \"true\"\n" +
		"url-resolver = \"echo resolver-said-no >&2; exit 7\"\n"
	return marker, layer
}

func TestCreateURLResolverFailureRefusesAndTearsDown(t *testing.T) {
	a := setupAuthRepo(t)
	marker, layer := failingResolverLayer(a)
	writeFile(t, a.parentLayer(), layer)

	out, err := a.create(t, false)
	if err == nil {
		t.Fatalf("Create succeeded; output:\n%s", out)
	}
	for _, want := range []string{"url-resolver", "resolver-said-no", "--allow-no-credential"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
	if _, statErr := os.Stat(a.rp.AbsPath); statErr == nil {
		t.Error("worktree not torn down")
	}
	cmd := exec.Command("git", "-C", a.repo, "rev-parse", "--verify", "refs/heads/feature-x")
	if cmd.Run() == nil {
		t.Error("branch not deleted")
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("mint ran despite resolver failure")
	}
}

func TestCreateURLResolverFailureWithAllowNoCredentialSkipsWholeLane(t *testing.T) {
	a := setupAuthRepo(t)
	_, layer := failingResolverLayer(a)
	writeFile(t, a.parentLayer(), layer)

	out, err := a.create(t, true)
	if err != nil {
		t.Fatalf("Create: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(a.rp.AbsPath); statErr != nil {
		t.Fatalf("worktree missing: %v", statErr)
	}
	if !strings.Contains(out, "not ok") || !strings.Contains(out, "mint credential feature-x") ||
		!strings.Contains(out, "severity: warn") || !strings.Contains(out, "url-resolver") {
		t.Errorf("want a warn not-ok naming url-resolver:\n%s", out)
	}
	if got, err := a.wtGit(t, "config", "--worktree", "--get-regexp", `^url\.`); err == nil {
		t.Errorf("url rewrite present: %q", got)
	}
	if got, err := a.wtGit(t, "config", "--worktree", "--get", "credential.helper"); err == nil {
		t.Errorf("credential.helper present: %q", got)
	}
	if _, statErr := os.Stat(filepath.Join(a.rp.AbsPath, ".spinclass", "git-credentials")); statErr == nil {
		t.Error("git-credentials written")
	}
}

// Regression pin for Decision 6: with no resolver anywhere the built-in
// ssh→https rewrite is unchanged.
func TestCreateWithoutURLResolverKeepsBuiltinRewrite(t *testing.T) {
	a := setupAuthRepo(t)
	writeFile(t, a.parentLayer(), "[auth]\nmint-command = \"echo tok\"\nrevoke-command = \"true\"\n")

	out, err := a.create(t, false)
	if err != nil {
		t.Fatalf("Create: %v\n%s", err, out)
	}
	if strings.Contains(out, "resolve origin") {
		t.Errorf("unexpected resolve point:\n%s", out)
	}
	got, err := a.wtGit(t, "config", "--worktree", "--get", "url.https://127.0.0.1/.insteadOf")
	if err != nil || got != "ssh://git@127.0.0.1:1/" {
		t.Errorf("built-in rewrite = %q, %v", got, err)
	}
}
