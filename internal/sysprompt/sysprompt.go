// Package sysprompt renders the dynamic system-prompt fragment spinclass
// contributes to a clown-launched session (clown plugin protocol, RFC-0002 §5).
//
// clown's stdio bridge asks `spinclass serve` for the well-known MCP prompt
// PromptName before the downstream agent has sent `initialize`; the rendered
// text is appended last into the agent's system prompt. The fragment is
// computed from live session state at request time — the whole point of the
// dynamic path over the build-time static fragments (FDR-0003) is that it can
// branch on runtime state the static text structurally cannot express.
//
// The single branch that matters: a spinclass-managed WORKTREE session vs the
// repo's MAIN CHECKOUT (an implicit session, FDR-0014). The worktree path sets
// SPINCLASS_* env vars via the executor (internal/executor/session.go); a main
// checkout has no executor, so coordinates come from cwd + git instead.
//
// Both shapes additionally carry a best-effort repository line (provider,
// owner, link, description) resolved by internal/repoinfo from the git remote
// plus a bounded live forge lookup (papi for the forge kind, gh / the
// Gitea-Forgejo API for the description). The lookup is deadline-capped
// (repoFetchTimeout) so it can never stall the pre-initialize prompts/get, and
// any failure just omits the affected lines.
package sysprompt

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"code.linenisgreat.com/spinclass/internal/clown"
	"code.linenisgreat.com/spinclass/internal/git"
	"code.linenisgreat.com/spinclass/internal/repoinfo"
	"code.linenisgreat.com/spinclass/internal/sweatfileio"
)

// repoFetchTimeout bounds the live forge enrichment (papi/gh/HTTP) so the
// prompts/get the clown bridge issues before `initialize` can never be
// stalled by a slow or unreachable forge. On timeout the repository line
// simply degrades to whatever was resolved locally (or is omitted).
const repoFetchTimeout = 2 * time.Second

// PromptName is the well-known MCP prompt the clown stdio bridge issues a
// `prompts/get` for when it asks spinclass for a dynamic system-prompt fragment
// (RFC-0002 §5). It MUST match the bridge's childPromptName, so it is fixed.
const PromptName = "system-prompt-append"

// Mode distinguishes the two session shapes spinclass orients an agent for.
type Mode string

const (
	// ModeWorktree is a spinclass-managed worktree session (the common case).
	ModeWorktree Mode = "worktree"
	// ModeMainCheckout is an agent working in a repo's main checkout — an
	// implicit session (FDR-0014), with no spinclass worktree.
	ModeMainCheckout Mode = "main-checkout"
)

// Coordinates is the runtime session state a fragment is rendered from.
type Coordinates struct {
	Mode        Mode
	SessionKey  string
	Repo        string
	Branch      string
	Worktree    string // worktree path (worktree mode) or checkout path (main checkout)
	Description string
	GroupID     string
	// Timezone is the host's local timezone, e.g. "America/New_York (UTC-04:00)",
	// resolved fully locally (no network) so it is safe before `initialize`.
	// Empty renders as an omitted line.
	Timezone string
	// RepoInfo is the best-effort forge identity of the session's repo —
	// provider, owner, link, description — resolved from the git remote plus
	// a bounded live forge lookup. Empty fields render as omitted lines.
	RepoInfo repoinfo.RepoInfo
	// DesignRecords is the pre-rendered "## Design records" markdown section
	// (FDR 0021), or "" when the repo has no scanned design-record dirs or the
	// index is disabled. Render appends it after the template body.
	DesignRecords string
	// CoActiveSessions is the pre-rendered one-line summary of the OTHER
	// active sessions on the same repo ("2 other live sessions on <repo>: …",
	// spinclass#238), or "" when there are none or the lookup failed. Resolved
	// entirely from local session state (no network) so it is safe before
	// `initialize`; empty renders as an omitted line.
	CoActiveSessions string
	// ProtocolWarning is a one-line ⚠ notice, prepended to the fragment, when
	// running under clown and this binary's linked jobwake ProtocolVersion does
	// not match the host ringmaster's `version --protocol` (#26, RFC-0018) —
	// the agent-visible half of the loud degrade. Empty when not under clown or
	// on a clean match. Reads clown.CheckProtocol's memoized verdict.
	ProtocolWarning string
}

//go:embed templates/*.md.tmpl
var templatesFS embed.FS

var fragmentTmpl = template.Must(template.ParseFS(templatesFS, "templates/*.md.tmpl"))

// Resolve discovers the current session's coordinates from the serve process's
// environment and, for a main checkout, its cwd + git.
func Resolve() Coordinates {
	c := resolve(os.Getenv, os.Getwd, fetchRepoInfo, loadDocIndex, loadCoActiveLine)
	// The ProtocolVersion warning is a process-global fact (from clown's
	// memoized CheckProtocol), independent of the worktree/main-checkout split,
	// so it is set here rather than threaded through resolve's injected
	// loaders — keeping resolve's signature and its unit tests untouched.
	c.ProtocolWarning = protocolWarning(context.Background())
	return c
}

// protocolWarning returns a one-line ⚠ notice when running under clown and the
// linked jobwake ProtocolVersion does not match the host ringmaster's
// `version --protocol` (#26, RFC-0018) — the agent-visible half of the loud
// degrade (serve-start also logs it to stderr/servelog). Empty when not under
// clown or on a clean match. Reads clown.CheckProtocol's memoized verdict, so
// it never re-shells.
func protocolWarning(ctx context.Context) string {
	if !clown.Enabled() {
		return ""
	}
	ok, want, got, err := clown.CheckProtocol(ctx)
	if ok {
		return ""
	}
	if err != nil {
		return fmt.Sprintf(
			"⚠ ringmaster protocol could not be verified (this spinclass linked jobwake "+
				"ProtocolVersion=%d): %v — per-job crash-liveness is disabled; async "+
				"merge/check wakes are unaffected.", want, err,
		)
	}
	return fmt.Sprintf(
		"⚠ ringmaster protocol mismatch: this spinclass linked jobwake ProtocolVersion=%d "+
			"but the host ringmaster reports %d — per-job crash-liveness is disabled until "+
			"they match; async merge/check wakes are unaffected.", want, got,
	)
}

// loadDocIndex is the production design-record index loader: it reads the
// merged sweatfile hierarchy rooted at root for [sysprompt].doc-index-dirs
// (falling back to the built-in default dirs when unset), then scans and
// renders the index. Local file I/O only — safe before `initialize` — and any
// failure yields an empty section. See FDR 0021.
func loadDocIndex(root string) string {
	if root == "" {
		return ""
	}
	dirs := defaultDocIndexDirs
	if home, err := os.UserHomeDir(); err == nil {
		if h, err := sweatfileio.LoadHierarchy(home, root); err == nil {
			if configured, ok := h.Merged.SyspromptDocIndexDirs(); ok {
				dirs = configured
			}
		}
	}
	return renderDesignRecords(root, dirs)
}

// fetchRepoInfo is the production repo-enrichment fetcher: a bounded
// repoinfo.Fetch. An empty path yields a zero RepoInfo.
func fetchRepoInfo(path string) repoinfo.RepoInfo {
	if path == "" {
		return repoinfo.RepoInfo{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), repoFetchTimeout)
	defer cancel()
	return repoinfo.Fetch(ctx, path)
}

// resolve is the testable core of Resolve with the environment, cwd,
// repo-enrichment, design-record, and co-active-session lookups injected.
func resolve(getenv func(string) string, getwd func() (string, error), fetchRepo func(string) repoinfo.RepoInfo, renderDocs func(string) string, coActive func(Mode, string) string) Coordinates {
	c := Coordinates{
		SessionKey:  getenv("SPINCLASS_SESSION_ID"),
		Repo:        getenv("SPINCLASS_REPO"),
		Branch:      getenv("SPINCLASS_BRANCH"),
		Worktree:    getenv("SPINCLASS_WORKTREE"),
		Description: getenv("SPINCLASS_DESCRIPTION"),
		GroupID:     getenv("CLOWN_GROUP_ID"),
		// Host-level and mode-independent: a worktree session and a main
		// checkout share the same host clock, so it survives the main-checkout
		// field reset below.
		Timezone: hostTimezone(),
	}

	cwd, _ := getwd()

	// Worktree mode requires SPINCLASS_WORKTREE set AND the serve process cwd
	// inside it. The cwd guard mirrors merge.isInsideSession: a nested clown
	// launched from within a worktree session inherits SPINCLASS_WORKTREE
	// (the executor strips CLOWN_/CLAUDE_SESSION_ID but not SPINCLASS_*), so
	// the env var alone would mislabel a main checkout as a worktree.
	if c.Worktree != "" && pathWithin(cwd, c.Worktree) {
		c.Mode = ModeWorktree
		c.RepoInfo = fetchRepo(c.Worktree)
		c.DesignRecords = renderDocs(c.Worktree)
		c.CoActiveSessions = coActive(ModeWorktree, c.Worktree)
		return c
	}

	// Main-checkout (implicit) session: no executor set SPINCLASS_*, so derive
	// the coordinates from cwd + git. Best-effort — a non-git cwd or a git
	// failure just leaves the fields empty; Render still produces a fragment.
	//
	// We deliberately do NOT correlate the exact <repo>/<rand> implicit key
	// from .spinclass/state-*.json: multiple concurrent agents in one checkout
	// make that ambiguous. Git-derived coordinates plus CLOWN_SESSION_ID are
	// enough for orientation.
	c.Mode = ModeMainCheckout
	// Drop any worktree-session env (possibly leaked from a nested clown) and
	// re-derive from git, so a main checkout never displays stale coordinates.
	c.Repo, c.Branch, c.Worktree, c.Description = "", "", "", ""
	if cwd != "" {
		if top, err := git.Run(cwd, "rev-parse", "--show-toplevel"); err == nil && top != "" {
			c.Worktree = top
			c.Repo = filepath.Base(top)
		}
		if br, err := git.Run(cwd, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && br != "" {
			c.Branch = br
		}
	}
	if c.SessionKey == "" {
		if id := getenv("CLOWN_SESSION_ID"); id != "" {
			c.SessionKey = id
		} else if c.Repo != "" && c.Branch != "" {
			c.SessionKey = c.Repo + "/" + c.Branch
		}
	}
	// Enrich from the checkout's git toplevel (falling back to cwd when the
	// toplevel couldn't be derived, e.g. a bare or non-standard checkout).
	repoPath := c.Worktree
	if repoPath == "" {
		repoPath = cwd
	}
	c.RepoInfo = fetchRepo(repoPath)
	c.DesignRecords = renderDocs(repoPath)
	c.CoActiveSessions = coActive(ModeMainCheckout, repoPath)
	return c
}

// Render executes the embedded template for c.Mode and returns the fragment
// text (trailing newline trimmed). It is best-effort: a template execution
// error yields an empty fragment, which clown treats as "no fragment" rather
// than a failure.
func Render(c Coordinates) (string, error) {
	name := "main_checkout.md.tmpl"
	if c.Mode == ModeWorktree {
		name = "worktree.md.tmpl"
	}
	var b strings.Builder
	if err := fragmentTmpl.ExecuteTemplate(&b, name, c); err != nil {
		return "", err
	}
	frag := strings.TrimRight(b.String(), "\n")
	// The design-record index (FDR 0021) is composed in Go rather than in the
	// templates: it is a Go-rendered markdown trailer, and appending it here
	// keeps the templates free of the grouping/whitespace logic.
	if c.DesignRecords != "" {
		frag += "\n\n" + c.DesignRecords
	}
	// The ProtocolVersion warning (#26) is prepended so the degrade is the
	// first thing the agent reads in the fragment, not buried after the
	// orientation body. Empty on a clean match (the common case).
	if c.ProtocolWarning != "" {
		frag = c.ProtocolWarning + "\n\n" + frag
	}
	return frag, nil
}

// pathWithin reports whether cwd is base or a descendant of base. A sibling that
// merely shares a string prefix (e.g. base/.worktrees/x vs .worktrees/x-other)
// is not within.
func pathWithin(cwd, base string) bool {
	if cwd == "" || base == "" {
		return false
	}
	rel, err := filepath.Rel(base, cwd)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}
