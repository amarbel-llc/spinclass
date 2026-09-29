package auth

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
)

const vanityResolver = `printf '%s' '{"canonical_https":"https://vanity.example.com/repo.git","canonical_ssh":"ssh://git@vanity.example.com/repo.git","owner":"x"}'`

const scpOrigin = "git@forge.example.com:owner/repo.git"

func TestResolveBuiltinWhenNoResolver(t *testing.T) {
	parsed, err := ParseForgeRemote(scpOrigin)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Resolve(context.Background(), "", Identity{}, scpOrigin, parsed)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := Rewrite{CredentialHost: "forge.example.com", HTTPS: "https://forge.example.com/", From: []string{"git@forge.example.com:"}}
	if !reflect.DeepEqual(res.Rewrite, want) {
		t.Errorf("Rewrite = %+v, want %+v", res.Rewrite, want)
	}
	if res.OriginURL != scpOrigin || res.Forge != parsed {
		t.Errorf("Resolution = %+v", res)
	}
}

func TestMintWithURLResolverRewritesOriginToCanonical(t *testing.T) {
	repoPath, wtPath, id := setupRepo(t)
	envFile := filepath.Join(t.TempDir(), "env")
	sf := authSweatfile("env | grep '^SPINCLASS_' | sort > "+envFile+"; echo tok", "true")

	outcome, err := Mint(context.Background(), sf, id, vanityResolver)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if !outcome.Minted || outcome.Resolved != "https://vanity.example.com/repo.git" {
		t.Fatalf("outcome = %+v", outcome)
	}

	key := "url.https://vanity.example.com/repo.git.insteadOf"
	if got := runGit(t, wtPath, "config", "--worktree", "--get-all", key); got != scpOrigin+"\nssh://git@vanity.example.com/repo.git" {
		t.Errorf("insteadOf values = %q", got)
	}
	if out, err := exec.Command("git", "-C", wtPath, "config", "--worktree", "--get", "url.https://forge.example.com/.insteadOf").CombinedOutput(); err == nil {
		t.Errorf("built-in key was written: %s", out)
	}
	if got := runGit(t, wtPath, "remote", "get-url", "origin"); got != "https://vanity.example.com/repo.git" {
		t.Errorf("effective origin = %q", got)
	}
	raw, _ := os.ReadFile(filepath.Join(wtPath, ".spinclass", CredentialFile))
	if got := strings.TrimSpace(string(raw)); got != "https://spinclass:tok@vanity.example.com" {
		t.Errorf("credential line = %q", got)
	}
	env, _ := os.ReadFile(envFile)
	for _, want := range []string{
		"SPINCLASS_FORGE_HOST=vanity.example.com",
		"SPINCLASS_FORGE_REPO=repo",
		"SPINCLASS_ORIGIN_URL=" + scpOrigin,
	} {
		if !strings.Contains(string(env), want) {
			t.Errorf("mint env missing %q:\n%s", want, env)
		}
	}

	st, err := session.Read(repoPath, "feature-x")
	if err != nil || st.Credential == nil || st.Credential.Remote == nil {
		t.Fatalf("state = %+v err=%v", st, err)
	}
	r := st.Credential.Remote
	if !r.Resolved || r.HTTPS != "https://vanity.example.com/repo.git" || r.CredentialHost != "vanity.example.com" ||
		!reflect.DeepEqual(r.From, []string{scpOrigin, "ssh://git@vanity.example.com/repo.git"}) {
		t.Errorf("Remote = %+v", r)
	}
}

func TestURLResolverSeesOriginEnvAndRepoRootCwd(t *testing.T) {
	repoPath, _, id := setupRepo(t)
	dir := t.TempDir()
	a, e, d := filepath.Join(dir, "A"), filepath.Join(dir, "E"), filepath.Join(dir, "D")
	resolver := "printf '%s' {origin} > " + a + "; env | grep '^SPINCLASS_' | sort > " + e + "; pwd -P > " + d +
		`; printf '{"canonical_https":"https://forge.example.com/repo.git"}'`

	if _, err := Mint(context.Background(), authSweatfile("echo tok", "true"), id, resolver); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if got, _ := os.ReadFile(a); string(got) != scpOrigin {
		t.Errorf("{origin} substituted as %q", got)
	}
	env, _ := os.ReadFile(e)
	for _, want := range []string{
		"SPINCLASS_ORIGIN_URL=" + scpOrigin,
		"SPINCLASS_FORGE_HOST=forge.example.com",
		"SPINCLASS_FORGE_REPO=owner/repo",
		"SPINCLASS_SESSION_ID=repo/feature-x",
	} {
		if !strings.Contains(string(env), want) {
			t.Errorf("resolver env missing %q:\n%s", want, env)
		}
	}
	wantDir, _ := filepath.EvalSymlinks(repoPath)
	if got, _ := os.ReadFile(d); strings.TrimSpace(string(got)) != wantDir {
		t.Errorf("resolver cwd = %q, want %q", got, wantDir)
	}
}

func TestURLResolverFailuresAreFatalAndWriteNothing(t *testing.T) {
	cases := []struct {
		name     string
		resolver string
		timeout  time.Duration
		want     []string
	}{
		{"nonzero", `echo nope >&2; exit 7`, 0, []string{"url-resolver", "nope"}},
		{"empty", `true`, 0, []string{"no JSON"}},
		{"not json", `echo not-json`, 0, []string{"invalid JSON"}},
		{"missing field", `echo '{}'`, 0, []string{"canonical_https"}},
		{"http", `echo '{"canonical_https":"http://x/y.git"}'`, 0, []string{"not an https URL"}},
		{"bad ssh", `echo '{"canonical_https":"https://x/y.git","canonical_ssh":"/srv/y.git"}'`, 0, []string{"canonical_ssh"}},
		{"userinfo", `echo '{"canonical_https":"https://u:p@x/y.git"}'`, 0, []string{"userinfo"}},
		{"query", `echo '{"canonical_https":"https://x/y.git?a=b"}'`, 0, []string{"query or fragment"}},
		{"fragment", `echo '{"canonical_https":"https://x/y.git#f"}'`, 0, []string{"query or fragment"}},
		{"uppercase scheme", `echo '{"canonical_https":"HTTPS://x/y.git"}'`, 0, []string{"not an https URL"}},
		{"timeout", `sleep 5`, 200 * time.Millisecond, []string{"timed out"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repoPath, wtPath, id := setupRepo(t)
			if c.timeout > 0 {
				old := urlResolverTimeout
				urlResolverTimeout = c.timeout
				t.Cleanup(func() { urlResolverTimeout = old })
			}
			marker := filepath.Join(t.TempDir(), "minted")
			sf := authSweatfile("touch "+marker+"; echo tok", "true")

			_, err := Mint(context.Background(), sf, id, c.resolver)
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q missing %q", err, w)
				}
			}
			if _, serr := os.Stat(marker); serr == nil {
				t.Error("mint-command ran")
			}
			if Minted(wtPath) {
				t.Error("credential written")
			}
			if out, gerr := exec.Command("git", "-C", wtPath, "config", "--worktree", "--get-regexp", `^url\.`).CombinedOutput(); gerr == nil {
				t.Errorf("url rewrite written: %s", out)
			}
			if st, rerr := session.Read(repoPath, "feature-x"); rerr == nil && st.Credential != nil {
				t.Errorf("credential recorded: %+v", st.Credential)
			}
		})
	}
}

func TestURLResolverSkippedOutsideForgeHosts(t *testing.T) {
	_, _, id := setupRepo(t)
	marker := filepath.Join(t.TempDir(), "resolved")
	sf := authSweatfile("echo tok", "true")
	sf.Auth.ForgeHosts = []string{"github.com"}

	outcome, err := Mint(context.Background(), sf, id, "touch "+marker+`; echo '{"canonical_https":"https://x/y.git"}'`)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if outcome.Skipped == "" {
		t.Errorf("outcome = %+v, want skipped", outcome)
	}
	if _, serr := os.Stat(marker); serr == nil {
		t.Error("resolver ran for an unlisted host")
	}
}

func TestURLResolverCanonicalHostMustBeInForgeHosts(t *testing.T) {
	repoPath, wtPath, id := setupRepo(t)
	marker := filepath.Join(t.TempDir(), "minted")
	sf := authSweatfile("touch "+marker+"; echo tok", "true")
	sf.Auth.ForgeHosts = []string{"forge.example.com"}

	_, err := Mint(context.Background(), sf, id, vanityResolver)
	if err == nil || !strings.Contains(err.Error(), "forge-hosts") {
		t.Fatalf("err = %v, want a forge-hosts refusal", err)
	}
	if _, serr := os.Stat(marker); serr == nil {
		t.Error("mint-command ran")
	}
	if Minted(wtPath) {
		t.Error("credential written")
	}
	if st, rerr := session.Read(repoPath, "feature-x"); rerr == nil && st.Credential != nil {
		t.Errorf("credential recorded: %+v", st.Credential)
	}

	sf.Auth.ForgeHosts = []string{"forge.example.com", "vanity.example.com"}
	if outcome, err := Mint(context.Background(), sf, id, vanityResolver); err != nil || !outcome.Minted {
		t.Fatalf("both hosts listed: outcome=%+v err=%v", outcome, err)
	}
}

func TestURLResolverHostileOriginIsInert(t *testing.T) {
	repoPath, _, id := setupRepo(t)
	dir := t.TempDir()
	marker, a := filepath.Join(dir, "pwned"), filepath.Join(dir, "A")
	origin := "git@forge.example.com:o/re'po$(touch " + marker + ")`touch " + marker + "`.git"
	runGit(t, repoPath, "remote", "set-url", "origin", origin)
	resolver := "printf '%s' {origin} > " + a + `; printf '{"canonical_https":"https://forge.example.com/repo.git"}'`

	if _, err := Mint(context.Background(), authSweatfile("echo tok", "true"), id, resolver); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if got, _ := os.ReadFile(a); string(got) != origin {
		t.Errorf("origin mangled: %q", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("hostile origin executed")
	}
}

func TestURLResolverCanonicalEqualsHTTPSOriginWritesHelperOnly(t *testing.T) {
	repoPath, wtPath, id := setupRepo(t)
	const https = "https://forge.example.com/repo.git"
	runGit(t, repoPath, "remote", "set-url", "origin", https)

	if _, err := Mint(context.Background(), authSweatfile("echo tok", "true"), id, `printf '{"canonical_https":"`+https+`"}'`); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	credPath := filepath.Join(wtPath, ".spinclass", CredentialFile)
	if got := runGit(t, wtPath, "config", "--worktree", "--get", "credential.helper"); got != "store --file="+credPath {
		t.Errorf("credential.helper = %q", got)
	}
	if out, err := exec.Command("git", "-C", wtPath, "config", "--worktree", "--get-regexp", `^url\.`).CombinedOutput(); err == nil {
		t.Errorf("unexpected url rewrite: %s", out)
	}
}

func addLand(t *testing.T, repoPath string) string {
	t.Helper()
	land := filepath.Join(repoPath, ".worktrees", ".land-x")
	runGit(t, repoPath, "worktree", "add", "--detach", land, "HEAD")
	return land
}

func TestMirrorIntoReplaysStoredRemoteNotOrigin(t *testing.T) {
	repoPath, wtPath, id := setupRepo(t)
	if _, err := Mint(context.Background(), authSweatfile("echo tok", "true"), id, vanityResolver); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	runGit(t, repoPath, "remote", "set-url", "origin", "git@forge.example.com:other/thing.git")
	land := addLand(t, repoPath)

	if err := MirrorInto(repoPath, "feature-x", wtPath, land); err != nil {
		t.Fatalf("MirrorInto: %v", err)
	}
	got := runGit(t, land, "config", "--worktree", "--get-all", "url.https://vanity.example.com/repo.git.insteadOf")
	if got != scpOrigin+"\nssh://git@vanity.example.com/repo.git" {
		t.Errorf("landing insteadOf = %q", got)
	}
	if out, err := exec.Command("git", "-C", land, "config", "--worktree", "--get", "url.https://forge.example.com/.insteadOf").CombinedOutput(); err == nil {
		t.Errorf("built-in key leaked into landing: %s", out)
	}
}

func TestMirrorIntoLegacyRecordUsesBuiltinRewrite(t *testing.T) {
	repoPath, wtPath, id := setupRepo(t)
	if _, err := Mint(context.Background(), authSweatfile("echo tok", "true"), id, ""); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if err := session.UpdateCredential(repoPath, "feature-x", &session.Credential{MintedAt: time.Now()}); err != nil {
		t.Fatalf("UpdateCredential: %v", err)
	}
	land := addLand(t, repoPath)

	if err := MirrorInto(repoPath, "feature-x", wtPath, land); err != nil {
		t.Fatalf("MirrorInto: %v", err)
	}
	if got := runGit(t, land, "config", "--worktree", "--get", "url.https://forge.example.com/.insteadOf"); got != "git@forge.example.com:" {
		t.Errorf("landing insteadOf = %q", got)
	}
}
