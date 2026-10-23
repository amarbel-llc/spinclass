package merge

import (
	"context"
	"fmt"
	"io"

	"code.linenisgreat.com/spinclass/internal/git"
)

// The operator-facing side of the post-merge section rule: what `sc merge
// --show-post-merge` and `sc merge --accept-post-merge` print. Both run at a
// terminal; the MCP merge tools never reach this file.

// writeSectionBlock prints one labelled section with its digest.
func writeSectionBlock(w io.Writer, label string, s PostMergeSection) {
	fmt.Fprintf(w, "%s\n  digest: %s\n", label, s.Digest())
	fmt.Fprint(w, indentLines(s.Describe(), "  "))
}

func indentLines(s, prefix string) string {
	out := ""
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			if i > start {
				out += prefix + s[start:i] + "\n"
			}
			start = i + 1
		}
	}
	return out
}

// WriteShowPostMerge is the `--show-post-merge` dry run: the post-merge section
// on the landing target's tip against the section at the session branch's HEAD,
// with both digests. It merges nothing.
func WriteShowPostMerge(ctx context.Context, w io.Writer, repoPath, wtPath, branch, defaultBranch string, gitSync bool) error {
	head, err := git.RevParse(wtPath, "HEAD")
	if err != nil {
		return fmt.Errorf("could not resolve %s HEAD: %w", branch, err)
	}
	tip, merged, err := ComparePostMergeSections(ctx, repoPath, wtPath, defaultBranch, head, gitSync)
	if err != nil {
		return err
	}
	writeSectionBlock(w, fmt.Sprintf("post-merge section on %s tip", defaultBranch), tip)
	writeSectionBlock(w, fmt.Sprintf("post-merge section at %s HEAD (%s)", branch, shortSha(head)), merged)
	if tip.Digest() == merged.Digest() {
		fmt.Fprintln(w, "unchanged: this merge needs no --accept-post-merge")
		return nil
	}
	fmt.Fprintf(w, "change: %s\n", merged.ChangesFrom(tip))
	fmt.Fprintf(w, "to land it, run at a terminal: sc merge --accept-post-merge %s %s\n", merged.Digest(), branch)
	return nil
}

// writeAcceptedSection prints, before an accepting merge proceeds, the exact
// section the operator is accepting (the one at the session HEAD) and its
// digest, flagging a digest that does not match it.
func writeAcceptedSection(w io.Writer, wtPath, repoPath, branch, accept string) error {
	head, err := git.RevParse(wtPath, "HEAD")
	if err != nil {
		return fmt.Errorf("could not resolve %s HEAD: %w", branch, err)
	}
	merged, err := PostMergeSectionAt(repoPath, head)
	if err != nil {
		return fmt.Errorf("post-merge section at %s: %w", shortSha(head), err)
	}
	writeSectionBlock(w, fmt.Sprintf("accepting post-merge section at %s HEAD (%s)", branch, shortSha(head)), merged)
	if merged.Digest() != accept {
		fmt.Fprintf(w, "warning: the digest given (%s) does not match this section; the merge will be refused unless the landed section matches it\n", accept)
	}
	return nil
}
