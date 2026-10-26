package clean

import "testing"

func TestPlanCleanNeverReapsAShelvedWorktree(t *testing.T) {
	cases := []struct {
		name        string
		wt          worktreeInfo
		interactive bool
		want        string
	}{
		{"merged and clean", worktreeInfo{repo: "r", branch: "a", merged: true}, false, "remove"},
		{"merged with a shelf", worktreeInfo{repo: "r", branch: "a", merged: true, shelved: true}, false, "skip-shelved"},
		{"shelf wins over interactive review", worktreeInfo{repo: "r", branch: "a", merged: true, dirty: true, shelved: true}, true, "skip-shelved"},
		{"dirty without a shelf still reviews", worktreeInfo{repo: "r", branch: "a", merged: true, dirty: true}, true, "interactive"},
		// #231: a merged worktree whose session's agent still runs is not
		// disposable; neither --yes nor review removes it.
		{"live session is skipped", worktreeInfo{repo: "r", branch: "a", merged: true, live: true}, false, "skip-live"},
		{"live wins over interactive review", worktreeInfo{repo: "r", branch: "a", merged: true, dirty: true, live: true}, true, "skip-live"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actions := planClean([]worktreeInfo{tc.wt}, tc.interactive)
			if len(actions) != 1 || actions[0].action != tc.want {
				t.Fatalf("actions = %+v, want one %q", actions, tc.want)
			}
		})
	}
	if actions := planClean([]worktreeInfo{{repo: "r", branch: "a", shelved: true}}, false); len(actions) != 0 {
		t.Fatalf("an unmerged worktree is never planned, shelf or not: %+v", actions)
	}
}
