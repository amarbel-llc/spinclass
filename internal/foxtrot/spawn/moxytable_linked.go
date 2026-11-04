package spawn

import (
	"slices"

	"code.linenisgreat.com/clown/pkg/profilelookup"
)

// moxyTableOf maps profilelookup.Resolved.Moxy (clown#343, clown profile
// protocol 3) onto spinclass's view; nil when the profile has none.
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
