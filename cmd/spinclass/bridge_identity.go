package main

import (
	"context"
	"errors"

	"code.linenisgreat.com/spinclass/internal/alfa/session"
	"code.linenisgreat.com/spinclass/internal/bravo/confinement"
)

// The identity of a bridged call. For a call that arrived on the bridge, the
// session acted on is the listener's CallerOrigin.SessionKey and nothing else:
// not the environment's principal (CLOWN_SESSION_ID), not the principals/
// mapping file (a file a session can write), and not any argument. The record
// is read through the frontend-held lookup (session.FindByTarget), never the
// index; the principal it acts as is the one that record carries.
//
// A bridged call whose origin resolves to no live record, or to a record with
// no principal, is refused (never falls back to the environment's principal).
const (
	bridgeNoSessionRefusal   = bridgeRefusalPrefix + "this bridge's session has no live frontend record, so there is no session to act on"
	bridgeNoPrincipalRefusal = bridgeRefusalPrefix + "this bridge's session record carries no principal yet (the SessionStart stamp is not there), so there is no session to act as"
)

// bridgeOriginSession resolves the session a bridged call acts on. bridged is
// false for any other call (stdio, or no origin), which keeps its own path; for
// a bridged call st is the frontend record the origin's key names, with its
// principal known, and err is a "refused: " text when it does not resolve.
func bridgeOriginSession(ctx context.Context) (st *session.State, bridged bool, err error) {
	o, ok := confinement.OriginFrom(ctx)
	if !ok || !o.Bridged() {
		return nil, false, nil
	}
	if o.SessionKey == "" {
		return nil, true, errors.New(bridgeNoSessionRefusal)
	}
	st, ferr := session.FindByTarget(o.SessionKey)
	// FindByTarget also matches a worktree basename; the origin's key is a
	// session key, so a record whose own key is not it is not the session.
	if ferr != nil || st == nil || st.IsTombstone() || st.Key() != o.SessionKey {
		return nil, true, errors.New(bridgeNoSessionRefusal)
	}
	if st.Principal == "" {
		return nil, true, errors.New(bridgeNoPrincipalRefusal)
	}
	return st, true, nil
}

// callerSessionFor is callerSession for a call with ctx: the origin's session
// when the call arrived on the bridge, the principal-mapping path otherwise.
func callerSessionFor(ctx context.Context, verb string) (principal string, st *session.State, err error) {
	if st, bridged, err := bridgeOriginSession(ctx); bridged {
		if err != nil {
			return "", nil, err
		}
		if err := guardHandleTarget(st, verb); err != nil {
			return "", nil, err
		}
		return st.Principal, st, nil
	}
	return callerSession()
}
