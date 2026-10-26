package auth

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/session"
)

// Token pool intake (FDR 0028 "Token pool"): a papi-minted batch in
// $XDG_STATE_HOME/spinclass/token-pool is claimed by rename instead of running
// [auth].mint-command, so a coordinator can spawn while the operator's agent is
// away.

const poolSecret = "s3cret-pool-token-value"

type testSlot struct {
	session  string
	repo     string
	deadline string // RFC 3339, "" = none
	token    string // file content; "" = poolSecret
}

func poolDirForTest(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("XDG_STATE_HOME"), "spinclass", "token-pool")
}

// writePool lays down a papi-shaped pool (dir 0700, tokens 0600, manifest 0600).
func writePool(t *testing.T, forge string, slots ...testSlot) string {
	t.Helper()
	dir := poolDirForTest(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	type row struct {
		Session  string `json:"session"`
		Repo     string `json:"repo"`
		ID       int    `json:"id"`
		Deadline string `json:"deadline,omitempty"`
		File     string `json:"file"`
	}
	m := struct {
		Version int    `json:"version"`
		Forge   string `json:"forge"`
		User    string `json:"user"`
		Tokens  []row  `json:"tokens"`
	}{Version: 1, Forge: forge, User: "pool-user"}
	for i, s := range slots {
		file := escapeSession(s.session) + ".token"
		tok := s.token
		if tok == "" {
			tok = poolSecret
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(tok+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		m.Tokens = append(m.Tokens, row{Session: s.session, Repo: s.repo, ID: i + 1, Deadline: s.deadline, File: file})
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func poolSweatfile(mint, revoke string) sweatfile.Sweatfile {
	sf := authSweatfile(mint, revoke)
	sf.Auth.ForgeHosts = []string{"forge.example.com"}
	return sf
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func future() string { return time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339) }
func past() string   { return time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) }

func TestEscapeSessionMatchesPapi(t *testing.T) {
	for in, want := range map[string]string{
		"pool/spinclass/01": "pool_2Fspinclass_2F01",
		"a.b":               "a.b",
		"a-b":               "a_2Db",
		"AZaz09":            "AZaz09",
		"a_b":               "a_5Fb",
	} {
		if got := escapeSession(in); got != want {
			t.Errorf("escapeSession(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPoolClaimsMatchingRepoSlotByRename(t *testing.T) {
	_, wtPath, id := setupRepo(t)
	dir := writePool(t, "https://forge.example.com",
		testSlot{session: "pool/other/01", repo: "someone/else"},
		testSlot{session: "pool/spinclass/01", repo: "owner/repo", deadline: future(), token: "tok-for-owner-repo"},
	)

	outcome, err := Mint(context.Background(), poolSweatfile("", ""), id, "")
	if err != nil || !outcome.Minted {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	if exists(filepath.Join(dir, "pool_2Fspinclass_2F01.token")) {
		t.Error("claimed slot's token is still in the pool dir")
	}
	if !exists(filepath.Join(dir, "pool_2Fother_2F01.token")) {
		t.Error("an unrelated slot was taken")
	}
	raw, _ := os.ReadFile(CredentialPath(wtPath))
	if got := strings.TrimSpace(string(raw)); got != "https://spinclass:tok-for-owner-repo@forge.example.com" {
		t.Errorf("credential line = %q", got)
	}
	if got := runGit(t, wtPath, "config", "--worktree", "--get", "credential.helper"); !strings.Contains(got, "store --file=") {
		t.Errorf("credential.helper = %q", got)
	}
	credDir := filepath.Dir(CredentialPath(wtPath))
	entries, _ := os.ReadDir(credDir)
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(credDir, e.Name()))
		if e.Name() != filepath.Base(CredentialPath(wtPath)) && strings.Contains(string(b), "tok-for-owner-repo") {
			t.Errorf("claimed token left behind in %s", e.Name())
		}
	}
}

func TestPoolRecordsSlotSessionAndPinsPoolRevoke(t *testing.T) {
	repoPath, _, id := setupRepo(t)
	writePool(t, "https://forge.example.com", testSlot{session: "pool/spinclass/01", repo: "owner/repo"})

	// The configured revoke-command names $SPINCLASS_SESSION_ID; the pool form
	// must not, because the token's forge-side session is the slot's.
	sf := poolSweatfile("", "papi forge token revoke --session $SPINCLASS_SESSION_ID")
	if _, err := Mint(context.Background(), sf, id, ""); err != nil {
		t.Fatal(err)
	}
	st, err := session.Read(repoPath, "feature-x")
	if err != nil || st.Credential == nil || st.Credential.RevokeCommand == nil {
		t.Fatalf("state = %+v err=%v", st, err)
	}
	rc := *st.Credential.RevokeCommand
	if !strings.Contains(rc, "papi forge token revoke") || !strings.Contains(rc, "pool/spinclass/01") {
		t.Errorf("RevokeCommand = %q, want the slot session", rc)
	}
	if strings.Contains(rc, "SPINCLASS_SESSION_ID") {
		t.Errorf("RevokeCommand = %q still names the spinclass session", rc)
	}
	if st.Credential.Remote == nil || st.Credential.Remote.ForgeRepo != "owner/repo" {
		t.Errorf("Remote = %+v", st.Credential.Remote)
	}

	// And revoke actually runs that string.
	var ran string
	old := commandCaptureAmbient
	commandCaptureAmbient = func(_ context.Context, _, cmd string, _ []string) (string, error) {
		ran = cmd
		return "", nil
	}
	defer func() { commandCaptureAmbient = old }()
	if out, err := Revoke(context.Background(), id, io.Discard); err != nil || !out.Ran {
		t.Fatalf("Revoke: %+v %v", out, err)
	}
	if ran != rc {
		t.Errorf("revoke ran %q, want the pinned %q", ran, rc)
	}
}

func TestPoolConcurrentClaimsNeverTakeTheSameSlot(t *testing.T) {
	setupRepo(t)
	dir := writePool(t, "https://forge.example.com",
		testSlot{session: "pool/spinclass/01", repo: "owner/repo", token: "tok-1"},
		testSlot{session: "pool/spinclass/02", repo: "owner/repo", token: "tok-2"},
		testSlot{session: "pool/spinclass/03", repo: "owner/repo", token: "tok-3"},
	)
	const claimers = 12
	dest := t.TempDir()
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		claimed []string
		errs    []error
	)
	for i := 0; i < claimers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			c, err := claimPoolSlot(dir, "owner/repo", []string{"forge.example.com"}, "", time.Now(), filepath.Join(dest, strings.Repeat("x", n+1)))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			} else if c != nil {
				claimed = append(claimed, c.slot.Session)
			}
		}(i)
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	if len(claimed) != 3 {
		t.Fatalf("%d claims succeeded, want exactly 3: %v", len(claimed), claimed)
	}
	seen := map[string]bool{}
	for _, s := range claimed {
		if seen[s] {
			t.Errorf("slot %s claimed twice", s)
		}
		seen[s] = true
	}
}

func TestPoolNoMatchingSlotFallsBackToMintCommand(t *testing.T) {
	_, wtPath, id := setupRepo(t)
	dir := writePool(t, "https://forge.example.com", testSlot{session: "pool/other/01", repo: "someone/else"})

	outcome, err := Mint(context.Background(), poolSweatfile("echo minted-tok", "true"), id, "")
	if err != nil || !outcome.Minted {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	raw, _ := os.ReadFile(CredentialPath(wtPath))
	if !strings.Contains(string(raw), "minted-tok") {
		t.Errorf("credential = %q, want the mint-command's token", raw)
	}
	if !exists(filepath.Join(dir, "pool_2Fother_2F01.token")) {
		t.Error("an unmatched slot was consumed")
	}
	st, _ := session.Read(id.RepoPath, "feature-x")
	if st.Credential == nil || st.Credential.RevokeCommand == nil || *st.Credential.RevokeCommand != "true" {
		t.Errorf("fallback must pin the configured revoke-command: %+v", st.Credential)
	}
}

func TestPoolNoMatchAndNoMintCommandSkips(t *testing.T) {
	_, wtPath, id := setupRepo(t)
	writePool(t, "https://forge.example.com", testSlot{session: "pool/other/01", repo: "someone/else"})
	outcome, err := Mint(context.Background(), poolSweatfile("", ""), id, "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if outcome.Minted || outcome.Skipped == "" {
		t.Fatalf("outcome = %+v, want a visible skip", outcome)
	}
	if Minted(wtPath) {
		t.Error("credential written without a slot")
	}
}

func TestPoolPrefersPoolOverMintCommand(t *testing.T) {
	_, wtPath, id := setupRepo(t)
	marker := filepath.Join(t.TempDir(), "mint-ran")
	writePool(t, "https://forge.example.com", testSlot{session: "pool/spinclass/01", repo: "owner/repo", token: "tok-pool"})
	if _, err := Mint(context.Background(), poolSweatfile("touch "+marker+"; echo tok-mint", "true"), id, ""); err != nil {
		t.Fatal(err)
	}
	if exists(marker) {
		t.Error("mint-command ran although the pool had a matching slot")
	}
	raw, _ := os.ReadFile(CredentialPath(wtPath))
	if !strings.Contains(string(raw), "tok-pool") {
		t.Errorf("credential = %q", raw)
	}
}

func TestPoolSkipsExpiredDeadline(t *testing.T) {
	_, wtPath, id := setupRepo(t)
	dir := writePool(t, "https://forge.example.com",
		testSlot{session: "pool/spinclass/01", repo: "owner/repo", deadline: past(), token: "tok-expired"},
		testSlot{session: "pool/spinclass/02", repo: "owner/repo", deadline: future(), token: "tok-live"},
	)
	if _, err := Mint(context.Background(), poolSweatfile("", ""), id, ""); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(CredentialPath(wtPath))
	if !strings.Contains(string(raw), "tok-live") {
		t.Errorf("credential = %q, want the unexpired slot", raw)
	}
	if !exists(filepath.Join(dir, "pool_2Fspinclass_2F01.token")) {
		t.Error("the expired slot was consumed")
	}
}

func TestPoolOnlyExpiredSlotsFallBackToMintCommand(t *testing.T) {
	_, wtPath, id := setupRepo(t)
	writePool(t, "https://forge.example.com", testSlot{session: "pool/spinclass/01", repo: "owner/repo", deadline: past()})
	if _, err := Mint(context.Background(), poolSweatfile("echo minted-tok", "true"), id, ""); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(CredentialPath(wtPath))
	if !strings.Contains(string(raw), "minted-tok") {
		t.Errorf("credential = %q", raw)
	}
}

func TestPoolRefusesGroupOrWorldAccessibleDir(t *testing.T) {
	for _, mode := range []os.FileMode{0o750, 0o755, 0o701} {
		_, wtPath, id := setupRepo(t)
		dir := writePool(t, "https://forge.example.com", testSlot{session: "pool/spinclass/01", repo: "owner/repo"})
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(t.TempDir(), "mint-ran")
		_, err := Mint(context.Background(), poolSweatfile("touch "+marker+"; echo tok", "true"), id, "")
		if err == nil || !strings.Contains(err.Error(), "token-pool") {
			t.Fatalf("mode %o: err = %v, want a pool permission refusal", mode, err)
		}
		if strings.Contains(err.Error(), poolSecret) {
			t.Errorf("error leaks the token: %v", err)
		}
		if exists(marker) {
			t.Errorf("mode %o: fell back to the mint-command instead of refusing", mode)
		}
		if !exists(filepath.Join(dir, "pool_2Fspinclass_2F01.token")) {
			t.Errorf("mode %o: a slot was claimed from an unsafe pool", mode)
		}
		if Minted(wtPath) {
			t.Errorf("mode %o: credential written", mode)
		}
	}
}

func TestPoolRefusesLooseTokenFileWithoutLeakingIt(t *testing.T) {
	_, _, id := setupRepo(t)
	dir := writePool(t, "https://forge.example.com", testSlot{session: "pool/spinclass/01", repo: "owner/repo"})
	if err := os.Chmod(filepath.Join(dir, "pool_2Fspinclass_2F01.token"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Mint(context.Background(), poolSweatfile("", ""), id, "")
	if err == nil {
		t.Fatal("expected a refusal for a group/world-readable token file")
	}
	if strings.Contains(err.Error(), poolSecret) {
		t.Errorf("error leaks the token: %v", err)
	}
}

func TestPoolRefusesManifestHostOutsideForgeHosts(t *testing.T) {
	_, wtPath, id := setupRepo(t)
	dir := writePool(t, "https://evil.example.org", testSlot{session: "pool/spinclass/01", repo: "owner/repo"})
	_, err := Mint(context.Background(), poolSweatfile("", ""), id, "")
	if err == nil || !strings.Contains(err.Error(), "forge-hosts") {
		t.Fatalf("err = %v, want a forge-hosts refusal", err)
	}
	if strings.Contains(err.Error(), poolSecret) {
		t.Errorf("error leaks the token: %v", err)
	}
	if !exists(filepath.Join(dir, "pool_2Fspinclass_2F01.token")) || Minted(wtPath) {
		t.Error("a slot was claimed from a pool for a foreign host")
	}
}

// The real fleet: the manifest names papi's API host (forge.starbrandshoes.com),
// while [auth].forge-hosts lists the git plane (code.linenisgreat.com) and the
// pinned revoke-command names the API host. The pool must be usable there; before
// this every session creation with a pool errored on the host check.
func TestPoolAcceptsManifestHostNamedByThePinnedRevokeCommand(t *testing.T) {
	_, _, id := setupRepo(t)
	writePool(t, "https://api.example.net", testSlot{session: "pool/spinclass/01", repo: "owner/repo", deadline: future()})
	sf := poolSweatfile("", "papi forge token revoke --host api.example.net --user u --card-login --session $SPINCLASS_SESSION_ID")
	outcome, err := Mint(context.Background(), sf, id, "")
	if err != nil || !outcome.Minted {
		t.Fatalf("outcome=%+v err=%v; a manifest host named by the revoke-command's --host must be accepted", outcome, err)
	}
}

// papi records the slot's repo as the resolved owner/name; an owner-less origin
// (the vanity plane: .../spinclass.git -> "spinclass") must still claim it.
func TestPoolRepoMatchingToleratesAnOwnerlessOrigin(t *testing.T) {
	for _, tc := range []struct {
		slot, repo string
		want       bool
	}{
		{"linenisgreat/spinclass", "spinclass", true},
		{"linenisgreat/spinclass", "linenisgreat/spinclass", true},
		{"linenisgreat/spinclass", "other/spinclass", false},
		{"linenisgreat/spinclass", "spin", false},
		{"linenisgreat/spinclass", "class", false},
		{"spinclass", "spinclass", true},
		{"a/b/spinclass", "spinclass", true},
		{"linenisgreat/spinclass", "", false},
	} {
		if got := poolRepoMatches(tc.slot, tc.repo); got != tc.want {
			t.Errorf("poolRepoMatches(%q, %q) = %v, want %v", tc.slot, tc.repo, got, tc.want)
		}
	}
}

func TestPoolRevokeHostOf(t *testing.T) {
	for in, want := range map[string]string{
		"papi forge token revoke --host h.example --user u --card-login --session s": "h.example",
		"papi forge token revoke --host=h.example --user u":                          "h.example",
		"papi forge token revoke --user u":                                           "",
		"":                                                                           "",
	} {
		if got := revokeCommandHost(in); got != want {
			t.Errorf("revokeCommandHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPoolRefusesWhenForgeHostsUnset(t *testing.T) {
	_, _, id := setupRepo(t)
	writePool(t, "https://forge.example.com", testSlot{session: "pool/spinclass/01", repo: "owner/repo"})
	if _, err := Mint(context.Background(), authSweatfile("", ""), id, ""); err == nil {
		t.Fatal("a pool with no [auth].forge-hosts allow-list must be refused")
	}
}

func TestPoolRefusesSlotFileThatIsNotTheEscapedSession(t *testing.T) {
	_, _, id := setupRepo(t)
	dir := writePool(t, "https://forge.example.com", testSlot{session: "pool/spinclass/01", repo: "owner/repo"})
	outside := filepath.Join(filepath.Dir(dir), "outside.token")
	if err := os.WriteFile(outside, []byte(poolSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := `{"version":1,"forge":"https://forge.example.com","user":"u","tokens":[{"session":"pool/spinclass/01","repo":"owner/repo","id":1,"file":"../outside.token"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(m), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Mint(context.Background(), poolSweatfile("", ""), id, "")
	if err == nil {
		t.Fatal("expected a refusal for a traversing slot file")
	}
	if strings.Contains(err.Error(), poolSecret) {
		t.Errorf("error leaks the token: %v", err)
	}
	if !exists(outside) {
		t.Error("a file outside the pool was renamed")
	}
}

func TestPoolErrorsNeverContainTokenContents(t *testing.T) {
	// An empty token file, and an unparseable manifest, both fail without
	// echoing file contents.
	_, _, id := setupRepo(t)
	dir := writePool(t, "https://forge.example.com", testSlot{session: "pool/spinclass/01", repo: "owner/repo"})
	if err := os.WriteFile(filepath.Join(dir, "pool_2Fspinclass_2F01.token"), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Mint(context.Background(), poolSweatfile("", ""), id, ""); err == nil {
		t.Error("an empty token file must fail")
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{not json "+poolSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Mint(context.Background(), poolSweatfile("", ""), id, "")
	if err == nil {
		t.Fatal("an unparseable manifest must fail")
	}
	if strings.Contains(err.Error(), poolSecret) {
		t.Errorf("error echoes file contents: %v", err)
	}
}

func TestNoPoolDirLeavesMintCommandPathUntouched(t *testing.T) {
	_, wtPath, id := setupRepo(t)
	if _, err := Mint(context.Background(), poolSweatfile("echo plain-tok", "true"), id, ""); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(CredentialPath(wtPath))
	if !strings.Contains(string(raw), "plain-tok") {
		t.Errorf("credential = %q", raw)
	}
}
