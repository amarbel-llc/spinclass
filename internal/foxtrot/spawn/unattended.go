package spawn

import (
	"fmt"
	"maps"

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
