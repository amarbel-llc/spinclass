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
*session\_id*; if both are set and differ, *CLOWN\_SESSION\_ID* wins and the
mismatch is logged. A principal that resolves to one recorded session lets
*Classify* decide. A principal resolves only through the frontend principal
mapping, which is written for confined workers alone, so an unconfined
caller's principal normally does not resolve. When it does not (no principal,
no mapping, an ambiguous one, or conflicting sources) the verdict depends on
*CLOWN\_UNIT*, which clown sets inside a confined unit only (a confined
worker's hook sees its principal but cannot resolve the session). With it set
the hook returns Allow with the reason "confined unit, session unresolved here,
deferred to the serve: body ownership check applies". With it empty the hook
resolves the session from the hook input's *cwd* instead (the live, non-implicit
worktree session containing it, spinclass#415) and *Classify* decides; only when
that fails too does it ask. A call with *force* set asks whenever the session is
unresolved. The marker is a hint, not a
boundary: the deferral is safe only because the tool body fails closed, see
CONFINED, UNCONFINED AND NO-SESSION CALLERS.

A session whose hook input carries *permission\_mode* *dontAsk* is never
prompted: the hook turns every *ask* it would emit, from this verdict and from
every other PreToolUse path, into a *deny* whose reason is the original reason
followed by " -- denied because this session cannot answer prompts (permission
mode dontAsk)" (an em dash in the output). Allow decisions are unchanged, as is
every other mode or an absent field. The *permission\_mode* values are taken
from Claude Code's hook input and are unverified in this repository.

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

# DEFAULT PERMISSIONS

The hook's verdict per call, for a session on branch _B_ (spinclass#415). The
*unconfined* column is a caller in its own session worktree with no principal
mapping, resolved from *cwd*. The *unit hook* column is the hook inside a unit,
where the session does not resolve: every call without *force* is Allow and
the serve's body check (CONFINED, UNCONFINED AND NO-SESSION CALLERS) is the
boundary. From a *cwd* in no session, every call is Ask. The test
*TestGritDefaultPermissionsForAnUnconfinedSession* (internal/hooks) asserts
every row; keep the two in step.

```
call                                    unconfined  unit hook why
stage status, diff                      allow       allow     read
branch log, rev-parse, diff             allow       allow     read
tag list, verify                        allow       allow     read
worktree list                           allow       allow     read
remote fetch (configured remote)        allow       allow     read
remote fetch (a URL)                    ask         allow     not a remote
stage add, unstage, rm, mv              allow       allow     on B's checkout
stage rm force                          ask         ask       force
commit, commit amend, merge             allow       allow     on B's checkout
stash list, save, apply, drop           allow       allow     B's own shelf
stash with a foreign repo_path          ask         allow     not B's worktree
branch create B--x                      allow       allow     owned name
branch create other, main/master        ask         allow     not owned
branch checkout B--x, paths, new B--x   allow       allow     owned
branch checkout main/master             ask         allow     default branch
branch delete B--x                      allow       allow     owned, exists
branch delete other, main/master        ask         allow     not owned
branch delete force                     ask         ask       force
branch reset-soft, reset-hard           allow       allow     on B's checkout
branch rebase (B onto upstream)         allow       allow     B is rewritten
branch rebase rebase_branch=main        ask         allow     default branch
branch restack root=B                   allow       allow     owned range
branch restack moving a foreign branch  ask         allow     foreign in range
branch cherry-pick, revert              allow       allow     on B's checkout
tag create, delete B--v                 allow       allow     owned tag
tag create, delete v (other names)      ask         allow     not owned
tag create force                        ask         ask       force
tag push B--v                           allow       allow     owned tag
tag push v, tag push delete             ask         allow     not owned/delete
remote pull                             allow       allow     on B's checkout
remote push (B or B--x)                 allow       allow     owned branch
remote push force_with_lease (owned)    allow       allow     owned branch
remote push main/master, remote_branch  ask         allow     default branch
remote push delete                      ask         allow     remote delete
remote push force                       ask         ask       force
remote push-stack (all owned)           allow       allow     owned branches
remote push-stack (any not owned)       ask         allow     not owned
worktree remove (a B--x worktree)       allow       allow     owned worktree
worktree remove (own, main checkout)    ask         allow     not removable
clone into the worktree (.tmp)          allow       allow     inside B's tree
clone anywhere else                     ask         allow     outside the tree
```

Remote talk about the session's own refs is Allow for an unconfined session
(FDR 0036). A stricter policy for confined and profile-gated sessions (no remote
access by default, except rebasing against upstream) is planned but not built;
it would hook in at the body's confined check, *refuseUnownedForConfined* in
*internal/store/git/confined.go*, where the confined column is enforced.

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
	default branch is refused whatever *force* says. The session worktree is
	writable from the unit, so every git run in it names the repository
	explicitly from session state (*GIT_DIR*, *GIT_COMMON_DIR*,
	*GIT_WORK_TREE*), drops the system config, overrides the config keys that
	run a command (*core.fsmonitor*, *core.sshCommand*, *core.pager*,
	*core.editor*, *diff.external*, *protocol.ext.allow*,
	*core.attributesFile*) and resets *credential.helper* to the one spinclass
	injected. Per-name drivers (filter, textconv, merge drivers, aliases and
	URL-specific credential helpers) cannot be unset by an override; the unit cannot write the
	config that would define them (FDR 0035). Raw *git commit*, *checkout -b*
	and *update-ref* fail inside a confined unit by design (its refs are
	read-only): use these tools.

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

For every caller, read or write, the body also refuses a call whose
*repo\_path* resolves into another spinclass session's worktree unless the
caller holds an accepted handle on that session (FDR 0032). The path is
resolved from the filesystem alone (symlinks, then the *.git* pointer's layout)
before any git runs, and an unresolvable path or a pointer of unknown layout is
refused. The refusal names the target session and the handles held. Plain
repositories and main checkouts are unaffected. *sc grit* is exempt. The
PreToolUse hook and *sc grit-perms* apply the same check before classifying, so
such a call asks rather than allows, and no git runs in that worktree to decide.

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

A merge (*merge-this-session*, its *-async* twin and *sc merge*) uses this
shelf for its own autostash (spinclass#416), on by default: uncommitted tracked
changes become an entry labelled _spinclass merge autostash (#416)_ before the
rebase, and that entry, by its number, is applied and dropped right after the
rebase and repair. Untracked files are left in place, since they do not block
the rebase. A conflicting restore keeps the entry, leaves the conflict in the
worktree, and the merge result names the entry; resolve the conflict, then
*grit\_stash drop* it. *autostash: false* (*sc merge --no-autostash*) restores
the refusal of a dirty tree.

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
  *force* flag ask. (FDR 0036 agrees: plain *force* asks.)
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
- An unconfined caller whose hook *cwd* is in no session worktree (and whose
  principal has no mapping) is asked about every grit call (denied under
  *dontAsk*). *CLOWN\_UNIT* is a hint, not a
  boundary: a process that sets it on itself gets the body refusal of writes
  it does not own instead of the prompt. A clown that predates the marker sets
  none in its units, so their hooks ask (or deny) too.
- An unconfined worker can still run raw *git push* with its session
  credential; only the confined unit lost that (read-only refs, inaccessible
  credential). *nix flake* evaluation of git inputs and direnv hooks in a
  confined unit are unverified against read-only refs, and so are systemd's
  handling of nested read-only file binds and *InaccessiblePaths=* inside a
  read-write bind (FDR 0035, "The git view").
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
