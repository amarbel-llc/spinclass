//go:build !clown_moxy_table

package spawn

import "code.linenisgreat.com/clown/pkg/profilelookup"

// moxyTableOf is the profile's moxy table as the linked clown reports it. The
// clown pinned in flake.lock (9c80697) predates profilelookup.Resolved.Moxy
// (clown#343, 252b976), so this build sees no table: a confined spawn keeps
// refusing moxy whole (R3) and nothing R3' accepts can be reached. The read of
// the field is moxytable_linked.go, built with the clown_moxy_table tag; once
// the operator relocks clown at or past 252b976, delete this file and the tag
// (TODO(#457)).
func moxyTableOf(*profilelookup.Resolved) *MoxyTable { return nil }
