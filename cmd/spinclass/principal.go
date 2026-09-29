package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"sync"

	"code.linenisgreat.com/spinclass/internal/spawn"
)

var (
	fallbackPrincipalOnce sync.Once
	fallbackPrincipal     string
)

// currentPrincipal returns this serve process's principal (FDR 0032 D1): the
// per-instance identity that spawn writes as spawned_by_principal, that a
// spawned worker's SessionStart hook sends its hello to, and that
// close-child-session checks against a child's holders. It reads
// CLOWN_SESSION_ID when clown hosts this process — clown mints that key
// today; juggler mints it in the target architecture, with no format change
// here. Absent clown, a per-process random id is generated once
// (crypto/rand, UUID v4 shape) and cached for the process lifetime, so
// nothing downstream can tell a clown-minted principal from the bare-serve
// fallback apart — that is deliberate, not an oversight. Never touches git or
// the cwd: the fix for spinclass#332 IS that this function has no cwd
// dependency, unlike currentSessionKey.
func currentPrincipal() string {
	if v := os.Getenv("CLOWN_SESSION_ID"); v != "" {
		return v
	}
	fallbackPrincipalOnce.Do(func() {
		fallbackPrincipal = randomUUIDv4()
	})
	return fallbackPrincipal
}

// randomUUIDv4 generates a UUID v4 (8-4-4-4-12 hex, version nibble 4, variant
// nibble in 8-b) via crypto/rand — the same shape clown's per-instance key
// carries, so currentPrincipal's fallback is indistinguishable from it.
func randomUUIDv4() string {
	var b [16]byte
	// rand.Read only errors when the OS entropy source is unavailable, which
	// is unrecoverable for a long-lived serve process anyway; a
	// partially-filled buffer still yields a validly-shaped (if less random)
	// id rather than panicking the process over a principal.
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10xx
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// bestEffortSessionKey returns currentSessionKey()'s result, or "" on error.
// A driver spawning from outside any git repository (a ~/eng coordinator,
// spinclass#332) has no session key to record — that is fine now, since the
// spawned child's authority link is the driver's principal
// (SpawnedByPrincipal), not this display-only key.
func bestEffortSessionKey() string {
	key, err := currentSessionKey()
	if err != nil {
		return ""
	}
	return key
}

// currentDriver resolves the current session as a spawn.Driver (FDR 0032
// D1). Principal always resolves — to CLOWN_SESSION_ID, else the per-process
// fallback — so it is the authority link and the hello target. SessionKey is
// best-effort display information only (`sc list`'s spawned-by column), so a
// driver outside any worktree (spinclass#332) still spawns successfully with
// an empty SessionKey.
func currentDriver() spawn.Driver {
	return spawn.Driver{
		Principal:  currentPrincipal(),
		SessionKey: bestEffortSessionKey(),
	}
}
