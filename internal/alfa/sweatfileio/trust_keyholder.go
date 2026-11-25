package sweatfileio

import (
	"fmt"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

// ParseKeyholderMode turns a [keyholder] mode value into the switch: "on" is
// true, "off" and the empty string are false, anything else is an error naming
// the value. It is the one parser the resolver and `sc validate` share.
func ParseKeyholderMode(v string) (bool, error) {
	switch v {
	case "", "off":
		return false, nil
	case "on":
		return true, nil
	}
	return false, fmt.Errorf("[keyholder] mode %q is not valid: use \"off\" or \"on\"", v)
}

// TrustedKeyholderMode returns the [keyholder] mode honoured for the repo: a
// scalar, the last layer above the repo that sets it wins, off when unset. The
// key holder replaces what a session can sign with, so it is the operator's
// switch: layers that may not set it (the repo and worktree files) are never
// honoured, and their paths are returned in untrusted. An invalid value in a
// trusted layer is an error and the answer stays off.
func TrustedKeyholderMode(h sweatfile.Hierarchy, home, repoRoot string) (on bool, untrusted []string, err error) {
	var value string
	for _, src := range h.Sources {
		if src.SkipReason != "" || !src.Found {
			continue
		}
		if src.File.Keyholder == nil || src.File.Keyholder.Mode == nil {
			continue
		}
		if LayerAboveRepo(src.Path, home, repoRoot) {
			value = *src.File.Keyholder.Mode
		} else {
			untrusted = append(untrusted, src.Path)
		}
	}
	on, err = ParseKeyholderMode(value)
	if err != nil {
		return false, untrusted, err
	}
	return on, untrusted, nil
}
