package spawn

import (
	"fmt"
	"maps"
	"slices"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

// unattendedEnvVar is the marker clown reads from the frontend environment at
// launch and on `clown resume`: set to exactly "1", it means "apply dontAsk".
// It is set only where spinclass splices dontAsk, so argv and clown's settings
// never disagree.
const unattendedEnvVar = "CLOWN_UNATTENDED"

// validateUnattendedMode refuses a [session-entry] unattended-permission-mode
// value a spawn cannot honour; "" (unset) is fine.
func validateUnattendedMode(mode string) error {
	if mode == "" || sweatfile.ValidUnattendedPermissionMode(mode) {
		return nil
	}
	return fmt.Errorf("spawn refused: [session-entry] unattended-permission-mode is %q, want %q or %q",
		mode, sweatfile.UnattendedModeDontAsk, sweatfile.UnattendedModeAuto)
}

// applyUnattendedMode decides the worker's permission mode in argv and whether
// it carries the CLOWN_UNATTENDED marker. A confined worker is always dontAsk
// (no operator at its tty), whatever the key says; an unconfined one is dontAsk
// only when the trusted key says "dontAsk". "auto" and unset leave argv as
// renderSpawn built it (auto unless disable-auto-mode) and carry no marker.
// Fails closed: a spawn-entry that cannot take dontAsk is refused.
func applyUnattendedMode(argv []string, confined bool, mode string) ([]string, bool, error) {
	if !confined && mode != sweatfile.UnattendedModeDontAsk {
		return argv, false, nil
	}
	out, err := SpliceDontAskMode(argv)
	if err != nil {
		if confined {
			return nil, false, err
		}
		return nil, false, fmt.Errorf("spawn refused: [session-entry] unattended-permission-mode = %q cannot be applied: %w", mode, err)
	}
	return out, true, nil
}

// SetUnattendedMarker is setUnattendedMarker for a restart that changes a
// session's posture (spinclass#342): the pinned entry's env gets the marker for
// unattended and loses it for attended, so the next `sc resume` agrees with the
// relaunch.
func SetUnattendedMarker(env map[string]string, on bool) map[string]string {
	return setUnattendedMarker(env, on)
}

// HasUnattendedMarker reports whether env carries the marker exactly as
// setUnattendedMarker writes it (spinclass#473 reads a pinned entry's posture).
func HasUnattendedMarker(env map[string]string) bool {
	return env[unattendedEnvVar] == "1"
}

// PinnedEntryExcludesDontAsk reports why a session whose pinned entry is argv
// cannot run in claude's dontAsk mode, or nil. An entry that names a harness
// (carries the "--" provider-args separator) must name claude. An entry with no
// separator is a shell, as `sc start` and `sc resume` pin: the harness it runs
// is not knowable from the entry, so it is not excluded here; the frontend
// (clown) gates the posture against the harness it actually runs, at request
// time and again before the stop, and a restart requires a clown-bound
// principal (spinclass#476). Nothing is spliced into a pinned entry.
func PinnedEntryExcludesDontAsk(argv []string) error {
	sep := slices.Index(argv, "--")
	if sep == -1 {
		return nil
	}
	provider, err := effectiveProvider(argv[:sep])
	if err != nil {
		return fmt.Errorf("the provider of its pinned entry cannot be determined: %w", err)
	}
	if provider != "claude" {
		return fmt.Errorf("its pinned entry's provider is %q, not claude", provider)
	}
	return nil
}

// setUnattendedMarker returns a copy of env with CLOWN_UNATTENDED set to "1"
// when on and absent otherwise: whatever a sweatfile's [session-entry].env said
// is dropped, so only an applied dontAsk ever carries the marker. (The launcher's
// inherited environ is stripped in workerEnvScoped.)
func setUnattendedMarker(env map[string]string, on bool) map[string]string {
	out := maps.Clone(env)
	delete(out, unattendedEnvVar)
	if on {
		if out == nil {
			out = make(map[string]string, 1)
		}
		out[unattendedEnvVar] = "1"
	}
	return out
}
