package worktree

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"

	"code.linenisgreat.com/spinclass/internal/git"
)

var adjectives = []string{
	"bold", "brave", "bright", "calm", "clear",
	"cool", "crisp", "deft", "eager", "fair",
	"fast", "firm", "fond", "free", "fresh",
	"glad", "grand", "green", "keen", "kind",
	"light", "live", "loud", "lucid", "merry",
	"mild", "neat", "noble", "plain", "prime",
	"proud", "pure", "quick", "quiet", "rapid",
	"rare", "ready", "rich", "sharp", "sleek",
	"slim", "smart", "smooth", "snug", "solid",
	"stark", "still", "sunny", "swift", "vivid",
}

var nouns = []string{
	"alder", "aspen", "beech", "birch", "cedar",
	"cherry", "chestnut", "cypress", "elder", "elm",
	"fir", "hazel", "hemlock", "hickory", "holly",
	"juniper", "larch", "laurel", "linden", "locust",
	"magnolia", "mahogany", "maple", "mulberry", "myrtle",
	"oak", "olive", "palm", "pecan", "pine",
	"plum", "poplar", "redwood", "rowan", "sequoia",
	"spruce", "sumac", "sycamore", "teak", "walnut",
	"willow", "yew", "acacia", "banyan", "baobab",
	"buckeye", "catalpa", "dogwood", "ebony", "fig",
}

// RandomName generates a random adjective-noun name that collides with neither
// an existing directory in <repoPath>/.worktrees/ nor an existing local or
// remote branch. The branch check matters because `git worktree add <path>`
// (with no -b) silently checks out a pre-existing branch of the same name
// instead of cutting a fresh one — so a name matching a stale, worktree-less
// branch would adopt that branch's history under a supposedly new session
// (#207). A non-git repoPath (or any git error) reports no branch, so the
// candidate is accepted.
func RandomName(repoPath string) string {
	wtDir := filepath.Join(repoPath, WorktreesDir)
	for {
		candidate := fmt.Sprintf(
			"%s-%s",
			adjectives[rand.IntN(len(adjectives))],
			nouns[rand.IntN(len(nouns))],
		)
		if _, err := os.Stat(filepath.Join(wtDir, candidate)); !os.IsNotExist(err) {
			continue
		}
		if git.BranchExists(repoPath, candidate) || git.RemoteBranchExists(repoPath, candidate) {
			continue
		}
		return candidate
	}
}
