---
status: active
date: 2026-09-30
promotion-criteria: |
  active -> realized: a certified child agent has acted on a signed operator
  quote without re-asking the operator, and a merge gate has refused a merge
  because a required signed attestation was missing or unverifiable.
children:
  - spinclass FDR 0032, docs/features/0032-principals-handles-and-provenance.md
  - troupe#39 (identity, certificates, signed-record grammar, transcript)
  - piggy#297 (key holders and tiers)
  - papi#87 (hardware-key slot enrollment and attestation)
  - clown#244 (scopes, the per-turn signing hook, parent address)
  - clown#245 (the subagent platform)
  - ringmaster#25 (lifecycle event records)
  - ringmaster#26 (scheduling and contention)
  - smith#68 (the version-control broker)
  - posh#224 (observer attach primitive)
  - spinclass#347 (observer attach consumer)
  - spinclass#338 (session confinement)
  - spinclass#339 (security review)
  - spinclass#337 (mechanical gates)
  - moxy#443 (tools declare the rights they enforce)
  - purse-first#194 (framework support for declared rights)
  - circus (remote operator surface)
---

# Trustworthy delegation between agents

> **Active.** The first epic: a vision-level record written for a reader who has
> never seen this ecosystem. It says what agent orchestration should become and
> why. The child records in the frontmatter, mapped in the closing appendix, are
> the design work that implements it. The body deliberately names no products; it
> says "the session manager", "the messaging layer", "the hardware key". Only the
> appendix names components, so the vision stays readable as they change.

## The problem

An **agent** here means a language-model process that reads instructions, calls
tools, and produces work: code, reviews, research. Agents are now useful enough
that one delegates to another. A coordinator spawns workers. A worker spawns a
helper. The result is a tree of agents, several deep, doing real work on a real
machine, mostly while the human who started it is not watching.

That tree has no foundation. Five gaps, each load-bearing.

**Nobody can prove who asked for what.** When one agent instructs another,
nothing is attached to the instruction saying where it came from. The receiving
agent gets text. Text is text.

**Identity is inferred from location.** An agent's name is derived from where its
process happens to be running: which directory, which checked-out copy of a
repository. A process id is not computed from a process's current directory, but
here it is, which means:

- An agent running somewhere unremarkable (a home directory, say) has no identity
  at all, and operations that need only a reply address fail outright.
- Two agents in the same place are the same agent, as far as the system can tell.
- Moving work changes who you are.

**Authority is a string comparison.** When an agent asks to shut down another,
the check is whether the target's "spawned by" field equals the caller's name.
Authority is therefore identical to parentage, so it cannot be:

- **delegated**, because there is no thing to hand over,
- **shared**, because the field holds one value,
- **revoked**, because the field is a historical fact,
- **narrowed**, because it is all or nothing.

A coordinator about to exit cannot hand its fleet to a successor. Its workers
simply become unownable.

**A worker cannot tell an order from a rumour.** A worker reads three kinds of
text through one channel: instructions from its coordinator, decisions the human
operator actually made, and content fetched from the outside world (an issue
description, a web page, a file, a tool's output). Nothing distinguishes them, so
the worker has two choices and both are bad:

- Trust everything. Any text it reads can now issue commands to it, so a
  malicious sentence in a bug report becomes an order. This is prompt injection,
  and in a tree it propagates: one poisoned worker relays poison to its siblings
  under the appearance of coordination.
- Trust nothing. It now stops to ask the human about decisions the human already
  made, out loud, ten minutes ago. Delegation stops being delegation.

**Nothing bounds cost, and nothing records history.** A fan-out of workers is a
fan-out of expensive builds and model calls with no ceiling. And the record of
who did the work is a free-text author line any process can write, so no third
party, the operator tomorrow included, can verify which agent produced a change,
under whose authority, from which conversation.

These are not five problems. They are one missing layer, showing up five times.

## The vision

Treat a fleet of agents the way an operating system treats processes.

An operating system solved this decades ago, for programs rather than agents. It
keeps three questions apart and answers each once:

- **Who is this?** A process has an identity assigned by the kernel, not by the
  process itself.
- **On whose behalf does it run?** A process runs as a user, and the system knows
  how that user was authenticated.
- **What may it do?** A process holds a set of privileges, and separately holds
  handles on specific objects it was given access to.

Three layers, one job each. The same three layers, for agents.

### Principal: who

Every agent instance gets a **principal**: an identity minted by the platform that
runs it.

- Unique and random. Not derived from a directory, a repository, a branch, or
  anything else about where the agent happens to be running.
- Minted by the platform, not claimed by the agent, the same way a process does
  not choose its own process id.
- Lasting exactly as long as that agent instance.

That alone fixes the agent with no identity, because a random identity is
available anywhere.

### Certificate: who vouched

A principal on its own is just a random number, and anyone could claim one. So
each principal's public key is **certified**.

A **certificate** here is a small signed record saying: this principal has this
public key, and I, the issuer, vouch for it. The issuer is itself a principal with
its own certificate, and so on upward. The chain ends at a **hardware key**: a
physical device the human operator carries, holding a private key that cannot be
copied off it and requiring a deliberate human touch to sign anything.

The chain is built as the tree grows:

- When a human starts a session, the hardware key certifies that session's
  principal. This is the one touch, at the top.
- When an agent spawns a worker, the spawner certifies the worker with an ordinary
  software key that was itself certified under the root.

So verifying a chain answers a question nobody can answer today: **which human
ultimately authorized this agent, and through whom.** Walk upward from the agent
and you get the whole delegation path, ending at a person who touched a device.

Certificates carry identity only, never permissions. That separation matters:
identity is stable and worth caching, while permissions change often and must be
checked at the moment of use.

### Rights: what

A **right** is a permission to do one kind of thing. Rights come in two carriers,
and the distinction is the one an operating system draws.

**Ambient rights** live on the principal and say what the agent may do at all,
regardless of target. Every right to execute code is here: build, run tests, run a
merge (which runs arbitrary project code in order to gate it), load a new tool or
plugin (which is arbitrary code by another name). An operating system calls this a
capability set: the list of privileged operations a process may perform, checked
whenever it tries one.

**Handles** are rights on a particular object. The object that matters here is
another agent's session. Holding a handle lets you do specific things to that one
session:

- **observe**: see that it exists, be told when it exits, watch its live output,
- **close**: shut it down,
- **grant**: pass the handle to somebody else,
- **instruct**: have your messages treated as orders rather than as data,
- **input**: send keystrokes into its live terminal.

An operating system calls this a file descriptor: a token a process holds that
refers to one specific open object, conveying exactly the access it was opened
with and nothing about any other object.

Both carriers obey one rule, and the whole design rests on it:

> **Rights only shrink, unless a human says otherwise.**

An agent may drop a right, release a handle, or hand a child a subset of what it
holds. It may never hand out more than it holds, and never grant itself more. Any
step upward is an **escalation**: an explicit request the human answers by touching
the hardware key. Mathematicians call a structure of this shape a lattice; the
useful part is that authority flows downward and narrows, and the only way back up
passes through a person.

Note what this makes impossible. A worker spawned without the right to run code
cannot run code. Not "is instructed not to". Cannot. Whatever its brief says,
whatever text it later reads, whatever it concludes it ought to do, the tool
refuses because the right is not in its set. Prompt injection stops being a
question of persuasion and becomes a question of permissions.

### The operating-system mapping

The design borrows from operating systems deliberately, so the correspondence is
worth stating plainly. Each row names the concept, explains it, and gives the
agent-world equivalent.

| Operating system | What it is | Here |
|---|---|---|
| process | a running program | an agent instance |
| process id | the identity the kernel assigns it | the agent's principal |
| process credentials | which user it runs as, and how that was authenticated | the certificate chain up to a human |
| capability set | the privileged operations it may perform | the agent's ambient rights |
| file descriptor | a token referring to one open object, with specific access | a handle on one agent's session |
| fork | a process creating a child | spawning a worker |
| child-exited signal | the notice a parent gets when a child ends | the exit notification every handle holder gets |
| resource limits on a group of processes | caps on memory, processor time, and count | a tree's budgets |
| the scheduler | decides what runs when | the job platform |
| terminal | where a process's input and output meet a human | a frontend |
| the log a process writes | the durable record of what it did | the agent's transcript |

## What becomes possible

Each of these is blocked today by exactly one of the gaps above.

**A coordinator hands off its fleet before exiting.** A coordinator finishing its
work passes handles on its workers to a successor. The workers keep running, now
owned by somebody still alive. This is how a program on a Unix system passes an
open file to another program: it sends the file descriptor over a socket, and the
receiver ends up with real access it never opened itself. As there, the sender
keeps its own copy unless it releases it, and the recipient acquires nothing until
it first uses the handle, so an agent is never saddled with responsibility it did
not act on.

**A worker acts on the operator's own words without asking again.** When the
operator types an instruction, whatever observes the keystrokes signs it at that
moment, producing a small signed record of what was actually said. A coordinator
relaying that decision attaches a **quote**: a reference to that record. The
worker retrieves it, checks the signature, and acts within the quoted words. The
quote cannot be forged, because it is fetched by content address rather than
retyped, so no agent is ever in a position to choose the operator's words.

**A research worker cannot land code.** A worker spawned to read and summarize
gets ambient rights covering reading and nothing else: no build right, no merge
right, no tool-loading right. An attacker may rewrite its brief and it still
cannot change the repository, because the capability is absent rather than
discouraged.

**A merge gate demands evidence instead of a claim.** Today a gate asks the
merging agent whether it reviewed its own work, and the agent says yes. Instead
the gate requires a signed record, and checks two things mechanically: that the
reviewing principal is distinct from the merging one and chains to this same human
session, and that the reviewed content matches the content being merged, by
digest. A merge with no such record is refused, so self-attestation stops being
possible rather than merely discouraged.

**A second agent watches or helps, by permission.** A helper agent attaches to a
live session: read-only if it holds the observe right, able to type if it holds
the input right, which is never granted by default and takes an escalation when
the requester lacks it. The session's owner decides which, and sees how many
observers are attached. Pair programming between agents becomes a permission
rather than a backdoor.

**The operator approves from a phone.** Away from the keyboard, the operator
renews a tree's lease or answers an escalation from a phone. Each operation
declares a minimum assurance: renewing a lease accepts a message from an enrolled
account, shutting down a worker with unsaved work requires an enrolled device, and
widening what a whole tree may do requires the hardware key itself. The common
case is convenient and the dangerous case is not.

**History answers who, under whose authority, from which conversation.** Every
change an agent commits is signed by that agent's key and references the signed
transcript span that produced it. Months later a reviewer can walk from a line of
code to the agent that wrote it, to the chain of agents that authorized it, to the
human at the root, to the conversation where the decision was made. No trust in
the record-keeper is required, because every step is a signature.

## What this guarantees, and what it does not

A design of this kind is easy to oversell. Stated plainly:

**It governs the sanctioned tool surface.** An agent cannot use a tool to do
something outside its ambient rights, cannot act on a session it holds no handle
for, and cannot forge who said what. That defeats the realistic threat: an agent
misled into misusing the authority it legitimately has, or into relaying
someone's injected instructions as if they were orders.

**It does not govern the operating-system user.** These agents all run as the same
user account. An agent that can write an arbitrary file can write a shell startup
file, a hook, or a build script something else will later execute, and thereby
escape every right at once. A read-only tool with an exploitable parser is the
same escape with more steps. Until each agent runs inside a **confined unit** (a
sandbox with its own restricted view of the filesystem and its own user), these
rights are a policy the tools enforce, not a wall the agent lives behind. That
confinement is a child record of this epic, and until it lands nothing here should
be described as containment.

**Non-repudiation is the goal, so deniability is not.** Some secure messaging
systems, the Signal protocol most famously, deliberately make messages *deniable*:
afterwards nobody can prove who sent what, which protects people from having their
conversations used against them. That is the opposite of what is wanted here. A
provenance record exists precisely so an agent cannot later deny what it did. The
two goals are incompatible and this design chooses accountability. Confidentiality
is layered on later and separately: records are signed first and may be encrypted
afterward, because those compose cleanly in that order and badly in the other.

**Rewinds and forks of a conversation are first-class.** Conversations do not run
in a straight line: a human rewinds and tries again, a conversation splits in two.
So the transcript is a graph rather than a chain, where each entry names the
entries it follows. A rewind adds an entry pointing at an older one, and the
abandoned branch stays valid history rather than being erased.

**A model change mid-conversation is recorded, and its consequences are open.** The
underlying model can change while a conversation runs, because the operator
switched it or a routing layer substituted one. Every transcript entry therefore
records what produced it, and a switch is marked explicitly. What that means for
authority is unsettled. The principal is the agent and not the model, so it carries
across; whether the permission set should carry across unchanged when the new model
is weaker is left open rather than guessed.

## Trust, time, and cost

**One root of trust, touched once.** The hardware key signs one thing per tree: the
certificate of the session the human started. Everything below is signed by software
keys certified under it. The human touches the device once, not once per agent and
never once per message. Touch fatigue is itself a security problem, because it is
what drives people to cache and then to disable.

**One clock per tree.** Only the root certificate expires, and a certificate below
it is valid only while its parent is, so the whole tree shares one deadline and
renewal is one touch at the root. There is no cascade where a worker's extension
needs its parent's, which needs its parent's, up to a human who is asleep. The
consequence is intentional: when the root's lease expires and nobody renews it, the
tree stops. An unattended fleet should not run forever because a middle layer kept
signing for itself. A tree can instead be adopted by a new human session, the move
an operating system makes when it reparents an orphaned process.

**Budgets are rights.** How much a tree may consume is governed by the same
machinery as what it may do: money spent on model calls, workers spawned,
concurrent builds, live sessions. Each is inherited at spawn as a subset of the
parent's allowance, can only shrink on its own, and is raised only by the same
human-touched escalation as any other right. Operating systems keep "whether" and
"how much" in separate subsystems that compose, because both are inherited the
same way. The same split applies here.

**Scheduling is a separate concern, deliberately.** Deciding *when* work runs
(queues, priorities, back-pressure, batching several approval prompts into one
touch) belongs to the job platform, not to this design. Coupling "who may" to
"when" is exactly the mistake that capability systems exist to avoid.

## How it arrives

In slices. Each is useful on its own and tightens the previous one rather than
replacing it.

- **Slice 0: identities and handles, no cryptography.** Every agent gets a real
  principal, and session ownership becomes a handle that can be granted, released
  and counted. This alone fixes the two concrete bugs that motivated the whole
  design: an agent in an ordinary directory can be addressed, and a coordinator can
  hand its fleet to a successor. Authority here is unguessable but not unforgeable,
  and the design record says so rather than implying more.
- **Slice 1: keys and certificates.** Each principal gets a keypair, the chain is
  built up to the hardware key with one touch per human-started session, and
  ownership checks additionally require a valid chain.
- **Slice 2: a signed transcript of the harness in use today.** The existing
  per-turn hook, which already watches the conversation log, becomes a signer: each
  turn produces a signed record over the new content, linked to the previous one.
  This exercises the transcript graph on the tooling actually in use, before any of
  it is replaced.
- **Slice 3: a subagent platform of our own.** Workers under a certified principal,
  with a transcript that is ours end to end and coordinator instructions signed
  under the human's chain. The first place the whole model runs without borrowed
  pieces, and exactly the boundary where injection crosses between agents.
- **Slice 4: the frontends.** The places a human meets an agent (a terminal, a chat
  client, a phone) become interchangeable views onto one certified agent.

Beyond the slices, the intended longer arc:

- **One model runtime backs every agent.** Instead of each agent being a separate
  program with its own conventions, a single runtime mints principals and hosts
  agents, and everything else talks to it.
- **Today's agent harness becomes one frontend among several.** The terminal
  program currently treated as the whole system becomes one way in.
- **The agent's own messaging archive becomes its transcript.** The durable,
  verifiable record of what an agent did is the signed history of its own
  conversations, stored where those conversations already live, rather than a log
  file owned by whichever tool happens to be running.

## Value and impact

**For the operator.**

- Delegate without babysitting: a worker tells an order from a rumour and stops
  asking about decisions already made.
- Audit without trusting: every claim about who did what is a signature rather
  than an assertion.
- Bound cost: spend and fan-out are inherited limits rather than hopes.
- Approve remotely: assurance levels make the common case easy and the dangerous
  case hard.

**For an agent.**

- Know whom to obey, and therefore stop asking.
- Know what it may do, and stop guessing at the edges of its mandate.
- Be unable to act outside its rights, rather than being asked to resist.

**For the tools.**

- One verifier, because every signed thing shares one record shape.
- One record grammar: certificates, grants, approvals, exits, operator input and
  transcript entries are all instances of the same object.
- One lattice, because per-tool permission lists and system capabilities stop
  being two overlapping mechanisms with two answers.

## Where this lives today

This appendix is the only place this document names components. Each row maps a
term from the vision onto the thing that realizes it and the child record that
tracks it. The umbrella design record every row hangs off is **spinclass FDR
0032** (`docs/features/0032-principals-handles-and-provenance.md`), which
specifies the decisions in full for readers inside the ecosystem.

| Vision term | Component today | Child record |
|---|---|---|
| principal | the harness's per-instance session id (clown) today; the model runtime (juggler) later | spinclass FDR 0032 (D1); clown#245 |
| certificate, record grammar, transcript | troupe, the messaging layer, which federates and so can serve certificates across hosts | troupe#39 |
| keys and holder tiers | piggy, with the hardware-key slot enrolled by papi | piggy#297; papi#87 |
| objects, handles, exit notifications | spinclass, the session manager (slice 0 has landed) | spinclass FDR 0032, slice 0 |
| event log and scheduling | ringmaster, the job platform | ringmaster#25; ringmaster#26 |
| version-control broker | smith, the forge client, pushing on a session's behalf | smith#68 |
| observer attach | posh, the terminal multiplexer, with spinclass as consumer | posh#224; spinclass#347 |
| confinement | spinclass, clown and the model runtime together | spinclass#338 |
| security review | spinclass | spinclass#339 |
| mechanical gates | spinclass, revising its attestation and exemption records | spinclass#337 |
| rights declarations by tools | moxy and purse-first | moxy#443; purse-first#194 |
| remote operator surface | circus, over federated chat | circus |
| scopes, per-turn signing hook, parent address | clown, the agent harness | clown#244 |
