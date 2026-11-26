package keyholder

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// signingRig is a repository with a linked worktree, an operator whose git
// config asks for no signature, and a real ssh-agent holding one throwaway key
// that stands in for the holder.
type signingRig struct {
	t          *testing.T
	repo, wt   string
	base, home string
	keygen     string
	pair       Pair
	agentPID   *os.Process
	operator   []string // the environment of the operator's own shell
}

func tool(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("%s is not on PATH (the flake's nativeCheckInputs carry openssh and git): %v", name, err)
	}
	return p
}

// newKey makes a throwaway ed25519 key and returns its path and public line.
func newKey(t *testing.T, keygen, dir, name string) (path, pub string) {
	t.Helper()
	path = filepath.Join(dir, name)
	if out, err := exec.Command(keygen, "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", path).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	data, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	return path, strings.TrimSpace(string(data))
}

func newSigningRig(t *testing.T) *signingRig {
	t.Helper()
	// Short: an agent socket path has a 107-byte limit.
	root, err := os.MkdirTemp("", "khs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	r := &signingRig{
		t: t, repo: filepath.Join(root, "repo"), home: filepath.Join(root, "home"),
		base: filepath.Join(root, "state", "keyholder"), keygen: tool(t, "ssh-keygen"),
	}
	r.wt = filepath.Join(r.repo, ".worktrees", "w")
	pairDir := filepath.Join(r.base, "h")
	for _, d := range []string{r.home, pairDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	r.operator = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + r.home,
		"GIT_CONFIG_GLOBAL=" + filepath.Join(r.home, "gitconfig"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=w", "GIT_AUTHOR_EMAIL=w@example.invalid",
		"GIT_COMMITTER_NAME=w", "GIT_COMMITTER_EMAIL=w@example.invalid",
	}
	// The operator's global config names another signing format; the worker's
	// settings must override it.
	if err := os.WriteFile(filepath.Join(r.home, "gitconfig"), []byte("[gpg]\n\tformat = x509\n[gpg \"x509\"]\n\tprogram = /nonexistent/card-signer\n[user]\n\tsigningkey = operator-card\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	holderKey, holderPub := newKey(t, r.keygen, root, "holder")
	sock := filepath.Join(root, "agent.sock")
	agent := exec.Command(tool(t, "ssh-agent"), "-D", "-a", sock)
	agent.Env = r.operator
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	r.agentPID = agent.Process
	t.Cleanup(func() { _ = agent.Process.Kill(); _, _ = agent.Process.Wait() })
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(sock); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatal("ssh-agent never made its socket")
		}
	}
	add := exec.Command(tool(t, "ssh-add"), holderKey)
	add.Env = append(slices.Clone(r.operator), "SSH_AUTH_SOCK="+sock)
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("ssh-add: %v: %s", err, out)
	}
	// Only the agent holds the private half from here on.
	if err := os.Remove(holderKey); err != nil {
		t.Fatal(err)
	}
	r.pair = Pair{Dir: pairDir, AgentSock: sock, PublicKey: holderPub}

	r.mustGit(r.operator, root, "init", "-q", "-b", "main", r.repo)
	r.mustGit(r.operator, r.repo, "commit", "-q", "--allow-empty", "-m", "base")
	r.mustGit(r.operator, r.repo, "worktree", "add", "-q", "-b", "w", r.wt)
	return r
}

func (r *signingRig) git(env []string, dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (r *signingRig) mustGit(env []string, dir string, args ...string) string {
	r.t.Helper()
	out, err := r.git(env, dir, args...)
	if err != nil {
		r.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}

// worker is the environment a holder worker runs git in: the operator's, plus
// what a launch adds.
func (r *signingRig) worker(otherSigners []byte, inherited ...string) []string {
	r.t.Helper()
	base := append(slices.Clone(r.operator), inherited...)
	kh, err := GitSigningEnv(r.base, "repo/w", r.keygen, r.pair, base, otherSigners)
	if err != nil {
		r.t.Fatal(err)
	}
	env := append(base, "SSH_AUTH_SOCK="+r.pair.AgentSock)
	for k, v := range kh {
		env = append(env, k+"="+v)
	}
	return env
}

func (r *signingRig) signed(env []string, rev string) bool {
	r.t.Helper()
	return strings.Contains(r.mustGit(env, r.wt, "cat-file", "commit", rev), "BEGIN SSH SIGNATURE")
}

// verifiesAgainst checks HEAD against an allowed-signers file naming only pub.
func (r *signingRig) verifiesAgainst(pub string) bool {
	r.t.Helper()
	fields := strings.Fields(pub)
	file, err := os.CreateTemp(r.home, "only-*")
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := file.WriteString("* namespaces=\"git\" " + fields[0] + " " + fields[1] + "\n"); err != nil {
		r.t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		r.t.Fatal(err)
	}
	_, err = r.git(r.operator, r.wt, "-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile="+file.Name(), "verify-commit", "HEAD")
	return err == nil
}

func TestAPlainCommitInAHolderWorkerIsSignedByTheHolderKey(t *testing.T) {
	r := newSigningRig(t)
	worker := r.worker(nil)
	_, otherPub := newKey(t, r.keygen, r.home, "other")
	for name, args := range map[string][]string{
		"plain commit": {"commit", "-q", "--allow-empty", "-m", "plain"},
		"commit -S":    {"commit", "-q", "-S", "--allow-empty", "-m", "dash-S"},
	} {
		r.mustGit(worker, r.wt, args...)
		if !r.signed(worker, "HEAD") {
			t.Fatalf("%s: not signed", name)
		}
		if !r.verifiesAgainst(r.pair.PublicKey) {
			t.Errorf("%s: does not verify against the holder's key", name)
		}
		if r.verifiesAgainst(otherPub) {
			t.Errorf("%s: verifies against a key that is not the holder's", name)
		}
		// Inside the worker, with no flag at all.
		if out, err := r.git(worker, r.wt, "verify-commit", "HEAD"); err != nil {
			t.Errorf("%s: git verify-commit in the worker: %v: %s", name, err, out)
		}
	}
}

// grit's remote lane runs git without SSH_AUTH_SOCK, and a shell can reset it:
// the signing program uses the holder's socket all the same.
func TestTheSigningProgramUsesTheHolderSocketWhateverTheCallerHas(t *testing.T) {
	r := newSigningRig(t)
	env := append(r.worker(nil), "SSH_AUTH_SOCK=/nonexistent/another-agent")
	r.mustGit(env, r.wt, "commit", "-q", "--allow-empty", "-m", "x")
	if !r.verifiesAgainst(r.pair.PublicKey) {
		t.Error("the commit is not the holder's")
	}
}

func TestAFailedSignatureNamesTheKeyHolderAndWhatToType(t *testing.T) {
	r := newSigningRig(t)
	worker := r.worker(nil)
	if err := r.agentPID.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = r.agentPID.Wait()
	out, err := r.git(worker, r.wt, "commit", "--allow-empty", "-m", "x")
	if err == nil {
		t.Fatalf("committed with no holder: %s", out)
	}
	for _, want := range []string{
		"the key holder of session repo/w did not sign", r.pair.AgentSock,
		"sc session-key repo/w", "sc resume repo/w", "-c commit.gpgsign=false after git",
		// A holder that shows and still does not sign has a next step too.
		"it shows one and signing still fails",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the failure lacks %q:\n%s", want, out)
		}
	}
}

// The settings are the worker's environment, not the repository's or the
// worktree's: the operator's git in the same worktree, a merge's rebase
// included, signs as he configured it, and nothing is left to undo.
func TestTheOperatorsGitInTheSameWorktreeIsUntouched(t *testing.T) {
	r := newSigningRig(t)
	configs := []string{filepath.Join(r.repo, ".git", "config"), filepath.Join(r.repo, ".git", "worktrees", "w", "config.worktree")}
	read := func() string {
		var all string
		for _, p := range configs {
			data, _ := os.ReadFile(p)
			all += p + "\x00" + string(data) + "\x00"
		}
		return all
	}
	before := read()
	worker := r.worker(nil)
	r.mustGit(worker, r.wt, "commit", "-q", "--allow-empty", "-m", "worker's")
	if after := read(); after != before {
		t.Errorf("a git config file changed (%d bytes before, %d after)", len(before), len(after))
	}
	// The default branch moves, so a rebase replays the worker's commit.
	r.mustGit(r.operator, r.repo, "commit", "-q", "--allow-empty", "-m", "moved")
	r.mustGit(r.operator, r.wt, "rebase", "-q", "main")
	if r.signed(r.operator, "HEAD") {
		t.Error("the operator's rebase signed with something, though his config asks for no signature")
	}
}

func TestInheritedGitSettingsAreKeptAndAHoldersAreReplaced(t *testing.T) {
	r := newSigningRig(t)
	inherited := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.abbrev", "GIT_CONFIG_VALUE_0=12"}
	worker := r.worker(nil, inherited...)
	if got := r.mustGit(worker, r.wt, "config", "--get", "core.abbrev"); got != "12" {
		t.Errorf("an inherited setting was lost: core.abbrev = %q", got)
	}
	// A holder worker launching another: the child's settings replace the
	// parent's, they are not stacked on them.
	child := r.pair
	child.Dir = filepath.Join(r.base, "child")
	kh, err := GitSigningEnv(r.base, "repo/child", r.keygen, child, worker, nil)
	if err != nil {
		t.Fatal(err)
	}
	if kh["GIT_CONFIG_COUNT"] != "6" || kh["GIT_CONFIG_VALUE_0"] != "12" || kh["GIT_CONFIG_VALUE_5"] != AllowedSignersPath(child.Dir) {
		t.Errorf("child: count %s, 0 = %q, 5 = %q", kh["GIT_CONFIG_COUNT"], kh["GIT_CONFIG_VALUE_0"], kh["GIT_CONFIG_VALUE_5"])
	}
}

func TestSignaturesTheOperatorAlreadyVerifiesStillDo(t *testing.T) {
	r := newSigningRig(t)
	_, otherPub := newKey(t, r.keygen, r.home, "colleague")
	r.worker([]byte("colleague@example.invalid " + otherPub + "\n"))
	data, err := os.ReadFile(AllowedSignersPath(r.pair.Dir))
	if err != nil {
		t.Fatal(err)
	}
	holder := strings.Fields(r.pair.PublicKey)
	if !strings.Contains(string(data), holder[1]) || !strings.Contains(string(data), "colleague@example.invalid ") {
		t.Errorf("allowed signers lack the holder or the existing entry (%d bytes)", len(data))
	}
}
