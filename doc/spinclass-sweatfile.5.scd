spinclass-sweatfile(5) "spinclass" "Spinclass Reference"

# NAME

spinclass-sweatfile - sweatfile configuration format

# DESCRIPTION

Spinclass sessions are configured via TOML files called _sweatfiles_.
These control git excludes, Claude Code permissions, direnv setup,
session entrypoints, lifecycle hooks, the dynamic system-prompt fragment,
MCP server registration, custom start-\* plugins, and remote session hosts.

Sweatfiles are loaded as a hierarchy that merges _global_
(_~/.config/spinclass/sweatfile_) → intermediate parent directories →
repository-level (_<repo>/sweatfile_) → worktree-level
(_<worktree>/sweatfile_). Later definitions override earlier ones according
to per-field merge semantics described below.

Intermediate parent directories are walked along *two* chains: the lexical
ancestors of the working directory, and the ancestors of its
symlink-resolved (_realpath_) form. Both chains are inspected in root-first
order, bounded above by *$HOME* (exclusive — loaded as the global file) when
the chain runs through home, or by the filesystem root (*/*, exclusive)
otherwise. Candidate directories are deduplicated by canonical
(symlink-resolved) path: if a directory in one chain resolves to the same
canonical path as one already loaded, the second entry is recorded with a
skip reason and not re-merged.

A given physical worktree therefore produces the same merged configuration
whether entered through a canonical path or through a symlink, provided
sweatfiles exist along both chains. When a filesystem-only-reachable
directory has a sweatfile that no path-only chain can reach (e.g. entering
through the canonical path skips a sweatfile that would only be visible via
a symlinked alias), the *sc validate* output makes this asymmetry explicit
by listing every directory it considered alongside the load/skip status.

# TABLES

## [claude]

Controls Claude Code session behaviour.

*allow*
	Array of Claude Code permission rules (e.g. *Read*, *Edit*,
	*Bash(git \*)*, *mcp\_\_server\_\_tool*). *Merge:* nil = inherit, empty
	array = clear, non-empty = append.

## [git]

*excludes*
	Array of patterns appended to _.git/info/exclude_. *Merge:* nil =
	inherit, empty = clear, non-empty = append.

## [direnv]

*envrc*
	Array of direnv directive lines written to _.envrc_. When no sweatfile
	in the cascade sets *envrc*, it defaults to *source\_up* (plus *use
	flake* if a _flake.nix_ exists). Note that as soon as *any* sweatfile
	sets *envrc*, those defaults are dropped entirely — the generated
	_.envrc_ contains only the directives you supply. Include *source\_up*
	explicitly if you still want it (e.g. *envrc = ["source\_up", "use
	flake . --impure"]*). *Merge:* nil = inherit, empty = clear, non-empty =
	append (lines accumulate across the cascade, like the other array
	fields).

*dotenv*
	Table of key-value pairs written to _.spinclass/env_ at session create.
	Values support *$VAR* expansion (from the create-time environment); the
	special variable *$WORKTREE* expands to the worktree path.

Declare it as a sub-table:

```
[direnv.dotenv]
ETSYWEB_PWD = "$WORKTREE"
```

When *dotenv* is non-empty, spinclass appends a *dotenv .spinclass/env*
directive to the generated _.envrc_ so direnv actually loads the file into
the shell — you do *not* add that line yourself. This append happens
whether _.envrc_ came from the *envrc* default or from an explicit *envrc*
array; the *dotenv* line is added after your directives.

This automatic _.envrc_ rewrite applies only to spinclass-managed
worktrees, whose _.envrc_ spinclass owns. An *implicit* session — an agent
running in a repo's own main checkout rather than an *sc*-created worktree
— also gets _.spinclass/env_ generated from the same merged *dotenv* map
(regenerated on every *SessionStart* so it tracks the current config), but
its _.envrc_ is committed and repo-owned, so spinclass never rewrites it.
To load the values there, the committed _.envrc_ must opt in itself with

```
dotenv_if_exists .spinclass/env
```

(the *\_if\_exists* form so the checkout still loads cleanly when no
implicit session has generated the file). Without that line, a
main-checkout session silently does not receive *dotenv* values.

The inline-table spelling (*dotenv = { FOO = "bar" }*) is equivalent and
equally accepted; use whichever reads better. *Merge:* map merge (child
keys override parent keys).

## [sysprompt]

Configures the dynamic system-prompt fragment spinclass contributes to a
clown-launched session (see FDR 0021). The fragment carries a *Design
records* section: an index of the repository's design records —
_docs/features_, _docs/adrs_, and _docs/rfcs_ by default — listed by
number, title, and lifecycle *status* (read from each file's YAML
frontmatter) and grouped by status.

*doc-index-dirs*
	Array of directories (relative to the worktree/checkout root) scanned
	for _NNNN-\*.md_ design records. Records are genre-tagged by directory
	(*docs/features* → *FDR*, *docs/adrs* → *ADR*, *docs/rfcs* → *RFC*; any
	other directory uses its basename). Scan-if-exists: an absent directory
	contributes nothing, so a repository with none of the scanned
	directories renders no *Design records* section at all. A structurally
	broken record (unreadable, or a frontmatter block opened with *---* but
	never closed) is reported in a trailing diagnostic block instead of
	being silently dropped — the fragment renders best-effort and never
	fails on a malformed doc. *Merge:* override, not append — unlike the
	other array fields. A non-empty value replaces the inherited list
	(override-wins down the cascade); an explicit empty array
	(*doc-index-dirs = []*) disables the index; nil (unset) inherits,
	falling back to the built-in default directories when unset everywhere.

Two further sections are available but, unlike the design-record index,
have *no* built-in default: they render nothing until a sweatfile selects
sources, because the useful set is host- and profile-specific rather than a
repository convention (see FDR 0030). Both take an array of _source specs_,
resolved identically: a leading *~* and any *$*_VAR_ are expanded, then
each result is split on *:*, so a bare *$MANPATH* expands to its individual
roots. A spec containing *\**, *?*, or *[* is matched as a glob; anything
else is taken literally. Duplicates are dropped, so a path reachable from
two specs is listed once, and a spec matching nothing contributes nothing.

Both indexes are bounded so a bulk selector cannot flood the prompt: each
renders at most *index-limit* rows (400 by default), and the scan yields
after 1.5 seconds. In either case the section reports how many entries it
skipped rather than truncating silently. Rows are ordered by the name they
render under, so a corpus past the cap loses a spread rather than whole
manpage sections.

*index-limit*
	Maximum rows EACH of the two indexes renders. The cap exists to bound a
	_bulk_ selector — a bare *$MANPATH* is well over a thousand pages — not
	to trim a deliberately assembled source, so the default is set to clear
	a curated first-party manpath rather than to be tight. A value of *0*
	(or negative) removes the cap entirely: the index then costs whatever
	its sources hold, in every session's system prompt. *Merge:* scalar
	override — nil inherits, a set value replaces.

*man-index*
	Array of source specs selecting the manpages listed in the *Manpage
	index* section. A spec naming a directory is treated as a _manpath
	root_ and scanned one level into its _man\*_ section directories; a glob
	names page files directly. Each page is listed as _name_(_section_) —
	taken from the filename, which is what *man*(1) accepts, and which can
	differ from the name the page documents — plus the short description
	scraped from its NAME section. Both roff dialects are understood:
	*man*(7) (*.SH NAME*, with the description after a *\\-* or plain *-*
	separator) and *mdoc*(7) (*.Nd*). Gzipped and plain pages are read;
	another compression is reported in the diagnostic block rather than
	silently skipped. A page with no NAME section is still listed, without
	a description.

	Prefer a purpose-built manpath containing only the pages you want over
	a broad glob or the whole *$MANPATH*: a developer profile holds well
	over a thousand pages, and the entry cap is a guardrail, not an
	intended operating point. *Merge:* override, not append — same
	semantics as *doc-index-dirs*.

*repo-index*
	Array of source specs selecting the checkouts listed in the *Repository
	index* section, so a session can tell which repository owns a question
	before researching it in the wrong one. A spec that is itself a git
	checkout is indexed directly; any other directory has its immediate
	children scanned for checkouts, which is what makes a directory of
	repositories a valid selector. Each is listed by directory name plus a
	short description taken from the *description* attribute of its
	_flake.nix_ and, failing that, the first prose line of its README. The
	forge's own repository description is deliberately *not* consulted: it
	costs a network round-trip per repository, which a
	directory-of-checkouts selector cannot fit inside the pre-initialize
	deadline. A checkout with neither source is listed without a
	description. *Merge:* override, not append.

## [hooks]

Lifecycle hooks executed as shell scripts.

The *create*, *pre-merge*, *repair*, *on-attach*, and *on-detach* hooks are
invoked via *sh -c* with the working directory set to the worktree path.
The environment variable *WORKTREE* is set to the absolute worktree path.
The *stop* hook follows a different invocation path (run by Claude Code's
*Stop* event handler with cwd set to the session's working directory) and
does not export *WORKTREE*.

*create*
	Script run after worktree creation (before session attach). *Merge:*
	scalar override.

	Spinclass writes and *direnv allow*s the generated _.envrc_ before
	running this hook, then re-runs a bare *direnv allow* after it
	succeeds. A create hook that mutates _.envrc_ (for example, appending a
	directive) therefore does *not* need to re-authorize the file itself —
	the final content is re-allowed automatically.

*stop*
	Script run when the Claude Code session ends (via Stop hook). *Merge:*
	scalar override.

*pre-merge*
	Shell script run by *sc merge* after the worktree branch has been
	rebased onto the default branch but before the fast-forward merge into
	it. If the script exits non-zero, the merge is aborted and the worktree
	is left in its rebased state so the user can fix the issue and retry.
	Typical use: run tests or linters as a pre-merge gate.

	By default the hook runs in a transient, detached *build worktree*
	checked out at the exact (post-rebase) commit being merged — a hidden
	*.merge-\** sibling under *.worktrees/*. This frees the session
	worktree for concurrent edits the moment the synchronous prefix
	(rebase, plus any *repair* phase) lands — especially with the async
	merge tool, whose prefix runs before the job id is returned, so the
	worktree is yours again the moment you hold the job id. It also makes
	the gate verify the committed tree rather than any uncommitted
	working-tree state. A merge therefore never locks your worktree: edits
	made while the hook runs are left for the next merge, since the gate
	builds and fast-forwards the sha it pinned when it started. The build
	worktree is removed when the hook finishes. Because the branch stays
	checked out in the session worktree, the build worktree is in *detached
	HEAD* state: a hook that reads the current branch name (e.g. *git
	rev-parse --abbrev-ref HEAD*) sees *HEAD* rather than the branch. Set
	*disable-merge-build-worktree* (below) to run the hook in place in the
	session worktree instead. When this hook is set, the spinclass
	PreToolUse hook auto-approves the
	*mcp\_\_plugin\_spinclass\_spinclass\_\_merge-this-session* and
	*mcp\_\_plugin\_spinclass\_spinclass\_\_check-this-session* tools
	(whichever is registered — see *disable-merge* below) so Claude Code
	does not prompt: the pre-merge hook acts as the gate instead. *Merge:*
	scalar override.

*post-merge*
	Shell script run by *sc merge* *after* the merge has landed on the
	default branch — the mirror image of *pre-merge*. Typical use: trigger
	a deploy, notify a service, or record the landing somewhere, keyed on
	the commit that actually shipped. This single-string form is the
	degenerate case of the named *[[post-merge]]* targets (see below): when
	any named target is active, this string is SUPERSEDED and does not run.

	It runs only on a *fully successful* merge: the fast-forward completed
	and, when git sync is on, the push to the remote succeeded. Any earlier
	failure (gate, landing rebase, teardown, push) returns before it. The
	hook is *non-fatal:* by the time it runs the merge cannot be undone, so
	a non-zero exit does *not* fail the merge — it emits a *severity=warn*
	test point carrying the hook's output, and the merge still reports
	success. Re-running the merge would find nothing to merge, so a failed
	post-merge hook is something to act on out of band, not to retry.

	Crucially, post-merge runs *under the per-repo merge lock* (see
	*disable-merge-queue* below and *FDR 0022* in the source tree), as the
	merge's last stage. A merge is exclusive end to end: while your hook
	runs, no sibling session in the repo can land or deploy. That ordering
	is the point — two deploys interleaving is the failure this hook exists
	to prevent — but it means a slow hook delays every other merge in the
	repo, which will sit logging *merge queue: waiting behind <session>*
	until it finishes. Keep the hook a cheap trigger rather than a long
	inline deploy.

	The simplest way to do that is to detach the slow work and return.
	spinclass sets no process group on hook subprocesses and does not reap
	the process tree, so a detached child reliably outlives the hook:

	```
	post-merge = """
	[ "$SPINCLASS_MERGE_PUSHED" = 1 ] || exit 0
	setsid ./bin/deploy.sh "$SPINCLASS_MERGED_SHA" \\
	  </dev/null >>deploy.log 2>&1 &
	"""
	```

	The *redirects are load-bearing, not cosmetic.* spinclass captures the
	hook's stdout/stderr through a pipe and waits for every holder of its
	write end to close. A detached child that inherits that pipe keeps it
	open, so the hook — and the merge lock — blocks for the child's full
	duration anyway. Point the child's stdout and stderr somewhere else and
	the hook returns immediately.

	*setsid* additionally puts the child in its own session, so it survives
	a process-group signal; plain *&* is enough today but is not
	future-proof against *spinclass#188* (reaping the hook's process tree
	on interrupt). Note that a detached deploy gives up the ordering
	guarantee above — two deploys can then overlap — so detach only work
	that is itself safe to run concurrently, or that serializes on its own
	lock at the far end.

	The hook runs in the session worktree when it still exists (teardown
	may have removed it) and otherwise in the main checkout — which is
	sitting on the merged tip either way. There is no detached build
	worktree: the merge has already landed, so there is nothing to isolate.
	Unlike *pre-merge*, *inactivity-timeout* does not apply — that knob
	bounds the pre-merge gate's silence. The post-merge hook has its own
	wall-clock cap, *post-merge-timeout* (below), which unlike the
	pre-merge watchdog is *on by default* (10 minutes), because a wedged
	post-merge hook holds the whole repo's merge queue rather than just its
	own session. Beyond the usual environment (including *WORKTREE*), the
	hook receives:

	- *SPINCLASS\_MERGED\_SHA* — the commit that landed. On a queued
	  landing that had to be rebased past a moved default branch this is
	  the *landing* sha, not the originally pinned one.
	- *SPINCLASS\_MERGED\_BRANCH* — the branch that was merged.
	- *SPINCLASS\_DEFAULT\_BRANCH* — the branch it landed on.
	- *SPINCLASS\_MERGE\_PUSHED* — *1* when the landing was pushed to the
	  remote, *0* when it is local-only (e.g. *sc run --local-only*). A
	  deploy trigger should gate on this.
	- *SPINCLASS\_REPO\_PATH* — the main checkout.
	- *SPINCLASS\_PINNED\_SHA* — the commit pinned before landing. Equal
	  to *SPINCLASS\_MERGED\_SHA* unless the queued landing was rebased
	  past a moved default branch, so a script can detect a rebased landing
	  without consulting git.
	- *SPINCLASS\_POST\_MERGE\_TIMEOUT* — the *effective* wall-clock cap on
	  the post-merge phase as a Go duration (e.g. *20m0s*), or *0* when the
	  cap is disabled. This is the value actually enforced: the per-merge
	  override (the *post\_merge\_timeout* parameter of
	  *merge-this-session*(1) or *sc merge --post-merge-timeout*) when one
	  was given, else *post-merge-timeout* below, else the 10m default.
	- *SPINCLASS\_POST\_MERGE\_TIMEOUT\_SECONDS* — the same cap as an
	  integer number of seconds (*0* when disabled), for shell arithmetic.
	- *SPINCLASS\_POST\_MERGE\_DEADLINE* — the absolute instant the cap
	  fires, as unix epoch seconds (*0* when disabled). A verify poll that
	  starts after a slow *command* has already spent part of the budget
	  should size itself from this (_deadline - now_), not from the
	  timeout.
	- *SPINCLASS\_POST\_MERGE\_TARGET* — the name of the *[[post-merge]]*
	  target being run, so one script can serve several targets. Not set on
	  this legacy single-string path.

	Applies to worktree sessions and to implicit (main-checkout) sessions,
	whose push is unconditional — there *SPINCLASS\_MERGED\_BRANCH* and
	*SPINCLASS\_DEFAULT\_BRANCH* are the same ref. *sc check* and
	*check-this-session* never run it: nothing lands. *Merge:* scalar
	override.

*disable-post-merge*
	Boolean. Suppresses an inherited *post-merge* command without clearing
	the string (mirrors *disable-repair*). *Merge:* scalar override.

*repair*
	Shell script run by *sc merge* as a *REPAIR* phase — after the rebase
	and the nothing-to-merge check, but before the *pre-merge* verify hook
	— to auto-fold mechanical fixes (formatting, lint-repair) into the
	commit being merged.
	The canonical value is *conformist --commit --amend --exit-zero-on-fix*.

	Unlike *pre-merge*, repair runs *in the session worktree* (not the
	detached build worktree): it amends the post-rebase *HEAD* in place,
	which moves the branch ref and leaves the working tree clean together.
	Running it in the build worktree would discard the amend, because the
	merge fast-forwards the originally pinned sha (see *FDR 0018* in the
	source tree). spinclass detects whether the command amended anything by
	comparing *HEAD* before and after — the command is expected to exit 0
	whether or not it changed the tree (hence *--exit-zero-on-fix*); a
	non-zero exit aborts the merge. The pin then reads the post-repair
	*HEAD*, so the verify hook and fast-forward merge act on the repaired
	commit.

	Repair is *merge-only* — *sc check* and *check-this-session* do not run
	it, and merging from an implicit (main-checkout) session is not
	supported at all (spinclass#317). Because the amend re-signs *HEAD*, a locked
	commit-signing agent fails repair (and thus the merge) rather than
	producing an unsigned commit. A repair that amends before a later
	verify failure leaves the amended commit on the session branch.
	*Merge:* scalar override.

*pre-commit*
	Shell command installed as a per-session git *pre-commit* hook at *sc
	start*/*sc resume* time, repairing staged content at *authoring time*
	so each commit is conformant in history (rather than folding fixes into
	the top commit at merge, as *repair* does). The canonical value is
	*conformist --staged --exit-zero-on-fix*, which formats only the staged
	blobs and restages them — no extra commit, no amend. The hook is
	installed into *<worktree>/.spinclass/hooks/pre-commit* and scoped to
	that worktree alone via *extensions.worktreeConfig* plus a per-worktree
	*core.hooksPath*, so it never leaks into the parent checkout or sibling
	worktrees. It is *best-effort and non-blocking:* a missing formatter
	(failed *command -v*) or any non-zero exit never blocks the commit.

	Because git consults exactly one hooks directory, the installed
	*<worktree>/.spinclass/hooks* is a *composing dispatcher:* it captures
	the repo's pre-existing hooks dir (the prior *core.hooksPath* or
	*$GIT\_COMMON\_DIR/hooks*) and, for every native hook present there,
	delegates to it after running the formatter on *pre-commit*, preserving
	the native hook's arguments, stdin, and exit code. So a repo's own
	*pre-commit*/*commit-msg*/*pre-push*/... hooks keep firing rather than
	being shadowed; a blocking native hook still blocks the commit. Note
	the captured original is usually the *shared* *$GIT\_COMMON\_DIR/hooks*,
	so a blocking native hook there gates every worktree's commits, not
	one. A nonzero formatter exit (e.g. a stale formatter rejecting its own
	flags) is surfaced loudly with its stderr rather than silently
	swallowed, though the commit still proceeds. Worktree sessions only;
	implicit (main-checkout) sessions are out of scope (spinclass does not
	install hooks in a user's main checkout). See
	*docs/plans/2026-06-17-precommit-hook-composition-design.md* in the
	source tree. *Merge:* scalar override.

*disable-pre-commit*
	Boolean. Suppresses an inherited *pre-commit* command (mirrors
	*disable-repair*). On an already-installed worktree this is a true
	*uninstall:* spinclass unsets its per-worktree *core.hooksPath* and
	removes its dispatcher, so the repo's native hooks fire normally again.
	This is also the rollback for the composing hook. *Merge:* scalar
	override.

*pre-merge-output-format*
	String. One of *raw* (default) or *tap-ndjson*. Controls how spinclass
	interprets the *pre-merge* hook's stdout when assembling the
	*merge-this-session* / *check-this-session* response. When set to
	*tap-ndjson*, spinclass parses the hook's stdout as a TAP-14 ndjson
	stream via *tap/go/pkgs/{reader,ndjson}* and, on failure, emits each
	failing test record as a structured *failure:* field in the YAMLish
	diagnostic instead of the trailing-N-lines *tail:*. The madder blob
	referenced by *resource\_link:* holds the parsed ndjson rather than the
	raw stdout, and the MCP *ResourceLinkContent* block's *mimeType* is set
	to *application/x-ndjson* (versus *text/plain* when *format*=*raw*). On
	parse failure (the hook didn't actually emit TAP, or emitted zero
	parseable records), the response falls back to a 15-line *tail:* of raw
	stdout so degenerate output is still visible. The *tail:* field remains
	the default visibility signal when *format*=*raw*. *sc validate* rejects
	any value outside the closed set *{*raw, tap-ndjson*}*. *Merge:* scalar
	override.

*on-attach*
	Script run when a session attaches to the worktree, fired by *sc start*
	or *sc resume* after the session state file has been written but before
	the configured session entrypoint (_[session].start or [session].resume_)
	is exec'd. Typical use: warm caches, kick off
	background helpers (e.g. a tmux side-pane, a watcher process), or emit
	a session-start log line. A failing *on-attach* script is logged as a
	warning but never blocks session start — it must not gate attach.
	*Merge:* scalar override.

*on-detach*
	Script run when the session detaches from the worktree, fired by *sc
	start* or *sc resume* after the entrypoint exits and the session state
	has been updated to *running-detached* or *inactive* (per the
	*[session].liveness-probe* result). Typical use: tear down on-attach
	side processes, post a session-end notification, or rotate logs. A
	failing *on-detach* script is logged as a warning; spinclass continues
	with cleanup either way. *Merge:* scalar override.

*disallow-main-worktree*
	Boolean. When true, *sc resume* refuses to attach to the main worktree.
	*Merge:* scalar override.

*tool-use-log*
	Boolean. When true, a PostToolUse hook logs tool invocations. *Merge:*
	scalar override.

*disable-merge*
	Boolean. When true, *sc merge* exits non-zero before any git operation
	runs, and the *merge-this-session* MCP tool is replaced in the tool
	catalog by *check-this-session* — the two tools are mutually exclusive,
	gated on this flag. *check-this-session* runs the configured
	*[hooks].pre-merge* command without merging, providing an agent-CI
	surface for repositories where merges must go through external review
	(e.g., protected branches that require pull requests). The *sc check*
	CLI subcommand is available regardless of this flag. *Merge:* scalar
	override.

*disable-merge-queue*
	Boolean. When true, disables the per-repo merge serialization lock
	(issue #235); merges then fail immediately if the default branch moved
	during the pre-merge gate instead of queuing, rebasing, and re-gating
	under the lock. *Merge:* scalar override.

*disable-merge-stacking*
	Boolean. When true, disables the intra-session merge queue
	(*spinclass#265*, *FDR 0025* in the source tree): a second
	*merge-this-session-async* issued while a merge is already running for
	the session is refused (*a background job is already running*) instead
	of enqueued to run when the current gate completes. Distinct from
	*disable-merge-queue* above, which governs the _cross-session_ per-repo
	landing lock, not the _intra-session_ enqueue. The refusal still does
	not consume the pre-merge attestation. *Merge:* scalar override.

*disable-repair*
	Boolean. When true, suppresses an inherited *[hooks].repair* command
	without having to clear the string — the *REPAIR* phase is skipped and
	the merge proceeds straight to the *pre-merge* verify hook. *Merge:*
	scalar override.

*disable-nix-gc*
	Boolean. When true, *sc close* and *sc clean* skip the post-removal
	worktree-scoped Nix garbage collection. By default (or when this flag
	is unset), spinclass enumerates the gc roots whose link path resolves
	into the worktree being removed, expands each root's closure, and
	attempts *nix-store --delete* on each path; nix's own liveness check
	refuses paths still kept alive by other roots, so the blast radius is
	bounded to "what only this worktree was holding." The
	*sc close --nix-gc=<true|false>* flag overrides this setting per-invocation. The
	collection is a silent no-op when *nix-store* is not on *PATH*, or when
	the worktree has no gc roots. *Merge:* scalar override.

*disable-merge-build-worktree*
	Boolean. When true, the *pre-merge* hook runs in place in the session
	worktree (the legacy behavior) instead of in a detached build worktree.
	By default (or when this flag is unset) the hook runs in a transient
	*.merge-\** worktree pinned to the committed sha being merged — see
	*pre-merge* above for the rationale and the detached-HEAD caveat. Set
	this when a hook genuinely needs the session worktree's path or its
	checked-out branch ref, accepting that the session worktree is then
	frozen for the hook's duration and that uncommitted state is what gets
	verified. *Merge:* scalar override.

*disable-worktree-path-rewrite*
	Boolean. When false (the default), the PreToolUse hook rewrites a
	tool-call path that targets the _parent checkout_ (_<repo>/X_) into the
	active worktree (_<repo>/.worktrees/<name>/X_), covering the
	file-reading and -writing tools (*Read*, *Write*, *Edit*,
	*NotebookEdit*, *Glob*, *Grep*, *Bash*) and the moxy/MCP path tools
	(*folio\_\**, *rg\_search*, *get-hubbed copy-\**, and the like). The
	corrected call proceeds without a prompt; the rewrite is surfaced in a
	system message. Paths already inside the worktree, under *.worktrees/*
	or *.spinclass/* , or outside the parent checkout entirely (*$HOME*,
	*/tmp*, sibling repos, */nix/store*) pass through untouched. The
	rewrite is a no-op for implicit (main-checkout) sessions, which have no
	separate worktree. When true, the rewrite is disabled; the opt-in
	*disallow-main-worktree* deny (if also set) then applies instead.
	*Merge:* scalar override.

*disable-implicit-sessions*
	Boolean. When true, an agent started in a repository's main checkout is
	_not_ materialized as a spinclass session on *SessionStart*: the hook
	handler is a no-op, leaving the main checkout without a session
	identity (the pre-feature behavior). By default (or when this flag is
	unset) a Claude Code agent attached to a repo's main checkout — a git
	repository on its default branch, not an *sc start* worktree — becomes
	a first-class implicit session, visible in *sc list* (marked *main*),
	addressable in cross-session chat, and mergeable via
	*merge-this-session* (hook-then-push, no rebase). This is the rollback
	lever for the implicit-sessions feature. *Merge:* scalar override.

*inactivity-timeout*
	String (a Go duration such as *"180s"* or *"3m"*). When set, an
	activity watchdog wraps the *pre-merge* hook: every line the hook
	writes to stdout/stderr bumps a last-activity timestamp, and if the
	hook produces no output for longer than this duration its subprocess is
	killed (via context cancellation of the *sh -c* process) and the
	merge/check fails with a distinct *inactivity-timeout* error rather
	than the generic non-zero-exit message. This auto-kills a wedged hook
	instead of letting it run until the outer MCP/clown request deadline,
	and applies to both the synchronous (*merge-this-session* /
	*check-this-session*) and asynchronous (*\*-this-session-async*) hook
	paths. An inactivity kill is distinct from a user cancel
	(*session-job-cancel*): only the former yields the inactivity error.
	Unset (the default), empty, or a non-positive value disables the
	watchdog — the hook runs without an inactivity bound, as before. Choose
	a duration comfortably longer than the hook's quietest legitimate phase
	(e.g. a long silent compile or download step) to avoid false kills. An
	unparseable or negative value is a *sc validate* error. *Merge:* scalar
	override.

*post-merge-timeout*
	String (a Go duration such as *"10m"* or *"600s"*). Wall-clock cap on
	the *post-merge* hook. When the hook exceeds it, its subprocess is
	killed and the merge emits the usual *severity=warn* point naming this
	knob — the merge itself still succeeded, so the cap never fails a
	merge.

	Unlike *inactivity-timeout*, this is *on by default* at *10m*. The
	asymmetry is deliberate: the pre-merge gate holds nothing while it
	runs, so an unbounded gate only stalls its own session, whereas
	*post-merge* runs under the per-repo landing lock — an unbounded hook
	there wedges every other session's merge in the repo. Set *"0"* to
	disable the cap entirely.

	A single merge may *override* this value for itself: the
	*post\_merge\_timeout* parameter of *merge-this-session*(1) and
	*merge-this-session-async*, and *--post-merge-timeout* on *sc merge*
	and *sc run*, take the same duration syntax ("0" disables) and win over
	this field for that merge only, raising or lowering it with no ceiling
	— a deploy the caller knows will outrun the repo's usual cap is the
	caller's to wait for. Whatever the source, the effective cap is
	exported to the hook as *SPINCLASS\_POST\_MERGE\_TIMEOUT*,
	*SPINCLASS\_POST\_MERGE\_TIMEOUT\_SECONDS* and
	*SPINCLASS\_POST\_MERGE\_DEADLINE* (see *post-merge* above), so a
	verify poll sizes itself from the budget it actually has.

	It is a *wall-clock* bound, not an inactivity one: a deploy can
	legitimately emit nothing for minutes, so silence is not evidence of a
	wedge. A cap kill is distinct from a user cancel
	(*session-job-cancel*); only the former names *post-merge-timeout*.

	The cap *frees the merge queue; it does not stop the work.* Cancelling
	the hook kills its shell, not the whole process tree, so an overrunning
	deploy's subprocesses keep running afterwards — what is guaranteed is
	that other sessions stop waiting, not that the deploy halted. A deploy
	that must be interruptible has to arrange that itself.

	An unparseable or negative value is a *sc validate* error, and at
	runtime falls back to the *default* rather than to "no cap" — a typo
	must not silently strip a protection that is on by default. (This is
	the opposite of *inactivity-timeout*, whose default is off, so
	degrading a bad value to off changes nothing there.) *Merge:* scalar
	override.

*auto-rebuild-on-resume*
	Boolean. When true, *sc resume* re-applies a worktree's setup (as *sc
	rebuild* does) before attaching whenever its recorded setup fingerprint
	is stale — the merged sweatfile config, the spinclass binary, or the
	build-time-pinned tools changed since the setup was last applied.
	Default (false): a stale worktree only prints a warning on resume;
	refresh it explicitly with *sc rebuild*. External-tool version drift
	(e.g. a changed formatter binary behind an unchanged hook command) is
	not detected, since the config is unchanged. *Merge:* scalar override.

*allow-stale-base*
	Boolean. Permits creating a session even when the repo's default branch
	could not be confirmed current.

	By default, *sc start*, *sc run*, *spawn-session* and the
	*[[start-commands]]* plugins fetch the default branch and cut the new
	branch from the fetched remote tip, passed as an explicit start-point.
	A stale base is not merely stale source: *flake.lock* is part of the
	tree, so it materializes a stale devShell and every tool pinned below
	it.

	Only an unverifiable base refuses creation — the fetch failed (e.g. the
	remote is unreachable) — and this knob (or the per-invocation
	*--allow-stale-base*, which *spawn-session* deliberately does not
	expose) is the way past it. The local default branch is then
	fast-forwarded for convenience only: when that is blocked (uncommitted
	changes in the way), or the local branch is ahead of or has diverged
	from upstream, creation proceeds and reports an *advance local
	<branch>* skip; see *spinclass-local-default-ref*(7). Neither does a
	repo with no remote configured refuse, nor one where *main* and
	*master* both exist and *origin/HEAD* does not break the tie; those
	fall back to the pre-#250 behaviour of branching from *HEAD*.

	*sc resume* runs the same step advisorily and can never fail on it, so
	the knob only silences its report there. *sc fork* is excluded entirely
	— forking means "branch from here". *Merge:* scalar override.

*allow-no-credential*
	Boolean. When true, a failed *[auth]* mint at session creation (forge
	API unreachable, no live agent for the card login) warns and creates
	the session WITHOUT a push credential — pushes then use the inherited
	ssh-agent as before FDR 0028 — instead of failing creation and removing
	the half-built worktree (the default). The persistent half of the
	override; *sc start --allow-no-credential* and
	*sc run --allow-no-credential* are the per-invocation half. Like
	*allow-stale-base*, deliberately no MCP tool parameter: *sc spawn*
	honours only this knob. *Merge:* scalar override.

## [auth]

Per-session forge push credential (FDR 0028). When set, every worktree
session created by *sc start*, *sc spawn*, or *sc run* mints its own forge
access token at creation, so its fetches and pushes — interactive and the
*merge-this-session* landing — never depend on the inherited ssh-agent.
Both commands are run via *sh -c* in the session worktree, devshell-scoped
like the lifecycle hooks, with *SPINCLASS\_SESSION\_ID*, *SPINCLASS\_REPO*,
*SPINCLASS\_BRANCH*, *SPINCLASS\_WORKTREE*, *SPINCLASS\_FORGE\_HOST* (the
origin's host), and *SPINCLASS\_FORGE\_REPO* (the origin's _owner/name_) in
the environment.

*mint-command*
	Prints the token on stdout. Spinclass stores it as a mode-600
	git-credential-store file at _.spinclass/git-credentials_ (the line
	*https://spinclass:<token>@<host>*; Forgejo accepts any username when
	the password is an access token) and sets worktree-scoped git config on
	the session worktree — and, at merge time, on the disposable landing
	worktree the push runs from (FDR 0029): *credential.helper* pointing at
	that file, and *url.https://<host>/.insteadOf* rewriting the origin's
	ssh form to https for that host only, so remotes on other hosts
	(GitHub) keep ssh. The root checkout's config is never touched. A
	failed mint fails session creation and removes the half-built worktree:
	a session that silently pushed off the agent instead would be exactly
	the failure this exists to remove. *Merge:* scalar override.

*revoke-command*
	Revokes the token by session id. Run at *sc close* and when *sc clean*
	reaps a merged worktree (a failure warns and leaves the token recorded
	unrevoked), and by the orphan sweep at the next session creation on the
	repo for sessions that ended without revoking (crashed, or removed
	outside spinclass). *Merge:* scalar override.

*forge-hosts*
	Array of origin hosts the mint applies to. When non-empty, a session
	whose origin host is not listed skips the mint (a visible SKIP point)
	and is created exactly as without *[auth]*, so one parent-directory
	entry can cover a tree of repos without a GitHub-origin repo ever
	minting a forge token and failing its creation. Empty or unset means
	any host. *Merge:* override, not append — a nearer level replaces the
	list, *[]* clears it, unset inherits.

*sc validate* warns when only one of the two commands is set, and when
*mint-command* is set without *forge-hosts*. A failed mint (forge API
unreachable, no live agent for the card login) is fatal to session creation
by default and the half-built worktree is removed;
*[hooks].allow-no-credential* (or *sc start/run --allow-no-credential*)
instead warns and creates the session without a credential, pushing over
the inherited ssh-agent as before. Implicit (main-checkout) sessions are
outside this: they are the operator in the root and keep ssh.

## [session-entry]

Controls what command is exec'd when attaching to a session, how detached
worker sessions are launched, what environment is injected, how liveness is
detected after detach, and how long tombstones are retained.

*start*
	Argv array for new sessions. Defaults to *$SHELL*. Each element
	supports *$VAR* expansion against the session environment, so
	*"$SPINCLASS\_SESSION\_ID"* substitutes at exec time. *Merge:* scalar
	override.

*resume*
	Argv array for resumed sessions. When nil, falls back to *start*.
	*Merge:* scalar override.

*spawn-entry*
	Argv array for the detached-harness a spawned worker session boots
	into, exec'd *directly* by *sc spawn* and detached *sc fork --brief*
	(FDR-0017 Piece 1: spinclass no longer wraps the worker in a
	multiplexer — the harness self-detaches and returns promptly, e.g.
	clown's *--clown-attach=spawn*). *{prompt}* is replaced by the driver's
	brief verbatim, staying within its single argv element — no shell
	joining or re-splitting — and *{dir}* by the worker worktree's absolute
	path. Defaults to *["clown", "--clown-attach=spawn", "--",
	"{prompt}"]*; override per the sweatfile cascade for a different
	harness. Template contract: the argv must detach the session and return
	promptly — *sc spawn* blocks until the worker's hello chat message
	arrives (60s default deadline), so a harness that blocks in the
	foreground burns the deadline. The exec'd process inherits the session
	env including any keys from *[session-entry].env*, carrying the
	_worker's_ *SPINCLASS\_\** identity — never the driver's. *sc validate*
	warns when no element contains *{prompt}* (legal, but the worker would
	boot without the driver's brief). *Merge:* scalar override.

*spawn-window*
	Argv template exec'd fire-and-forget right after the *spawn-entry*
	launches (before the hello wait, so the harness boot is watchable
	live): it opens a terminal window onto the freshly spawned worker.
	*{id}* expands to the worker's session key and *{dir}* to the worker
	worktree's absolute path; this is a leaf argv — *{entry}* and
	*{prompt}* are rejected by *sc validate*, which also warns when no
	element contains *{id}* or *{dir}* (the window command could not
	identify its session). The command runs with the worker's
	*SPINCLASS\_\** identity and cwd set to the worker worktree; start and
	exit failures are logged warnings, never spawn failures. No default —
	opening windows is a desktop preference. A typical value points at a
	small user script that dispatches per platform, e.g.
	*["sc-spawn-window", "{id}", "{dir}"]* wrapping _kitty --detach --title
	sc/$1 --directory $2 sc resume $1_ on Linux or
	_open -na kitty --args ..._ on macOS. *Merge:* scalar override.

*model-flags*
	Map from a clown provider name (_or profile name, see below_) to the
	CLI flag that provider's binary uses to select a model. The lookup key
	is resolved by scanning the resolved *spawn-entry* for, in order,
	(*--provider* or *--provider=*) then (*--profile* or *--profile=*),
	defaulting to *claude* (clown's own default) when none are present. An
	explicit *--provider* always wins over *--profile*, matching clown's
	own precedence; when only *--profile* is present, spinclass cannot
	resolve which provider it implies (that requires querying clown's own
	profile registry), so the profile name itself is used as the lookup
	key — a *model-flags* entry may be keyed by profile name instead of
	provider name for this case. Consulted when the (*spawn-session* /
	*fork-session*) *model* param, or the equivalent (*sc spawn --model* /
	*sc fork --brief --model*) CLI flag, is set: the flag and the requested
	model (_alias_) are spliced into *spawn-entry* immediately after its
	literal *"--"* provider-args separator. The requested value is
	validated against a fixed alias set (*sonnet*, *opus*, *haiku*, or
	*fable*) *only* when the resolved key is *claude*; for any other key
	(juggler, a profile name, ...) the value is passed through unvalidated
	— spinclass has no registry to check it against, so a bad value only
	surfaces as a failure from the provider itself. Defaults to *{ claude =
	"--model" }*, the only mapping verified against an actual provider CLI;
	a model requested for a key absent from this map, or for a
	*spawn-entry* with no *"--"*, is a hard spawn error. *Merge:* per-key
	map merge (child wins on collision, parent-only keys survive), same as
	*[session-entry.env]*.

*liveness-probe*
	*Deprecated* (post-FDR-0017): clown owns the multiplexer attach and
	names sessions by its own per-instance key, so this argv can no longer
	match. Spinclass now derives liveness from clown's presence index
	(*decoration* == *$SPINCLASS\_SESSION\_ID*), and this probe is no
	longer consulted — retained only until removed. Formerly an argv array
	exec'd after the entrypoint returned, to distinguish *running-detached*
	from *inactive*, inheriting the session env including any keys from
	*[session-entry].env*; e.g. *["sh", "-c", "posh -g
	\\"$SPINCLASS\_GROUP\\" list --short | grep -qxF
	\\"$SPINCLASS\_SESSION\_ID\\""]*. *Merge:* scalar override.

*tombstone-retention*
	Duration string (e.g. *30d*, *0* to keep forever) controlling how long
	*sc clean* preserves tombstone entries before reaping. The same
	retention also governs the cross-session chatroom: messages older than
	this window, and stale per-session read cursors, are reaped by *sc
	clean* (see *chat-send*(), *chat-read*()). *Merge:* scalar override.

## [session-entry.env]

Subtable map of environment variables injected into every session process
(entrypoint, hooks, liveness probe). Keys are strings, values are strings.
Values are set *literally* — unlike *[direnv].dotenv* and the
*[session-entry].start* argv, they are *not* *$VAR* / *$WORKTREE* expanded. For a
worktree-relative path, use *[direnv].dotenv* (whose values do expand
*$WORKTREE*) rather than *[session-entry.env]*. This is a generic
env-injection mechanism with one convention: *SPINCLASS\_GROUP* is the
customary key for a multiplexer-group name so the *liveness-probe* argv can
reference it symbolically instead of hardcoding the group in two places.
Spinclass-owned variables — *SPINCLASS\_SESSION\_ID*, *SPINCLASS\_REPO*,
*SPINCLASS\_BRANCH*, *SPINCLASS\_WORKTREE*, *SPINCLASS\_DESCRIPTION*,
*TMPDIR*, *CLAUDE\_CODE\_TMPDIR* — are applied AFTER this map and so cannot
be clobbered. *Merge:* per-key map merge (child wins on collision,
parent-only keys survive).

## [[start-commands]]

Array of tables declaring custom *sc start-*_name_ subcommands. See
*spinclass-start-commands*(7) for the full schema and JSON protocols.
*Merge:* dedup-by-name (later definition replaces earlier one in-place).

## allowed-mcps

Top-level array of MCP server names to auto-approve in
_.claude/settings.local.json_. These servers must be registered externally
(e.g. in the user's _~/.claude.json_). *Merge:* nil = inherit, empty =
clear, non-empty = append.

## [[mcps]]

Array of tables declaring MCP servers to register in _.mcp.json_ and
auto-approve.

*name*
	(required) Server name. Used as the key in _.mcp.json_ and in
	_enabledMcpjsonServers_.

*command*
	Executable to run. A name-only entry (empty command) acts as a removal
	sentinel, removing an inherited server.

*args*
	Argv passed to *command*.

*env*
	Table of environment variables set on the server process.

*Merge:* dedup-by-name (full entry replacement, same as
*[[start-commands]]*). Every entry with a non-empty *command* implicitly
adds to the allow-list.

## [[pre-merge-skills]]

Array of tables declaring skill names that the agent must address before
*merge-this-session* or *check-this-session* will run the pre-merge hook.
When the resolved list is non-empty, those MCP tools refuse to proceed
until a fresh attestation has been recorded via the companion
*nothing-but-the-truth* tool, which spinclass registers automatically
alongside whichever of *merge-this-session* / *check-this-session* is
active. The terminal CLI (*sc merge*, *sc check*) is unaffected.

*name*
	(required) Skill identifier as it appears in the Claude Code skill
	catalog (e.g. *eng:code-reviewer*, *simplify*, *security-review*). A
	name-only entry (empty or absent *rationale*) acts as a removal
	sentinel, removing an inherited skill from the resolved list.

*rationale*
	Free-form string describing why the skill is required for this repo or
	sweatfile scope. The text is echoed back to the agent in the
	gate-failure response and must be addressed in the agent's per-skill
	reasoning.

*Merge:* dedup-by-name (full entry replacement, same as *[[mcps]]*). A
non-empty resolved list activates the attestation gate; an empty or absent
list leaves merge behaviour unchanged.

See *PRE-MERGE SKILL ATTESTATION* below for the *nothing-but-the-truth*
tool's input schema, lifecycle, and gate-failure response shape.

## [[remotes]]

Array of tables declaring remote hosts whose spinclass sessions appear in
*sc list* and tab completion under a _host_*:* prefix and can be reattached
via *sc resume *_host_*:*_id_. Remotes normally live in the global
sweatfile. Completion entries for _host_*:* targets are served from a
per-host cache refreshed by each *sc list* — completion never networks.

*name*
	(required) The _host_*:* prefix users type. A name containing *:* or
	*/* is an *sc validate* error.

*ssh*
	ssh destination substituted for the attach template's *{ssh}*
	placeholder. Defaults to *name* when empty, so a name-only entry is a
	valid all-defaults remote and _~/.ssh/config_ does the work.

*attach*
	Argv template exec'd by *sc resume* for _host_*:* targets. *{ssh}* and
	*{id}* are substituted literally in every element; the argv is exec'd
	directly (no shell). Defaults to *["ssh", "-t", "{ssh}", "spinclass",
	"resume", "{id}"]*. An empty element is an *sc validate* error.

*remove*
	Boolean. When true, the entry is an explicit removal sentinel that
	removes an inherited remote of the same name. This deliberately
	diverges from the *[[mcps]]* name-only removal sentinel: a name-only
	*[[remotes]]* entry is the most natural declaration of an all-defaults
	remote (the ssh destination defaults to the name, the attach template
	to the default above), so it cannot double as the removal sentinel —
	removal must be explicit. Setting *remove = true* alongside *ssh* or
	*attach* fields is an *sc validate* warning (the fields are ignored).

*Merge:* dedup-by-name (full entry replacement, same as *[[mcps]]*).

Example:

```
[[remotes]]
name = "devbox"           # the <host>: prefix users type
ssh  = "sasha@devbox.lan" # optional; defaults to name
# attach defaults to:
# ["ssh", "-t", "{ssh}", "spinclass", "resume", "{id}"]

# name-only = all-defaults remote; ~/.ssh/config does the work
[[remotes]]
name = "lab"
```

Drop an inherited remote in a child sweatfile:

```
[[remotes]]
name   = "devbox"
remove = true
```

## [[post-merge]]

Array of tables declaring NAMED post-merge deploy targets (FDR 0026). A
top-level array — distinct from the legacy *[hooks].post-merge* scalar
string — modelled on *[[mcps]]*. Each target is a deploy trigger and an
optional acceptance check, run after a merge lands, under the per-repo
landing lock, with the same *SPINCLASS\_MERGED\_\** environment as the
legacy hook (see *[hooks].post-merge* above).

*name*
	(required) Target name. Used to select the target at merge time and to
	label its verdict. A name-only entry (empty *command*) acts as a
	removal sentinel, removing an inherited target of the same name (like
	*[[mcps]]*).

*command*
	The deploy trigger. Run first; a nonzero exit yields the
	*command-failed* verdict and *verify* is not run.

*verify*
	(optional) A remote acceptance check, run only when *command* exits
	zero. A nonzero exit yields the *verify-failed* verdict. The
	command-failed / verify-failed split tells a human whether to fix the
	change or investigate the probe/ack path. A target with no *verify* is
	opaque-command: command zero is *ok*, nonzero is *command-failed*.

*Verdicts:* each target emits one result point labeled "*post-merge <name>
(<sha>)*" with a per-target verdict of *ok*, *command-failed*, or
*verify-failed*. Every failure is NON-fatal — the merge already landed —
and carries *severity=warn*. The async completion wake surfaces every
failed target.

*Supersession:* when any active *[[post-merge]]* target exists, the legacy
*[hooks].post-merge* string is NOT run — the named targets are the phase.
With no named targets, the string runs exactly as before. A repo migrates
by adding stanzas (the string goes dormant) and deleting it at leisure; no
flag-day.

*Selection:* *merge-this-session* and its *-async* twin take a *targets*
parameter, and *sc merge* takes *--post-merge-targets* (comma-separated)
and *--no-post-merge*. Omitting the selection deploys ALL active targets; a
subset deploys only those names; an empty selection deploys none (a
docs-only merge). A name no target declares fails the merge BEFORE it
lands.

*Timeout:* *[hooks].post-merge-timeout* bounds the WHOLE phase — all
selected targets run *concurrently* under one shared wall-clock deadline,
so lock-hold is *max* of the targets, not their sum. Size it for the
slowest single target's command plus verify; when the deadline fires, every
still-running target is killed and reports *verdict=timeout*. Verdict
points are emitted in declaration order after all targets complete, so the
result ladder is deterministic regardless of finish order. A single merge
may override the cap for itself — *post\_merge\_timeout* on
*merge-this-session*(1) / *merge-this-session-async*, or
*--post-merge-timeout* on *sc merge*; same syntax, "0" disables, no
ceiling.

*Environment:* every target's *command* and *verify* receive the
*SPINCLASS\_MERGED\_\** facts of *[hooks].post-merge* plus the effective cap
as *SPINCLASS\_POST\_MERGE\_TIMEOUT* (Go duration, "0" when uncapped),
*SPINCLASS\_POST\_MERGE\_TIMEOUT\_SECONDS* (integer) and
*SPINCLASS\_POST\_MERGE\_DEADLINE* (unix epoch seconds of the shared
deadline, "0" when uncapped) — a verify poll should derive its budget from
the deadline, since the command already spent part of it — together with
*SPINCLASS\_POST\_MERGE\_TARGET* (this target's *name*, so one script can
serve several targets) and *SPINCLASS\_PINNED\_SHA* (the pre-landing pin;
differs from *SPINCLASS\_MERGED\_SHA* only on a rebased landing).

*Merge:* dedup-by-name (full entry replacement, same as *[[mcps]]*).
Suppressed entirely by *[hooks].disable-post-merge*.

Example — split a deploy into a trigger and an acceptance check:

```
[[post-merge]]
name    = "krone"
command = "just infra/hosts/trigger-redeploy-krone"
verify  = "kssh krone -- systemctl is-active krone.service"

[[post-merge]]
name    = "nikulin"
command = "just infra/hosts/trigger-redeploy-nikulin"
```

# PRE-MERGE SKILL ATTESTATION

When a sweatfile's resolved *[[pre-merge-skills]]* list is non-empty,
spinclass registers an additional MCP tool, *nothing-but-the-truth*, and
gates *merge-this-session* / *check-this-session* behind it.

## Gate behaviour

On invocation, if no fresh attestation is buffered in the session state
file (_~/.local/state/spinclass/sessions/<hash>-state.json_), the gated
tool fails before running any pre-merge hook with a TAP-14 response
containing *required\_skills* (the full list, each entry's *name* and
*rationale*) and *required\_tool:* nothing-but-the-truth*.*

Each merge attempt consumes one attestation. After consumption the record
is cleared; a subsequent merge requires a new attestation call against the
current diff. There is no sticky once-per-session mode.

The terminal CLI (*sc merge*, *sc check*) bypasses the gate entirely.

## nothing-but-the-truth input schema

```
{
  "skills": [
    {
      "name":      "eng:code-reviewer",
      "used":      true,
      "reasoning": "Ran the review; addressed one nil-check finding."
    },
    {
      "name":      "simplify",
      "used":      false,
      "reasoning": "Single-line bugfix, no abstraction surface to prune."
    }
  ]
}
```

*skills*
	(required) Array containing exactly one entry per skill listed in the
	resolved *[[pre-merge-skills]]*. Names must match exactly; missing or
	unrecognised names fail validation.

*skills[].name*
	(required) Skill identifier matching a *[[pre-merge-skills]]* entry.

*skills[].used*
	(required) Boolean. True if the skill was invoked for this diff; false
	otherwise.

*skills[].reasoning*
	(required) Non-empty free-form string. The tool does not police length,
	style, or content — the act of writing the reasoning down is the
	deliverable.

## Validation

Validation is strict on presence (every listed skill must have an entry,
names must match exactly, reasoning must be non-empty) and lenient on
content. On failure the tool emits a TAP error listing missing and
unrecognised names so the agent can correct and retry without re-fetching
the skill list.

## Mutual exclusivity with [hooks].disable-merge

The companion tool tracks the active gated tool:

- *disable-merge* unset/false plus a non-empty *[[pre-merge-skills]]* list
  registers *merge-this-session* and *nothing-but-the-truth*.
- *disable-merge = true* plus a non-empty *[[pre-merge-skills]]* list
  registers *check-this-session* and *nothing-but-the-truth*.
- An empty or absent *[[pre-merge-skills]]* list leaves
  *nothing-but-the-truth* unregistered.

# MERGE SEMANTICS

Each field uses one of these merge strategies when combining files in the
hierarchy:

*nil = inherit, empty = clear, non-empty = append*
	Used by array fields: *claude.allow*, *git.excludes*, *direnv.envrc*,
	*allowed-mcps*. A nil value (key absent) inherits the parent's value.
	An explicitly empty array clears inherited values. A non-empty array is
	appended to the parent's list.

*scalar override*
	Used by single-value fields: *hooks.create*, *hooks.stop*,
	*session-entry.start*, etc. The child's value replaces the parent's
	entirely.

*dedup-by-name*
	Used by array-of-tables: *[[start-commands]]*, *[[mcps]]*,
	*[[pre-merge-skills]]*, *[[remotes]]*. Entries are matched by *name*; a
	later entry replaces the entire earlier entry in-place (not a
	field-level merge). New names are appended. Removal of an inherited
	entry is per-table: *[[mcps]]* and *[[pre-merge-skills]]* use a
	name-only sentinel, while *[[remotes]]* requires an explicit *remove =
	true* entry.

*map merge*
	Used by *direnv.dotenv* and *mcps.env*. Child keys override parent
	keys; parent keys not present in the child are preserved.

# FILES

_~/.config/spinclass/sweatfile_
	Global sweatfile (lowest precedence).

_<parent-dir>/sweatfile_
	Intermediate parent directory sweatfiles.

_<repo>/sweatfile_
	Repository-level sweatfile.

_<worktree>/sweatfile_
	Worktree-level sweatfile (highest precedence).

_.claude/settings.local.json_
	Generated by spinclass from sweatfile config. Contains
	*permissions.allow* and *enabledMcpjsonServers*. The *spinclass* MCP
	server itself and its Claude Code hooks (*PreToolUse*, *Stop*,
	*PostToolUse*) are loaded once per session via the clown plugin
	manifest shipped at _<plugin-share>/hooks/hooks.json_, not mirrored
	into this file.

_.mcp.json_
	Generated by spinclass when at least one user-declared *[[mcps]]* entry
	is present. Contains MCP server registrations from those entries; the
	*spinclass* MCP server is loaded via the clown plugin and is not
	written here.

# SEE ALSO

*spinclass*(1), *spinclass-start-commands*(7), *spinclass-validate*(1)
