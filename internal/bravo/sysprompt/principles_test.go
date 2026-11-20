package sysprompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

// The principles file named by the trusted [sysprompt] principles-file is
// rendered verbatim as the first appended section; a missing or unreadable
// file renders nothing and one warning line instead (fail soft).

func TestRenderPrinciplesReadsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "principles.md")
	if err := os.WriteFile(path, []byte("## Principles\n\n- P1 keep it small\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	section, warning := renderPrinciples(path)
	if section != "## Principles\n\n- P1 keep it small" || warning != "" {
		t.Errorf("renderPrinciples = %q, %q; want the trimmed file and no warning", section, warning)
	}
}

func TestRenderPrinciplesExpandsALeadingTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "circus")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "principles.md"), []byte("tilde works"), 0o644); err != nil {
		t.Fatal(err)
	}
	if section, warning := renderPrinciples("~/.config/circus/principles.md"); section != "tilde works" || warning != "" {
		t.Errorf("renderPrinciples(~/...) = %q, %q; want the file under $HOME", section, warning)
	}
}

func TestRenderPrinciplesFailsSoftOnAMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.md")
	section, warning := renderPrinciples(missing)
	if section != "" {
		t.Errorf("a missing file renders no section, got %q", section)
	}
	if !strings.HasPrefix(warning, "⚠") || !strings.Contains(warning, missing) {
		t.Errorf("want one warning line naming the file, got %q", warning)
	}
}

func TestRenderPrinciplesUnsetIsSilent(t *testing.T) {
	if section, warning := renderPrinciples(""); section != "" || warning != "" {
		t.Errorf("unset: got %q, %q; want nothing", section, warning)
	}
}

// A session in a worktree sits under its repo, so the repo's own sweatfile is a
// parent directory of the worktree; it must still not choose the principles
// file. The global sweatfile does.
func TestLoadIndexesTakesThePrinciplesFileOnlyFromAboveTheRepo(t *testing.T) {
	testgit.RequireGit(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "work", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustInit(t, repo)
	wt := filepath.Join(repo, ".worktrees", "feat")
	testgit.MustWorktreeAdd(t, repo, wt, "feat")
	for name, body := range map[string]string{"evil.md": "EVIL", "good.md": "GOOD"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	testgit.MustWriteFile(t, repo, "sweatfile", "[sysprompt]\nprinciples-file = \"~/evil.md\"\n")

	if got := loadIndexes(wt); got.Principles != "" {
		t.Fatalf("the repo's own sweatfile chose the principles: %q", got.Principles)
	}

	global := filepath.Join(home, ".config", "spinclass")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustWriteFile(t, global, "sweatfile", "[sysprompt]\nprinciples-file = \"~/good.md\"\n")
	if got := loadIndexes(wt); got.Principles != "GOOD" {
		t.Fatalf("the global principles-file was not rendered: %q", got.Principles)
	}
}

func TestRenderPutsPrinciplesFirstAmongTheAppendedSections(t *testing.T) {
	got, err := Render(Coordinates{
		Mode:              ModeWorktree,
		Principles:        "PRINCIPLES",
		DesignRecords:     "DESIGN",
		Manpages:          "MANPAGES",
		PrinciplesWarning: "⚠ principles warning",
	})
	if err != nil {
		t.Fatal(err)
	}
	p, d, m := strings.Index(got, "PRINCIPLES"), strings.Index(got, "DESIGN"), strings.Index(got, "MANPAGES")
	if p < 0 || d < 0 || m < 0 || p > d || d > m {
		t.Errorf("want principles, then design records, then manpages; positions %d %d %d", p, d, m)
	}
	if !strings.Contains(got, "⚠ principles warning") {
		t.Error("the principles warning must be rendered")
	}
}
