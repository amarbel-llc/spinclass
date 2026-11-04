//go:build clown_moxy_table

package spawn

import (
	"slices"

	"code.linenisgreat.com/clown/pkg/profilelookup"
)

// moxyTableOf maps profilelookup.Resolved.Moxy (clown#343, clown 252b976 and
// later) onto spinclass's view; nil when the profile has none. This file needs
// a clown at or past 252b976 in flake.lock; until the relock it is behind the
// clown_moxy_table tag and moxytable_unlinked.go stands in (TODO(#457): drop
// the tag and the stand-in after the relock).
func moxyTableOf(res *profilelookup.Resolved) *MoxyTable {
	if res.Moxy == nil {
		return nil
	}
	return &MoxyTable{
		Placement:  res.Moxy.Placement,
		AllowTools: slices.Clone(res.Moxy.AllowTools),
		Tools:      slices.Clone(res.Moxy.Tools),
	}
}
