// Package embeds holds build-time-pinned absolute /nix/store paths that
// are linked into spinclass via -ldflags by `lib.mkSpinclass` in the
// flake. The values are set once at process startup from cmd/spinclass
// and read by callers that need to invoke the pinned binaries.
//
// Empty strings mean the corresponding integration is dormant: madder
// store init is skipped and direnv falls back to PATH lookup. See
// FDR 0003.
package embeds

var (
	madderBin string
	direnvBin string
)

// Set records the build-time-pinned binary paths. Called once from
// cmd/spinclass; tests may call it to override per-test (with
// t.Cleanup restoring the prior values).
func Set(madder, direnv string) {
	madderBin = madder
	direnvBin = direnv
}

// MadderBin returns the absolute path to the pinned madder binary, or
// "" if no madder was supplied at build time.
func MadderBin() string { return madderBin }

// DirenvBin returns the absolute path to the pinned direnv binary, or
// "" if no direnv was supplied at build time.
func DirenvBin() string { return direnvBin }
