package validate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSweatfile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func TestRunValidHierarchy(t *testing.T) {
	home := t.TempDir()
	repoDir := filepath.Join(home, "eng", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	globalPath := filepath.Join(home, ".config", "spinclass", "sweatfile")
	writeSweatfile(t, globalPath, "[claude]\nallow = [\"Read\", \"Bash(git *)\"]")

	repoSweatfile := filepath.Join(repoDir, "sweatfile")
	writeSweatfile(t, repoSweatfile, "[git]\nexcludes = [\".direnv/\"]")

	var buf bytes.Buffer
	exitCode := Run(&buf, home, repoDir)

	out := buf.String()
	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d\noutput:\n%s", exitCode, out)
	}
	if !strings.HasPrefix(out, "TAP version 14\n") {
		t.Errorf("expected TAP version header, got: %q", out)
	}
	if !strings.Contains(out, "# Subtest:") {
		t.Errorf("expected subtests, got: %q", out)
	}
}

func TestRunWarnsOnRepoLayerURLResolver(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", home)
	repoDir := filepath.Join(home, "eng", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSweatfile(t, filepath.Join(repoDir, "sweatfile"), "[auth]\nurl-resolver = \"x\"\n")

	var buf bytes.Buffer
	Run(&buf, home, repoDir)
	out := buf.String()
	if !strings.Contains(out, "auth valid # warning") || !strings.Contains(out, "url-resolver") {
		t.Errorf("expected url-resolver warning, got:\n%s", out)
	}
	if strings.Contains(out, "not ok") {
		t.Errorf("url-resolver reported as unknown field:\n%s", out)
	}
}

func TestRunAcceptsParentLayerURLResolver(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", home)
	repoDir := filepath.Join(home, "eng", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSweatfile(t, filepath.Join(home, "eng", "sweatfile"), "[auth]\nurl-resolver = \"x\"\n")

	var buf bytes.Buffer
	Run(&buf, home, repoDir)
	out := buf.String()
	if strings.Contains(out, "url-resolver") || strings.Contains(out, "not ok") {
		t.Errorf("parent-layer url-resolver should be clean, got:\n%s", out)
	}
}

func TestRunWarnsOnRepoLayerRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", home)
	repoDir := filepath.Join(home, "eng", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSweatfile(t, filepath.Join(repoDir, "sweatfile"), "[session-entry]\nrestart = [\"clown\", \"restart\"]\n")

	var buf bytes.Buffer
	Run(&buf, home, repoDir)
	out := buf.String()
	if !strings.Contains(out, "session-entry valid # warning") || !strings.Contains(out, "restart") {
		t.Errorf("expected restart warning, got:\n%s", out)
	}
	if strings.Contains(out, "not ok") {
		t.Errorf("restart reported as an error:\n%s", out)
	}
}

func TestRunAcceptsParentLayerRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", home)
	repoDir := filepath.Join(home, "eng", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSweatfile(t, filepath.Join(home, "eng", "sweatfile"), "[session-entry]\nrestart = [\"clown\", \"restart\"]\n")

	var buf bytes.Buffer
	Run(&buf, home, repoDir)
	out := buf.String()
	if strings.Contains(out, "warning") || strings.Contains(out, "not ok") {
		t.Errorf("parent-layer restart should be clean, got:\n%s", out)
	}
}

func runInheritedHooks(t *testing.T, parentBody, repoBody string) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", home)
	repoDir := filepath.Join(home, "eng", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	parentPath := filepath.Join(home, "eng", "sweatfile")
	if parentBody != "" {
		writeSweatfile(t, parentPath, parentBody)
	}
	if repoBody != "" {
		writeSweatfile(t, filepath.Join(repoDir, "sweatfile"), repoBody)
	}
	var buf bytes.Buffer
	Run(&buf, home, repoDir)
	return buf.String(), parentPath
}

func TestRunWarnsOnInheritedCreateHook(t *testing.T) {
	out, parentPath := runInheritedHooks(t,
		"[hooks]\ncreate = \"parent-create\"\n",
		"[git]\nexcludes = [\".direnv/\"]\n")
	if !strings.Contains(out, "hooks inherited # warning") ||
		!strings.Contains(out, "`create`") ||
		!strings.Contains(out, parentPath) {
		t.Errorf("expected inherited create warning naming %s, got:\n%s", parentPath, out)
	}
	if strings.Contains(out, "not ok") {
		t.Errorf("inherited hook reported as an error:\n%s", out)
	}
}

func TestRunNoInheritedHookWarningWhenRepoSetsCreate(t *testing.T) {
	out, _ := runInheritedHooks(t,
		"[hooks]\ncreate = \"parent-create\"\n",
		"[hooks]\ncreate = \"repo-create\"\n")
	if strings.Contains(out, "hooks inherited") {
		t.Errorf("repo sets create; expected no inherited warning, got:\n%s", out)
	}
}

func TestRunNoInheritedHookWarningWhenRepoClearsCreate(t *testing.T) {
	out, _ := runInheritedHooks(t,
		"[hooks]\ncreate = \"parent-create\"\n",
		"[hooks]\ncreate = \"\"\n")
	if strings.Contains(out, "hooks inherited") {
		t.Errorf("repo clears create with \"\"; expected no inherited warning, got:\n%s", out)
	}
}

func TestRunNoInheritedHookWarningWhenNoLayerSetsCreate(t *testing.T) {
	out, _ := runInheritedHooks(t,
		"[git]\nexcludes = [\".envrc\"]\n",
		"[git]\nexcludes = [\".direnv/\"]\n")
	if strings.Contains(out, "hooks inherited") {
		t.Errorf("no layer sets create; expected no inherited warning, got:\n%s", out)
	}
}

func TestRunInvalidSyntax(t *testing.T) {
	home := t.TempDir()
	repoDir := filepath.Join(home, "eng", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	repoSweatfile := filepath.Join(repoDir, "sweatfile")
	writeSweatfile(t, repoSweatfile, "[claude]\nallow = [\"Bash(git *\"]")

	var buf bytes.Buffer
	exitCode := Run(&buf, home, repoDir)

	if exitCode != 1 {
		t.Errorf("expected exit code 1, got %d", exitCode)
	}
	if !strings.Contains(buf.String(), "not ok") {
		t.Errorf("expected not ok in output, got: %q", buf.String())
	}
}

func TestRunNoSweatfiles(t *testing.T) {
	home := t.TempDir()
	repoDir := filepath.Join(home, "eng", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	exitCode := Run(&buf, home, repoDir)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
	if !strings.Contains(buf.String(), "# SKIP") {
		t.Errorf("expected SKIP directives, got: %q", buf.String())
	}
}

func TestRunInvalidTOML(t *testing.T) {
	home := t.TempDir()
	repoDir := filepath.Join(home, "eng", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	repoSweatfile := filepath.Join(repoDir, "sweatfile")
	writeSweatfile(t, repoSweatfile, `this is not valid toml [[[`)

	var buf bytes.Buffer
	exitCode := Run(&buf, home, repoDir)

	if exitCode != 1 {
		t.Errorf("expected exit code 1, got %d", exitCode)
	}
	if !strings.Contains(buf.String(), "not ok") {
		t.Errorf("expected not ok in output, got: %q", buf.String())
	}
}

func TestRunDuplicatesInMerged(t *testing.T) {
	home := t.TempDir()
	repoDir := filepath.Join(home, "eng", "myrepo")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	globalPath := filepath.Join(home, ".config", "spinclass", "sweatfile")
	writeSweatfile(t, globalPath, "[claude]\nallow = [\"Read\"]")

	repoSweatfile := filepath.Join(repoDir, "sweatfile")
	writeSweatfile(t, repoSweatfile, "[claude]\nallow = [\"Read\"]")

	var buf bytes.Buffer
	exitCode := Run(&buf, home, repoDir)

	// Duplicates are warnings, not errors — exit code 0
	if exitCode != 0 {
		t.Errorf("expected exit code 0 (warnings only), got %d\noutput:\n%s", exitCode, buf.String())
	}
}
