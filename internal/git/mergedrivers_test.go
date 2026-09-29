package git

import (
	"os"
	"path/filepath"
	"testing"

	"code.linenisgreat.com/spinclass/internal/testgit"
)

// driverRepo builds main + feature where gen.txt (bound to merge=gen on both
// sides) and plain.txt are both changed on both sides, and only.txt on
// feature. It returns the repo with feature checked out.
func driverRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "gitconfig"))
	t.Setenv("GIT_CEILING_DIRECTORIES", root)

	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.MustInit(t, repo)
	testgit.MustSeedMergeDriverPath(t, repo, "gen.txt", "gen")
	testgit.MustWriteFile(t, repo, "plain.txt", "base\n")
	testgit.MustGit(t, repo, "add", "plain.txt")
	testgit.MustGit(t, repo, "commit", "-m", "base plain")

	testgit.MustGit(t, repo, "checkout", "-b", "feature")
	testgit.MustWriteFile(t, repo, "gen.txt", "feature\n")
	testgit.MustWriteFile(t, repo, "plain.txt", "feature\n")
	testgit.MustWriteFile(t, repo, "only.txt", "feature\n")
	testgit.MustGit(t, repo, "add", ".")
	testgit.MustGit(t, repo, "commit", "-m", "feature")

	testgit.MustGit(t, repo, "checkout", "main")
	testgit.MustWriteFile(t, repo, "gen.txt", "main\n")
	testgit.MustWriteFile(t, repo, "plain.txt", "main\n")
	testgit.MustGit(t, repo, "commit", "-am", "main")
	testgit.MustGit(t, repo, "checkout", "feature")
	return repo
}

// spinclass#324: the driver bound to the both-sides-changed gen.txt is in
// play (plain.txt has no driver, only.txt changed on one side); its program
// is off PATH until the test puts it there.
func TestMergeDriversInPlayResolvesBoundDriver(t *testing.T) {
	repo := driverRepo(t)
	testgit.MustGit(t, repo, "config", "merge.gen.driver", "spinclass-test-absent-driver %O %A %B")

	drivers, err := MergeDriversInPlay(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(drivers) != 1 || drivers[0].Name != "gen" || drivers[0].Argv0() != "spinclass-test-absent-driver" {
		t.Fatalf("drivers = %+v, want the gen driver", drivers)
	}
	if len(drivers[0].Paths) != 1 || drivers[0].Paths[0] != "gen.txt" {
		t.Errorf("Paths = %v, want [gen.txt]", drivers[0].Paths)
	}
	if reason := drivers[0].Unresolved(repo); reason == "" {
		t.Error("absent driver reported resolving")
	}

	testgit.MustPutOnPath(t, "spinclass-test-absent-driver", "#!/bin/sh\n")
	if reason := drivers[0].Unresolved(repo); reason != "" {
		t.Errorf("driver on PATH reported absent: %s", reason)
	}
}

// A `merge=` name with no merge.<name>.driver config, and git's builtin
// drivers, are never in play: git handles those itself, with markers.
func TestMergeDriversInPlayIgnoresUnboundAndBuiltin(t *testing.T) {
	repo := driverRepo(t)
	testgit.MustWriteFile(t, repo, ".gitattributes", "gen.txt merge=gen\nplain.txt merge=union\n")
	testgit.MustGit(t, repo, "commit", "-am", "attrs")
	drivers, err := MergeDriversInPlay(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(drivers) != 0 {
		t.Errorf("unbound/builtin drivers reported in play: %+v", drivers)
	}
}

// The replay's HEAD sits on theirs, so theirs' .gitattributes govern: a
// binding present only on the target is in play, one present only on ours
// is not.
func TestMergeDriversInPlayReadsAttributesFromTheirs(t *testing.T) {
	repo := driverRepo(t)
	testgit.MustGit(t, repo, "config", "merge.gen.driver", "x %O %A %B")
	testgit.MustGit(t, repo, "config", "merge.plainer.driver", "y %O %A %B")

	// ours drops the gen binding and adds one for plain.txt; main keeps gen only.
	testgit.MustWriteFile(t, repo, ".gitattributes", "plain.txt merge=plainer\n")
	testgit.MustGit(t, repo, "commit", "-am", "rebind on feature")

	drivers, err := MergeDriversInPlay(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(drivers) != 1 || drivers[0].Name != "gen" {
		t.Errorf("drivers = %+v, want [gen] (theirs' attributes, not ours')", drivers)
	}
}

// The rebase replays commit by commit, so a path an earlier commit edited and
// a later one reverted still goes through the driver.
func TestMergeDriversInPlayCountsRevertedEdits(t *testing.T) {
	repo := driverRepo(t)
	testgit.MustGit(t, repo, "config", "merge.gen.driver", "x %O %A %B")
	testgit.MustWriteFile(t, repo, "gen.txt", "base\n")
	testgit.MustGit(t, repo, "commit", "-am", "revert gen to base")

	drivers, err := MergeDriversInPlay(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(drivers) != 1 || drivers[0].Name != "gen" {
		t.Errorf("drivers = %+v, want [gen] despite the net no-op on gen.txt", drivers)
	}
}

// merge.default routes attribute-less paths to a driver too.
func TestMergeDriversInPlayHonoursMergeDefault(t *testing.T) {
	repo := driverRepo(t)
	testgit.MustGit(t, repo, "config", "merge.default", "dflt")
	testgit.MustGit(t, repo, "config", "merge.dflt.driver", "x %O %A %B")

	drivers, err := MergeDriversInPlay(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(drivers) != 1 || drivers[0].Name != "dflt" || len(drivers[0].Paths) != 1 || drivers[0].Paths[0] != "plain.txt" {
		t.Errorf("drivers = %+v, want dflt on plain.txt", drivers)
	}
}

// Unrelated histories have nothing to pre-flight; the rebase reports its own
// error.
func TestMergeDriversInPlayNoMergeBase(t *testing.T) {
	repo := driverRepo(t)
	testgit.MustGit(t, repo, "config", "merge.gen.driver", "x %O %A %B")
	testgit.MustGit(t, repo, "checkout", "--orphan", "orphan")
	testgit.MustGit(t, repo, "commit", "-m", "orphan root")
	drivers, err := MergeDriversInPlay(repo, "main")
	if err != nil || len(drivers) != 0 {
		t.Errorf("drivers, err = %+v, %v; want none, nil", drivers, err)
	}
}

// Resolves judges the command the way git's `sh -c` from the worktree top
// would: a relative path against dir, a bare word on PATH, and anything
// needing shell parsing (quotes, escapes, env prefix) or empty not at all.
func TestMergeDriverResolves(t *testing.T) {
	dir := t.TempDir()
	tools := filepath.Join(dir, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "drv"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		cmd  string
		want bool
	}{
		{`./tools/drv %O %A %B`, true},
		{filepath.Join(tools, "drv") + ` %O`, true},
		{`./tools/missing %O`, false},
		{`./tools/my\ drv %O`, true},
		{`"./tools/my drv" %O`, true},
		{`FOO=1 spinclass-test-absent-driver %O`, true},
		{``, true},
		{`spinclass-test-absent-driver %O`, false},
		{`sh -c true`, true},
	}
	for _, c := range cases {
		if got := (MergeDriver{Command: c.cmd}).Resolves(dir); got != c.want {
			t.Errorf("Resolves(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}
