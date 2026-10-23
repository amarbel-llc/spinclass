package main

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// principalPlaceholder in [session-entry].resume is replaced with the
// session's recorded principal, so the harness can resume under the same
// identity, e.g. `clown -- --resume {principal}` (FDR 0035, "The principal
// across sc resume"; FDR 0034 item 17). clown keeps CLOWN_SESSION_ID only when
// relaunched with that id; spinclass never sets CLOWN_SESSION_ID itself.
const principalPlaceholder = "{principal}"

// uuidShaped is the principal shape clown's --resume accepts as a channel
// key: a non-UUID key makes clown mint a separate claude id and drift.
var uuidShaped = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// applyRecordedPrincipal substitutes the recorded principal into the resume
// entry. Without the placeholder, a session that has a principal resumes
// under a fresh one: a warning, or a refusal when profile rights are enforced,
// because its principal-bound grants and the links it granted would stop
// resolving. With the placeholder, a missing or non-UUID principal refuses.
//
// shellFallback (no [session-entry].resume, so the start entry, a shell, runs)
// is left untouched with no warning or refusal: a shell is not a harness to
// hand the principal to, and the operator resumes the harness from it.
func applyRecordedPrincipal(entry []string, principal string, enforced, shellFallback bool) ([]string, string, error) {
	if shellFallback {
		return entry, "", nil
	}
	uses := slices.ContainsFunc(entry, func(a string) bool { return strings.Contains(a, principalPlaceholder) })
	if !uses {
		if principal == "" {
			return entry, "", nil
		}
		msg := fmt.Sprintf("the resume entry has no %s placeholder, so this session resumes under a fresh principal "+
			"instead of %s and its principal-bound profile grants will not resolve (FDR 0035)", principalPlaceholder, principal)
		if enforced {
			return nil, "", errors.New("profile rights are enforced and " + msg + "; add e.g. `--resume " + principalPlaceholder + "` to [session-entry].resume")
		}
		return entry, msg, nil
	}
	if principal == "" {
		return nil, "", fmt.Errorf("the resume entry uses %s but this session has no recorded principal to resume as", principalPlaceholder)
	}
	if !uuidShaped.MatchString(principal) {
		return nil, "", fmt.Errorf("recorded principal %q is not UUID-shaped; clown cannot resume under it", principal)
	}
	out := make([]string, len(entry))
	for i, a := range entry {
		out[i] = strings.ReplaceAll(a, principalPlaceholder, principal)
	}
	return out, "", nil
}
