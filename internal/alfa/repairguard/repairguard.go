// Package repairguard keeps [hooks].repair from rewriting generated files: after
// repair runs, a file whose pre-repair contents carry the generated-code marker
// is put back as the generator wrote it, or the repair is refused
// ([hooks].repair-generated). Both callers of repair use it: the regenerate
// phase and the pre-merge REPAIR phase (FDR 0018).
package repairguard

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"code.linenisgreat.com/spinclass/internal/0/generated"
	"code.linenisgreat.com/spinclass/internal/0/git"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

// KeptLabel is the test point a caller emits when Enforce put files back.
const KeptLabel = "repair kept generated files"

const silenceHint = "set skip-generated in the repair's conformist config"

// RefusalError is Enforce's refusal under the fail mode: repair changed the
// generated files in Paths.
type RefusalError struct {
	Paths []string
}

func (e *RefusalError) Error() string {
	return fmt.Sprintf("repair reformatted generated file(s) %s, and [hooks].repair-generated is %q (%s so repair leaves them alone)",
		strings.Join(e.Paths, ", "), sweatfile.RepairGeneratedFail, silenceHint)
}

// KeptMessage describes what Enforce put back, for the caller's test point.
func KeptMessage(paths []string) string {
	return fmt.Sprintf("repair reformatted generated file(s) %s; kept the generator's bytes (%s to silence this)",
		strings.Join(paths, ", "), silenceHint)
}

// Effective is the mode the guard runs with: configured, except that without an
// active [hooks].pre-commit the guard stands aside (allow), because repair is
// then the only hook that can regenerate anything and may be the generator.
func Effective(sf sweatfile.Sweatfile, configured sweatfile.RepairGeneratedMode) sweatfile.RepairGeneratedMode {
	if !sf.PreCommitActive() {
		return sweatfile.RepairGeneratedAllow
	}
	return configured
}

// Enforce applies mode to what repair changed in wtPath (the worktree root)
// since snapshot, the clean HEAD repair started from. A changed path counts as
// generated when its contents at snapshot carry Go's generated-code marker;
// changes in HEAD (a repair amend), the index and the working tree all count.
//
// Under protect those paths are put back to their bytes at snapshot in the
// index and the working tree; when repair moved HEAD, the restore is amended
// into HEAD, and HEAD is reset to snapshot when that leaves its tree equal to
// snapshot's. kept names the restored paths. Under fail the tree is left as
// repair left it and err is a *RefusalError naming them, for the caller to
// undo. Under allow nothing is checked. Any other error is a git failure.
func Enforce(wtPath, snapshot string, mode sweatfile.RepairGeneratedMode) (kept []string, err error) {
	if mode == sweatfile.RepairGeneratedAllow {
		return nil, nil
	}
	paths, err := generatedPathsChangedSince(wtPath, snapshot)
	if err != nil || len(paths) == 0 {
		return nil, err
	}
	if mode == sweatfile.RepairGeneratedFail {
		return nil, &RefusalError{Paths: paths}
	}
	if err := restore(wtPath, snapshot, paths); err != nil {
		return nil, err
	}
	return paths, nil
}

func generatedPathsChangedSince(wtPath, snapshot string) ([]string, error) {
	changed := map[string]bool{}
	for _, args := range [][]string{
		{"diff", "--name-only", "-z", "--no-renames", snapshot, "HEAD"},
		{"diff", "--name-only", "-z", "--no-renames", "--cached", snapshot},
		{"diff", "--name-only", "-z", "--no-renames", snapshot},
	} {
		out, err := git.RunStdin(wtPath, "", args...)
		if err != nil {
			return nil, fmt.Errorf("could not list what repair changed since %s: %w", snapshot, err)
		}
		for _, p := range bytes.Split(out, []byte{0}) {
			if len(p) > 0 {
				changed[string(p)] = true
			}
		}
	}
	var paths []string
	for p := range changed {
		data, found, err := git.FileAtRev(wtPath, snapshot, p)
		if err != nil {
			return nil, fmt.Errorf("could not read %s at %s: %w", p, snapshot, err)
		}
		if found && generated.HasGoMarker(data) {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	return paths, nil
}

func restore(wtPath, snapshot string, paths []string) error {
	specs := make([]string, len(paths))
	for i, p := range paths {
		specs[i] = ":(literal)" + p
	}
	if _, err := git.Run(wtPath, append([]string{"checkout", snapshot, "--"}, specs...)...); err != nil {
		return fmt.Errorf("could not put the generator's bytes back: %w", err)
	}
	head, err := git.RevParse(wtPath, "HEAD")
	if err != nil {
		return err
	}
	if head == snapshot {
		return nil
	}
	amend := append([]string{"commit", "-q", "--amend", "--no-edit", "--no-verify", "--allow-empty", "--only", "--"}, specs...)
	if _, err := git.Run(wtPath, amend...); err != nil {
		return fmt.Errorf("could not fold the generator's bytes into repair's amend: %w", err)
	}
	headTree, err := git.RevParse(wtPath, "HEAD^{tree}")
	if err != nil {
		return err
	}
	snapTree, err := git.RevParse(wtPath, snapshot+"^{tree}")
	if err != nil {
		return err
	}
	if headTree != snapTree {
		return nil
	}
	if _, err := git.Run(wtPath, "reset", "-q", "--soft", snapshot); err != nil {
		return fmt.Errorf("could not undo an amend that changed only generated files: %w", err)
	}
	return nil
}
