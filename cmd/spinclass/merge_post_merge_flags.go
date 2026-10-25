package main

import (
	"errors"
	"io"
	"os"

	"golang.org/x/term"

	"code.linenisgreat.com/spinclass/internal/delta/merge"
)

// Seams for command tests.
var (
	// postMergeAcceptIsTerminal reports whether a human is at the keyboard:
	// stdin and stdout must both be terminals, so a pipe or an agent's shell
	// cannot confirm a changed post-merge section.
	postMergeAcceptIsTerminal = func() bool {
		return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	}
	postMergeOperatorStdout = func() io.Writer { return os.Stdout }
)

// postMergeOperatorOptions maps `sc merge --accept-post-merge <digest>` and
// `--show-post-merge` onto merge.PostMergeOptions. Accepting a changed
// post-merge section (which runs commands on this machine after landing) is
// operator-only: it needs a terminal, and no MCP tool has a parameter for it.
// --show-post-merge only reads, so it needs none.
func postMergeOperatorOptions(acceptDigest string, show bool) (merge.PostMergeOptions, error) {
	var pm merge.PostMergeOptions
	switch {
	case show && acceptDigest != "":
		return pm, errors.New("--show-post-merge and --accept-post-merge are mutually exclusive: show first, then accept")
	case show:
		pm.ShowPostMerge = postMergeOperatorStdout()
	case acceptDigest != "":
		if !postMergeAcceptIsTerminal() {
			return pm, errors.New("--accept-post-merge needs a terminal: stdin and stdout must both be terminals, because accepting a changed post-merge section runs new commands here; an operator at the keyboard must do it, it cannot be piped or driven by a program")
		}
		pm.AcceptPostMergeDigest = acceptDigest
		pm.AnnounceAccept = postMergeOperatorStdout()
	}
	return pm, nil
}
