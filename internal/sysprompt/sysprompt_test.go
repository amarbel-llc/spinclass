package sysprompt

import (
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/repoinfo"
)

// envFunc builds a getenv stub from a map.
func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func wdFunc(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

// noRepo is a repo-enrichment stub that resolves nothing.
func noRepo(string) repoinfo.RepoInfo { return repoinfo.RepoInfo{} }

// repoStub returns a fetcher that records the path it was called with and
// returns a fixed RepoInfo.
func repoStub(info repoinfo.RepoInfo, gotPath *string) func(string) repoinfo.RepoInfo {
	return func(p string) repoinfo.RepoInfo {
		if gotPath != nil {
			*gotPath = p
		}
		return info
	}
}

// noDocs is a design-record loader stub that renders nothing.
func noDocs(string) string { return "" }

// noCoActive is a co-active-session loader stub that finds nothing.
func noCoActive(Mode, string) string { return "" }

// docsStub records the root it was called with and returns a fixed section.
func docsStub(section string, gotRoot *string) func(string) string {
	return func(root string) string {
		if gotRoot != nil {
			*gotRoot = root
		}
		return section
	}
}

func TestResolveWorktreeMode(t *testing.T) {
	wt := "/repos/myrepo/.worktrees/feat-x"
	env := map[string]string{
		"SPINCLASS_SESSION_ID":  "myrepo/feat-x",
		"SPINCLASS_REPO":        "myrepo",
		"SPINCLASS_BRANCH":      "feat-x",
		"SPINCLASS_WORKTREE":    wt,
		"SPINCLASS_DESCRIPTION": "do a thing",
		"CLOWN_GROUP_ID":        "myrepo/feat-x",
	}
	// cwd inside the worktree (a subdirectory) must still resolve to worktree.
	var fetchedPath, docsRoot string
	want := repoinfo.RepoInfo{ForgeKind: "github", Owner: "o", Name: "r", URL: "https://github.com/o/r"}
	c := resolve(envFunc(env), wdFunc(filepath.Join(wt, "internal", "pkg")), repoStub(want, &fetchedPath), docsStub("## Design records\n\n**proposed**\n- FDR 0021 — X", &docsRoot), noCoActive)

	// The docs index is rendered from the worktree root, not a subdirectory,
	// and appended to the fragment via Render.
	if docsRoot != wt {
		t.Errorf("docs rendered from %q, want worktree %q", docsRoot, wt)
	}
	if !strings.Contains(c.DesignRecords, "FDR 0021") {
		t.Errorf("DesignRecords not wired: %q", c.DesignRecords)
	}

	if c.Mode != ModeWorktree {
		t.Fatalf("Mode: got %q, want %q", c.Mode, ModeWorktree)
	}
	if c.SessionKey != "myrepo/feat-x" || c.Branch != "feat-x" || c.Worktree != wt {
		t.Errorf("coordinates: got %+v", c)
	}
	if c.Description != "do a thing" || c.GroupID != "myrepo/feat-x" {
		t.Errorf("description/group: got %+v", c)
	}
	// RepoInfo is fetched from the worktree path, not a subdirectory.
	if fetchedPath != wt {
		t.Errorf("repo fetched from %q, want worktree %q", fetchedPath, wt)
	}
	if c.RepoInfo != want {
		t.Errorf("RepoInfo: got %+v, want %+v", c.RepoInfo, want)
	}
}

// A nested clown launched from within a worktree session inherits
// SPINCLASS_WORKTREE; when the serve cwd is NOT inside it, the env var alone
// must not mislabel the session as a worktree.
func TestResolveWorktreeEnvButCwdOutside(t *testing.T) {
	env := map[string]string{"SPINCLASS_WORKTREE": "/repos/myrepo/.worktrees/feat-x"}
	c := resolve(envFunc(env), wdFunc("/somewhere/else/not-a-repo"), noRepo, noDocs, noCoActive)

	if c.Mode != ModeMainCheckout {
		t.Fatalf("Mode: got %q, want %q (cwd outside the inherited worktree)", c.Mode, ModeMainCheckout)
	}
}

func TestResolveMainCheckout(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir, "trunk")

	env := map[string]string{"CLOWN_SESSION_ID": "clown-key-123"}
	var fetchedPath, docsRoot string
	c := resolve(envFunc(env), wdFunc(dir), repoStub(repoinfo.RepoInfo{ForgeKind: "github"}, &fetchedPath), docsStub("", &docsRoot), noCoActive)

	// The docs index is rendered from the derived git toplevel.
	if docsRoot != c.Worktree {
		t.Errorf("docs rendered from %q, want toplevel %q", docsRoot, c.Worktree)
	}

	if c.Mode != ModeMainCheckout {
		t.Fatalf("Mode: got %q, want %q", c.Mode, ModeMainCheckout)
	}
	if c.Branch != "trunk" {
		t.Errorf("Branch: got %q, want %q", c.Branch, "trunk")
	}
	if c.Worktree == "" || c.Repo != filepath.Base(c.Worktree) {
		t.Errorf("Worktree/Repo: got worktree=%q repo=%q", c.Worktree, c.Repo)
	}
	if c.SessionKey != "clown-key-123" {
		t.Errorf("SessionKey: got %q, want CLOWN_SESSION_ID", c.SessionKey)
	}
	// RepoInfo is fetched from the derived git toplevel.
	if fetchedPath != c.Worktree {
		t.Errorf("repo fetched from %q, want toplevel %q", fetchedPath, c.Worktree)
	}
	if c.RepoInfo.ForgeKind != "github" {
		t.Errorf("RepoInfo not populated: %+v", c.RepoInfo)
	}
}

func TestRenderWorktreeVariant(t *testing.T) {
	got, err := Render(Coordinates{
		Mode:       ModeWorktree,
		SessionKey: "myrepo/feat-x",
		Branch:     "feat-x",
		Worktree:   "/repos/myrepo/.worktrees/feat-x",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	mustContain(t, got, "myrepo/feat-x") // live session key injected
	mustContain(t, got, "Worktree management")
	mustContain(t, got, "EnterWorktree") // worktree-only guidance present
	mustContain(t, got, "Background jobs (merge/check)")
	if strings.Contains(got, "main checkout") {
		t.Error("worktree variant must not mention 'main checkout'")
	}
}

func TestRenderMainCheckoutVariant(t *testing.T) {
	got, err := Render(Coordinates{
		Mode:       ModeMainCheckout,
		SessionKey: "clown-key-123",
		Repo:       "myrepo",
		Branch:     "trunk",
		Worktree:   "/repos/myrepo",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	mustContain(t, got, "main checkout")
	mustContain(t, got, "clown-key-123")
	mustContain(t, got, "Background jobs (merge/check)") // async guidance still applies
	// The worktree-only guidance must NOT leak into the main-checkout variant.
	if strings.Contains(got, "Worktree management") || strings.Contains(got, "EnterWorktree") {
		t.Error("main-checkout variant must omit worktree-management guidance")
	}
}

// The repository block renders provider/owner/link/description when the
// RepoInfo is populated, in both template variants.
func TestRenderRepoBlock(t *testing.T) {
	info := repoinfo.RepoInfo{
		ForgeKind:   "github",
		Owner:       "amarbel-llc",
		OwnerType:   "org",
		Name:        "spinclass",
		URL:         "https://code.linenisgreat.com/spinclass",
		Description: "worktree session manager",
	}
	for _, mode := range []Mode{ModeWorktree, ModeMainCheckout} {
		got, err := Render(Coordinates{Mode: mode, SessionKey: "k", RepoInfo: info})
		if err != nil {
			t.Fatalf("Render(%s): %v", mode, err)
		}
		mustContain(t, got, "amarbel-llc/spinclass")
		mustContain(t, got, "github")
		mustContain(t, got, "org")
		mustContain(t, got, "https://code.linenisgreat.com/spinclass")
		mustContain(t, got, "worktree session manager")
	}
}

// An empty RepoInfo omits the repository block entirely (no dangling
// "Repository:" label).
func TestRenderRepoBlockOmittedWhenEmpty(t *testing.T) {
	for _, mode := range []Mode{ModeWorktree, ModeMainCheckout} {
		got, err := Render(Coordinates{Mode: mode, SessionKey: "k"})
		if err != nil {
			t.Fatalf("Render(%s): %v", mode, err)
		}
		if strings.Contains(got, "Repository:") || strings.Contains(got, "Repository URL") {
			t.Errorf("%s: repo block should be omitted for empty RepoInfo:\n%s", mode, got)
		}
	}
}

// Render appends the design-record section, separated by a blank line, in
// both variants; an empty DesignRecords adds nothing.
func TestRenderDesignRecordsTrailer(t *testing.T) {
	section := "## Design records\n\n**proposed**\n- FDR 0021 — Composable dynamic system-prompt"
	for _, mode := range []Mode{ModeWorktree, ModeMainCheckout} {
		got, err := Render(Coordinates{Mode: mode, SessionKey: "k", DesignRecords: section})
		if err != nil {
			t.Fatalf("Render(%s): %v", mode, err)
		}
		mustContain(t, got, "## Design records")
		mustContain(t, got, "- FDR 0021 — Composable dynamic system-prompt")
		if !strings.HasSuffix(got, section) {
			t.Errorf("%s: design records not appended as trailer:\n%s", mode, got)
		}
		if !strings.Contains(got, "\n\n"+section) {
			t.Errorf("%s: design records not blank-line separated", mode)
		}

		none, err := Render(Coordinates{Mode: mode, SessionKey: "k"})
		if err != nil {
			t.Fatalf("Render(%s): %v", mode, err)
		}
		if strings.Contains(none, "## Design records") {
			t.Errorf("%s: empty DesignRecords must add no section", mode)
		}
	}
}

// A non-github forge produces a "Forge workflow" guidance block; GitHub does not.
func TestRenderForgeWorkflowBlock(t *testing.T) {
	forgeInfo := repoinfo.RepoInfo{
		ForgeKind: "forgejo",
		Owner:     "myorg",
		Name:      "myrepo",
		URL:       "https://code.example.com/myorg/myrepo",
	}
	for _, mode := range []Mode{ModeWorktree, ModeMainCheckout} {
		got, err := Render(Coordinates{Mode: mode, SessionKey: "k", RepoInfo: forgeInfo})
		if err != nil {
			t.Fatalf("Render(%s): %v", mode, err)
		}
		mustContain(t, got, "Forge workflow")
		mustContain(t, got, "forgejo")
		mustContain(t, got, "fj")
		mustContain(t, got, "smith")
		mustContain(t, got, "read-only")
	}

	// GitHub and no-URL cases must NOT render the forge workflow block.
	for _, tc := range []struct {
		name string
		info repoinfo.RepoInfo
	}{
		{"github", repoinfo.RepoInfo{ForgeKind: "github", Owner: "amarbel-llc", Name: "spinclass", URL: "https://code.linenisgreat.com/spinclass"}},
		{"no URL", repoinfo.RepoInfo{ForgeKind: "forgejo", Name: "myrepo"}},
	} {
		for _, mode := range []Mode{ModeWorktree, ModeMainCheckout} {
			got, err := Render(Coordinates{Mode: mode, SessionKey: "k", RepoInfo: tc.info})
			if err != nil {
				t.Fatalf("Render(%s/%s): %v", tc.name, mode, err)
			}
			if strings.Contains(got, "Forge workflow") {
				t.Errorf("%s/%s: forge workflow block must not render:\n%s", tc.name, mode, got)
			}
		}
	}
}

// The host timezone line renders when set and is omitted when empty, in both
// template variants.
func TestRenderTimezoneLine(t *testing.T) {
	for _, mode := range []Mode{ModeWorktree, ModeMainCheckout} {
		got, err := Render(Coordinates{Mode: mode, SessionKey: "k", Timezone: "America/New_York (UTC-04:00)"})
		if err != nil {
			t.Fatalf("Render(%s): %v", mode, err)
		}
		mustContain(t, got, "Host timezone: America/New_York (UTC-04:00)")

		none, err := Render(Coordinates{Mode: mode, SessionKey: "k"})
		if err != nil {
			t.Fatalf("Render(%s): %v", mode, err)
		}
		if strings.Contains(none, "Host timezone") {
			t.Errorf("%s: timezone line must be omitted when empty", mode)
		}
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("fragment missing %q\n---\n%s", needle, haystack)
	}
}

func gitInit(t *testing.T, dir, branch string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"checkout", "-b", branch},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		if _, err := git.Run(dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
}
