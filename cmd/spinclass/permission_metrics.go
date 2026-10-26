package main

import (
	"code.linenisgreat.com/spinclass/internal/0/statsd"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/charlie/perms"
)

// Permission prompt/allow/deny counters (spinclass#369). perms and hooks cannot
// load the sweatfile hierarchy, so the endpoint is resolved here, per event
// (a hook is a short-lived process), and installed as the perms seam.
func init() {
	perms.CountMetric = func(cwd, name string) {
		sf, ok := mergedSweatfileForDir(cwd)
		permissionStatsdClient(sf, ok).Count(name)
	}
}

// permissionStatsdClient resolves the statsd endpoint for a merged sweatfile. If
// the sweatfile could not be loaded, only the environment's opt-in applies.
func permissionStatsdClient(sf sweatfile.Sweatfile, loaded bool) statsd.Client {
	if !loaded {
		return statsd.Resolve("", false)
	}
	return statsd.Resolve(sf.MetricsStatsdAddress(), sf.MetricsStatsdDisabled())
}
