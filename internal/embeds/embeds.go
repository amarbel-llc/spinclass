// Package embeds holds build-time-pinned absolute /nix/store paths that
// are linked into spinclass via -ldflags by `lib.mkSpinclass` in the
// flake. The values are set once at process startup from cmd/spinclass
// and read by callers that need to invoke the pinned binaries.
//
// Empty strings mean the corresponding integration is dormant: madder
// store init and dodder repo init are skipped and direnv falls back to
// PATH lookup. See FDR 0003 (madder, direnv) and FDR 0008 (dodder).
package embeds

var (
	madderBin string
	direnvBin string
	dodderBin string
	version   string
	commit    string
)

// Set records the build-time-pinned binary paths. Called once from
// cmd/spinclass; tests may call it to override per-test (with
// t.Cleanup restoring the prior values).
func Set(madder, direnv, dodder string) {
	madderBin = madder
	direnvBin = direnv
	dodderBin = dodder
}

// SetVersion records the build-time spinclass version and commit (the
// main.version/main.commit ldflags values). Kept separate from Set so the
// pinned-binary callers stay untouched. Callers that need the values for the
// setup fingerprint (internal/setupfingerprint) read them via Version/Commit;
// tests may call SetVersion to override per-test.
func SetVersion(v, c string) {
	version = v
	commit = c
}

// Version returns the build-time spinclass version ("" if unset).
func Version() string { return version }

// Commit returns the build-time spinclass commit ("" if unset).
func Commit() string { return commit }

// MadderBin returns the absolute path to the pinned madder binary, or
// "" if no madder was supplied at build time.
func MadderBin() string { return madderBin }

// DirenvBin returns the absolute path to the pinned direnv binary, or
// "" if no direnv was supplied at build time.
func DirenvBin() string { return direnvBin }

// DodderBin returns the absolute path to the pinned dodder binary, or
// "" if no dodder was supplied at build time.
func DodderBin() string { return dodderBin }
