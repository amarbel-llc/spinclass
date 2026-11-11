package spawn

import (
	"os"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
)

// DriverUnattended reports whether the calling session is itself unattended
// (clown's CLOWN_UNATTENDED marker, which clown also puts in claude's own
// environment, so a hook sees it). Anything but unset counts: a misspelt marker
// must not read as attended.
func DriverUnattended() bool {
	return os.Getenv(unattendedEnvVar) != ""
}

// WillBeUnattended reports whether a spawn-session call with this repo
// argument, issued from cwd, would launch an UNCONFINED worker in dontAsk: the
// trusted [session-entry].unattended-permission-mode is "dontAsk" and the
// worker repo's spawn-entry can take the splice (applyUnattendedMode). It is the
// permission layer's read-only mirror of that launch decision, for deciding
// whether the always-ask floor may yield (spinclass#476): a worker that cannot
// answer a prompt is a narrowing, not a widening. A confined worker is
// WillConfine's. Any doubt is false, which keeps the prompt.
func WillBeUnattended(home, cwd, repo string) bool {
	target := repoFor(cwd)
	if repo != "" {
		var err error
		if target, err = ResolveRepo(home, repo); err != nil {
			return false
		}
	}
	if target == "" {
		return false
	}
	h, err := sweatfileio.LoadHierarchy(home, target)
	if err != nil {
		return false
	}
	mode, _ := sweatfileio.TrustedUnattendedPermissionMode(h, home, target)
	if mode != sweatfile.UnattendedModeDontAsk {
		return false
	}
	merged := sweatfile.GetDefault().MergeWith(h.Merged)
	_, err = SpliceDontAskMode(merged.SessionSpawnEntry())
	return err == nil
}
