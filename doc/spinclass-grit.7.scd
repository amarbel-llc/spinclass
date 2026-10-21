spinclass-grit(7) "spinclass" "Spinclass Reference"

# NAME

spinclass-grit - the grit store tools: session-owned git operations and their permission model

# DESCRIPTION

spinclass serves nine git tools as MCP tools, named *grit\_stage*,
*grit\_branch*, *grit\_commit*, *grit\_tag*, *grit\_worktree*, *grit\_stash*,
*grit\_remote*, *grit\_merge* and *grit\_clone*. Their leaf names, subcommands
and argument shapes are those of moxy's grit moxin; only the qualified MCP name
differs. They replace moxy's per-tool permission annotations with a model that
knows which session, worktree and branch the caller owns. Design record: FDR
0036 (*docs/features/0036-grit-store-tools.md*). Code: *internal/store* (the
backend-neutral layer), *internal/store/git* (the git backend) and
*cmd/spinclass/grit\_cmd.go*.

The tools run in the frontend *spinclass serve*, not in the caller. For a
confined worker (FDR 0035) the worker's own *.git* stays read-only and the
frontend does the writing.

# VERDICT MODEL

Every invocation is classified, without side effects, into a verdict:

*Allow*
	The call proceeds without a prompt.

*Ask*
	A human is prompted. Under a mode that denies prompts, such as *dontAsk*,
	an Ask is a deny. Deny is never a verdict: a refusal is made by the tool
	body, not by classification.

Each verb belongs to one class:

*read*
	Allow for any ref or path. These are *stage* status and diff, *branch* log,
	rev-parse and diff, *tag* list and verify, *worktree* list, and *remote*
	fetch. A *remote* fetch of a name that is not a configured remote is Ask.

*write*
	Allow if and only if every target is owned by the caller's session (see
	OWNERSHIP) and the call runs in the session's own worktree; otherwise Ask.
	These are *stage* add, unstage, rm and mv; *commit*; *merge*; *clone*;
	*branch* create, delete, checkout, reset-soft, reset-hard, rebase, restack,
	cherry-pick and revert; *tag* create, delete and push; *worktree* remove;
	*remote* pull, push and push-stack; and *stash* list, save, apply and drop.
	*stash* is a write verb only so that it requires the session-worktree
	binding; it acts on the session's own shelf and needs no ownership
	argument.

*floor*
	Ask unconditionally. No defined verb is in this class today; it is the
	class of any verb the classifier does not know. A call with any
	argument of the wrong type, a ref-like value that begins with *-*, or
	*force* set (anything other than absent, null or false) is also Ask
	whatever its class.

The verdict is computed by one function shared by the permission hook and the
tool body. The PreToolUse hook consults it for these tools after the *AlwaysAsk*
floors. The hook's principal is *CLOWN\_SESSION\_ID* when set, else the hook's
*session\_id* (clown does not export *CLOWN\_SESSION\_ID* to the hook); if both
are set and differ the principal is known but unusable. A principal that
resolves to one recorded session lets *Classify* decide. A principal that is
known but does not resolve (no recorded session, an ambiguous one, or
conflicting sources) always asks, *force* or not, because ownership cannot be
judged. Only when the hook sees no principal at all does it return Allow with
the reason "deferred to the serve: body ownership check applies", except that
a call with *force* set still asks. That deferral is safe only because the tool
body fails closed, see CONFINED, UNCONFINED AND NO-SESSION CALLERS.

# OWNERSHIP

A session whose branch is _B_ owns:

- the branch _B_ and any branch named _B_*--*_topic_ (*refs/heads/*);
- the remote-tracking ref of such a branch (*refs/remotes/<remote>/*);
- tags named _B_*--*_something_ (*refs/tags/*);
- refs under *refs/spinclass/*_B_*/*, which the tools use for refs that are not
  branches;
- its own worktree, by path.

A generated session name has no *--* in it, so no session's namespace is a
prefix of another's. A session creates scratch branches as _B_*--*_topic_.

The default branch is never owned: *main*, *master* and the branch origin's
HEAD points at, whatever the session branch is called.

A target is resolved to its canonical full ref (using *git rev-parse* with
*\--symbolic-full-name*) before it is checked, so *HEAD*, *@*, abbreviations and
*refs/heads/* forms cannot launder a foreign branch past a prefix test.
Revision syntax (*..*, *^*, *~*, *@{*), option-like values, characters git
forbids in refs and a detached HEAD are never owned. A verb with no named target
(*commit*, *stage* add) is bound to the branch checked out in the worktree the
call runs in, and that worktree must be the session's own; *repo\_path* is
resolved against the session worktree. Rebase with *update\_refs* and *restack*
are Ask when the rewritten range would also move a branch the session does not
own.

*worktree* remove is Allow only for another worktree whose checked-out branch is
owned and with *force* unset; the tool body always refuses the worktree the call
runs in. *clone* is Allow only when the destination, with symlinks resolved on
the part that exists, lies inside the session worktree (its *.tmp* included);
the source is not gated.

# CONFINED, UNCONFINED AND NO-SESSION CALLERS

The tool body, which runs in *serve*, resolves the caller:

_Confined_
	A caller is confined unless frontend state positively shows otherwise
	(*internal/confinement*): an unresolved principal, a unit record, a
	frontend principal mapping, an unreadable record, or more than one live
	*serve* record for the principal all say confined. Nothing is staged in
	the unit. For a confined caller the body re-applies the write-verb
	ownership check, ignoring *force* (no human could have approved it), and
	refuses what it rejects. Reads are scoped to the session worktree: a
	*repo\_path* whose git toplevel is another repository is refused. *clone*
	is refused outright. Every mutating verb, and *remote* fetch, runs with
	*core.hooksPath* set to */dev/null*, and *commit* also gets *\--no-verify*,
	because the hooks directory is worker-writable and its hooks must not run
	in the frontend; the merge gate remains the check. Pushing or deleting the
	default branch is refused whatever *force* says.

_Unconfined_
	A caller whose worktree is a recognised session worktree and whom no
	state shows confined. The hook is the sole ownership gate: the body does
	not re-check, because a tool cannot prompt and, once a human has approved
	a foreign operation, it must run. Git hooks run as usual. A resolved
	unconfined caller pushing the default branch gets Ask from the hook.

_No session_
	A caller whose worktree does not resolve to a live, non-implicit session
	worktree owns nothing. Reads work; the body refuses every write, for any
	confinement, with an error pointing at *sc start*. Classification is Ask.
	This includes a plain checkout. The way out is *sc grit* (see below).

For every caller the body refuses, without exception, *branch* delete,
reset-hard, rebase and restack whose target is the default branch, and *merge*
into it (*merge* abort and continue excepted). Nothing is approvable into these.

# THE AGENTLESS REMOTE LANE

*remote* fetch, pull, push, push-stack, *tag* push and *clone* run git with the
session worktree as its working directory, so the worktree-scoped
*credential.helper* and *url.insteadOf* that per-session credentials install
(FDR 0028, *spinclass-sweatfile*(5), *[auth]*) apply. The environment is
stripped of *SSH\_AUTH\_SOCK*, *SSH\_AGENT\_PID*, *GIT\_SSH* and
*GIT\_SSH\_COMMAND*; ssh is then pinned to *ssh -o IdentityAgent=none -o
IdentitiesOnly=yes -o BatchMode=yes* and *GIT\_TERMINAL\_PROMPT=0* is set. Where
no credential was minted there is no fallback: the verb fails with git's own
authentication error. Key files ssh finds itself (default identities,
*IdentityFile* in the ssh configuration) are not an agent and are not blocked.

Only configured remote names are accepted. *remote* may be empty (git's
default, origin for pull and push) or the name of a configured remote. A URL is
refused by the body for every caller and classifies Ask, so the worktree's
credentials cannot be sent to a host nobody configured.

Verdicts: *fetch* is read class. *pull*, *push*, *push-stack* and *tag push* are
Allow when the remote is configured and the targets are owned (for *pull*, the
current branch). *delete*, *force* and anything not owned are Ask. *tag create*
and *tag delete* are Allow for names beginning with _B_ and two hyphens. *merge* is Allow when the current
branch is owned.

*push* always uses an explicit refspec,
*refs/heads/<src>:refs/heads/<dst>*, with _src_ canonicalized (default: the
current branch). *force*, *force\_with\_lease*, *lease\_ref\_sha*,
*force\_if\_includes*, *set\_upstream* and *delete* are supported;
*lease\_ref\_sha* or *force\_if\_includes* without *force\_with\_lease*, and
*force* together with *force\_with\_lease*, are errors. *lease\_ref\_sha* with
*force\_with\_lease* is the explicit *--force-with-lease=<branch>:<sha>* form,
which needs no remote-tracking ref; *force\_with\_lease* alone is the boolean
form. *lease\_ref\_sha* accepts 4 to 64 hex characters.

*push-stack* force-pushes the named branches bottom to top with
*--force-with-lease* (boolean form, per branch) after a dry run of every branch,
and pushes nothing unless the whole dry run passed. It returns JSON,
*{"phase", "results": [{branch, status, output}]}*, and a failure returns the
same JSON as the error. *lease\_ref\_sha* does not apply to it. *push-stack* and
*merge* into the default branch are refused for every caller.

# ACCEPTED BRANCH FORMS (spinclass#393)

*branch* create, checkout and delete each accept one form:

- *create*: *name* is the new branch (a short name), *start\_point* the optional
  start (default HEAD). Returns *created branch <name> at <sha>*.
- *checkout* of an existing branch: *ref*.
- *checkout* with *new\_branch*: *name* is the new branch, *start\_point* the
  start. The legacy form, *ref* alone naming the new branch, still works only
  when *ref* is a plain local-style name; otherwise it is refused with a pointer
  to *name* and *start\_point*. *name* and *ref* together with *new\_branch* is
  refused, as is *name* or *start\_point* without *new\_branch*.
- A new branch's name (create and checkout) is refused when it begins with
  *refs/*, resolves as *refs/remotes/<name>*, or begins with a configured
  remote's name and a slash. Nothing is created on refusal. To branch from
  *origin/master*, pass *name=<new> start\_point=origin/master*.
- *delete*: the short name or *refs/heads/<name>*, normalised to the short name
  before *git branch -d* (or *-D* with *force*). Any other *refs/* prefix is
  refused; *heads/x* is not an alias.
- Ownership is judged on the same ref: the new branch's name for create and
  checkout with *new\_branch*, the normalised *refs/heads/<short>* for delete,
  which must exist. *start\_point* never affects ownership.

# EMPTY OUTPUT

A git verb that succeeds silently would give an MCP client a content block with
no text, which clients reject. When a tool produces no output, the result text
is the fixed sentence *ok (git printed no output)*. *branch* verbs return their
own confirmation instead (for example *deleted branch <name>*). The sentence is
added on the MCP path only; *sc grit* prints nothing for a silent success and
exits 0.

# STASH

*stash* does not use git's repository-wide stash stack, which every worktree
shares. Entries live in a per-worktree shelf at *<worktree>/.spinclass/stash/*,
so another session cannot address them. grit never reads or writes *refs/stash*,
so a plain *git stash* made by hand is untouched. Stash is session-local for
every caller, confined or not: the shelf is always this session's own, and a
*repo\_path* whose git toplevel is not the session worktree (or that git cannot
resolve) is refused with _stashes are session-local: grit\_stash only touches
this session's own shelf_. A worktree reused by a later session shows that
session the earlier shelf entries. Subcommands are save, list, apply and
drop. *stash\_ref* is *stash@{N}* (the Nth newest) or a bare entry number; empty
means the newest. Apply is *git apply --3way*; on a conflict the entry is kept
and the paths are named. Submodule changes are refused and ignored files are
never shelved. Entries die with the worktree: *sc close* refuses a non-empty
shelf without *--force*, and *sc resurrect* restores the commit only.
*branch* rebase with *autostash* still uses git's own transient stash within a
single call.

# THE sc grit ESCAPE HATCH

*sc grit* _tool_ ['_json_'] runs one grit tool by hand, as an unconfined
caller, for an operator who is outside any session or when the rules misfire.
It is CLI-only: it is not an MCP tool and nothing on the hook path reaches it.
It calls the same backend as the MCP tools, so remote verbs stay agentless.

- It refuses unless stdin and stdout are both terminals. The arguments are one
  JSON object on the command line (default *{}*) and are never read from stdin;
  stdin is read only for the confirmation.
- The session is the recognised session worktree when the working directory is
  in one; otherwise the working directory, with no session branch, but the
  no-session write refusal does not apply. The *[store]* identity is stamped as
  for the MCP path.
- The verdict is the classification for that session. A verb that is not Allow
  prints the verb and arguments to stderr and needs a typed *y*; *--yes* skips
  the typing but a terminal is still required. A declined confirmation runs
  nothing and is logged. Outside a session every write classifies Ask, so every
  write needs *y* or *--yes*.
- Each invocation that gets past the argument checks is appended as one JSON
  line (*time*, *cwd*, *tool*, *subcommand*, *args*, *verdict*,
  *confirm\_required*, *confirmed*, *outcome*, *error*) to
  *$XDG\_STATE\_HOME/spinclass/grit-escape.log* (default
  *~/.local/state/spinclass/*; directory mode 0700, file 0600). The log is opened
  before anything runs, so an unwritable log refuses the operation. A run
  refused for lack of a terminal, malformed arguments or an unknown tool is not
  logged.
- The body's default-branch refusals (*branch* delete, reset-hard, rebase and
  restack, and *merge*, on *main*, *master* or origin's HEAD) apply, *--yes* or
  not. The hatch adds its own rule, *the escape hatch never force-pushes,
  deletes, or rewrites the default branch*: before the verb runs, and whatever
  *--yes* or a typed *y*, a *remote* push of the default branch (as source or as
  *remote\_branch*) with *force*, *force\_with\_lease* or *delete*, or that is not
  a fast-forward, and any *remote* push-stack naming it, is refused and logged.
  The fast-forward test is *git merge-base --is-ancestor* between the
  remote-tracking ref and the pushed commit; a missing remote-tracking ref is
  refused. A plain fast-forward push of the default branch is an Ask answered
  with *y*; *tag* push is unaffected.

# KNOWN LIMITATIONS

- Key-file ssh: the remote verbs strip the ambient ssh-agent and pin ssh to
  *IdentityAgent=none*, but ssh key files that ssh finds itself are not blocked.
  The operator ruled to leave ssh remotes allowed.
- Owned-branch lease pushes are unprompted: a *push* with *force\_with\_lease*
  of an owned branch is Allow; only non-owned refs, remote deletes and the
  *force* flag ask. (The design record says *force* and *force\_with\_lease* both
  classify Allow on an owned branch; the classifier asks for *force* because it
  treats any set *force* argument as Ask.)
- No-session callers are read-only: outside a recognised session worktree every
  write is refused with no in-band escape. Recovery is *sc grit*, or moxy's
  grit.
- *branch* reset-hard discards uncommitted work: on an owned branch, in the
  session's own worktree, it is Allow, never prompts, and throws away
  uncommitted changes and commits not reachable from the new ref. The default
  branch and unowned branches still ask or are refused.
- The stash emulation is not byte-for-byte *git stash*.
- A confined caller is detected from frontend state. Several peers sharing one
  *serve* through the clown stdio-bridge loopback port look like one *serve* with
  one principal and are not distinguished. A corrupt unit record for an unrelated
  session makes every caller classify as confined (fail closed); repair or
  remove the record.
- An unconfined session whose hook sees no principal at all (neither *CLOWN\_SESSION\_ID* nor a hook *session\_id*) gets no human
  prompt on foreign operations; the body refuses writes it does not own instead.
- Not exercised against a real confined unit, a live clown, an https push with a
  minted credential, the *allow-no-credential* ssh path, signing with a real
  key, or a real terminal run of *sc grit*.
- A manager provisioning git credentials for the workers it spawns is not covered
  (spinclass#390).

# CONFIGURATION

The *[store]* sweatfile table sets the author and committer identity for the
commits the tools create. It is read only from layers above the repo; see
*spinclass-sweatfile*(5).

# SEE ALSO

*spinclass-sweatfile*(5), *spinclass-internals*(7)

spinclass FDR 0036 (grit store tools), FDR 0028 (per-session credentials), FDR
0032 (principals), FDR 0035 (session confinement), FDR 0019 (per-commit hook).
