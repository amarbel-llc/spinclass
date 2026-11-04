package spawn

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"code.linenisgreat.com/spinclass/internal/alfa/worktree"
)

// A confined spawn gowns at the door (spinclass#462): the spawn-entry's exact
// `direnv exec {dir}` prefix (already substituted to the worktree path by the
// time the confined branch sees it) is dropped so the worker's clown starts
// with no devShell and no .envrc chain. Any other shape passes through as is.
func TestStripDirenvExecPrefix(t *testing.T) {
	const wt = "/w/.worktrees/x"
	cases := []struct {
		name  string
		argv  []string
		want  []string
		strip bool
	}{
		{
			name:  "the canonical prefix is dropped",
			argv:  []string{"direnv", "exec", wt, "clown", "--clown-attach=spawn", "--", "brief"},
			want:  []string{"clown", "--clown-attach=spawn", "--", "brief"},
			strip: true,
		},
		{
			name: "an entry with no direnv is untouched",
			argv: []string{"clown", "--", "brief"},
			want: []string{"clown", "--", "brief"},
		},
		{
			name: "direnv exec of another directory is not the prefix",
			argv: []string{"direnv", "exec", "/elsewhere", "clown", "--", "brief"},
			want: []string{"direnv", "exec", "/elsewhere", "clown", "--", "brief"},
		},
		{
			name: "a bare direnv exec with nothing after it is untouched",
			argv: []string{"direnv", "exec", wt},
			want: []string{"direnv", "exec", wt},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, stripped := stripDirenvExec(c.argv, wt)
			if !slices.Equal(got, c.want) || stripped != c.strip {
				t.Errorf("stripDirenvExec = %q, %v; want %q, %v", got, stripped, c.want, c.strip)
			}
		})
	}
}

// The unit spec's env is spinclass's whole gowning kit: the identity variables,
// the tmp dirs and the rendered [direnv.dotenv] pairs, with the identity
// winning over a dotenv entry of the same name.
func TestConfinedSpecEnvCarriesIdentityTmpAndDotenv(t *testing.T) {
	rp := worktree.ResolvedPath{SessionKey: "worker/w", RepoPath: "/r", AbsPath: "/r/.worktrees/w", Branch: "w"}
	env := confinedSpecEnv(rp, "desc", []string{
		"PILOT_IPC=/r/.worktrees/w/.tmp/ipc.sock",
		"SPINCLASS_SESSION_ID=forged",
	})
	want := map[string]string{
		"SPINCLASS_SESSION_ID":  "worker/w",
		"SPINCLASS_REPO":        "worker",
		"SPINCLASS_BRANCH":      "w",
		"SPINCLASS_WORKTREE":    "/r/.worktrees/w",
		"SPINCLASS_DESCRIPTION": "desc",
		"TMPDIR":                "/r/.worktrees/w/.tmp",
		"CLAUDE_CODE_TMPDIR":    "/r/.worktrees/w/.tmp",
		"PILOT_IPC":             "/r/.worktrees/w/.tmp/ipc.sock",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, env[k], v)
		}
	}
	if len(env) != len(want) {
		t.Errorf("env has %d entries, want %d: %v", len(env), len(want), env)
	}
}

// dotenvSpawnSweatfile dumps the worker's environment and declares a dotenv
// entry; the worker must see it without direnv, since a confined spawn runs
// no direnv at all.
const dotenvSpawnSweatfile = `[session-entry]
spawn-entry = ["sh", "-c", 'env > "$PWD/env.txt"; touch "$PWD/launched"', "sh", "--", "{prompt}"]

[direnv.dotenv]
PILOT_IPC = "$WORKTREE/.tmp/ipc.sock"
SPINCLASS_SESSION_ID = "forged"
`

func TestLaunchCarriesDotenvInWorkerEnv(t *testing.T) {
	res, _ := launchWithProfile(t, dotenvSpawnSweatfile, "")
	data, err := os.ReadFile(filepath.Join(res.WorktreePath, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	envTxt := string(data)
	if got, want := envValue(t, envTxt, "PILOT_IPC"), filepath.Join(res.WorktreePath, ".tmp", "ipc.sock"); got != want {
		t.Errorf("PILOT_IPC = %q, want %q (the dotenv pair, $WORKTREE expanded)", got, want)
	}
	if got := envValue(t, envTxt, "SPINCLASS_SESSION_ID"); got != res.SessionKey {
		t.Errorf("SPINCLASS_SESSION_ID = %q, want %q (the identity wins over a dotenv entry)", got, res.SessionKey)
	}
}
