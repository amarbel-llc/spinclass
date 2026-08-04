package clown

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/ringmaster/pkgs/jobwake"
)

// These tests drive the REAL ringmaster binary rather than the shell stubs the
// rest of this package uses (spinclass#253).
//
// The stub tests answer "did we build the argv we meant to?". They cannot
// answer "does ringmaster accept it?" — a stub agrees with whatever we send.
// Every ringmaster contract spinclass depends on has been unverified in that
// exact way since FDR 0010 landed: the platform was reachable only from PATH,
// no sandboxed lane had it, so nothing could exercise it. ringmaster is now an
// extracted, pinnable flake, wired here as a checkPhase input, which makes the
// contract testable for the first time.
//
// The suite skips when no binary is reachable, so a devshell `go test` without
// the pin still passes.

// realRingmaster resolves the actual ringmaster binary and points the package's
// own resolution at it, or skips.
//
// It also redirects ringmaster's entire on-disk world into t.TempDir(): the
// journal (XDG_STATE_HOME), the nudge socket (XDG_RUNTIME_DIR), and the session
// key that names the channel. That isolation is not incidental tidiness — these
// tests routinely run on a developer machine that IS an agent session, and
// without it a `done` emit would deliver a real wake to whoever is sitting in
// front of it.
func realRingmaster(t *testing.T) string {
	t.Helper()

	bin, err := exec.LookPath("ringmaster")
	if err != nil {
		// Inside the nix sandbox the binary is guaranteed by the flake's
		// nativeCheckInputs, so its absence is a wiring regression, not an
		// environment we should tolerate. Skipping there would restore exactly
		// the failure mode this suite exists to end: coverage silently at zero
		// while the lane still reports green. NIX_BUILD_TOP is the repo's
		// established sandbox probe (see flake.nix's checkPhase note).
		if os.Getenv("NIX_BUILD_TOP") != "" {
			t.Fatal("ringmaster is missing from PATH inside the nix sandbox; the " +
				"checkPhase pin has been dropped and the ringmaster contract is " +
				"no longer covered")
		}
		t.Skip("no ringmaster on PATH; the contract suite needs the real binary " +
			"(nix provides it via nativeCheckInputs)")
	}

	root := t.TempDir()
	state := filepath.Join(root, "state")
	run := filepath.Join(root, "run")
	for _, d := range []string{state, run} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	t.Setenv("RINGMASTER_BIN", bin)
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_RUNTIME_DIR", run)
	t.Setenv("CLOWN_SESSION_ID", "spinclass-contract-test")
	// A stray CLOWN_DISABLE_JOB_WAKEUP in the ambient environment would turn
	// every emit into an exit-0 no-op and quietly hollow out the assertions.
	t.Setenv("CLOWN_DISABLE_JOB_WAKEUP", "")

	return state
}

// ringmasterOut runs the real binary with the test's isolated env and returns
// its stdout, for reading the journal back independently of the code under test.
func ringmasterOut(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("ringmaster", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ringmaster %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// The full lifecycle spinclass drives for an async job: allocate, resolve the
// spool, stream output into it, finish. Asserted end to end against the real
// journal, because each step's failure mode is invisible in isolation — a
// spool path we never write to looks identical to one ringmaster rejected.
func TestRingmasterJobLifecycleContract(t *testing.T) {
	state := realRingmaster(t)
	ctx := context.Background()

	id, err := StartJob(ctx, "merge", Source)
	if err != nil {
		t.Fatalf("StartJob against real ringmaster: %v", err)
	}
	if id == "" {
		t.Fatal("StartJob returned an empty id")
	}

	// The id must carry the label as its prefix. This is the property that
	// made spinclass#243 confusing rather than merely wrong: ringmaster mints
	// `<label>-<hash>` while the old local scheme minted `<label>-<unix-ts>`,
	// so the two rhymed closely enough that an agent read its own wake as a
	// sibling session's. Pinning the shape here means a change to it fails
	// loudly instead of resurrecting that ambiguity.
	if !strings.HasPrefix(id, "merge-") {
		t.Errorf("job id %q does not start with its label; the wake would no "+
			"longer be attributable to the dispatch that created it", id)
	}

	spool, err := SpoolPath(ctx, id)
	if err != nil {
		t.Fatalf("SpoolPath against real ringmaster: %v", err)
	}
	// The spool must live inside the journal we redirected. If it does not,
	// the isolation above is not actually holding and these tests are writing
	// into the developer's real job state.
	if !strings.HasPrefix(spool, state) {
		t.Fatalf("spool %q escaped the test journal root %q", spool, state)
	}

	// runner.go tees the pre-merge hook's output here so `ringmaster status
	// --tail` can show a running job instead of the empty spool it reported
	// before (#251). Write, then read it back through ringmaster's own surface.
	if err := os.WriteFile(spool, []byte("hook line one\nhook line two\n"), 0o644); err != nil {
		t.Fatalf("write spool: %v", err)
	}
	status := ringmasterOut(t, "status", id, "--tail", "5")
	if !strings.Contains(status, "hook line two") {
		t.Errorf("spooled output did not surface in `ringmaster status --tail`;\n"+
			"the tee writes somewhere ringmaster does not read.\ngot:\n%s", status)
	}

	// Terminal emit with nothing listening. This is the sandbox's condition
	// and the common one in production too — a wake with no monitor bound
	// must not be an error, or every job would report a spurious failure.
	if err := FinishJob(ctx, id, "succeeded", "merge succeeded", "spinclass session-job-status"); err != nil {
		t.Fatalf("FinishJob with no monitor listening: %v", err)
	}

	records := ringmasterOut(t, "read", "--job", id)
	for _, want := range []string{"started", "succeeded", "merge succeeded"} {
		if !strings.Contains(records, want) {
			t.Errorf("journal missing %q after the full lifecycle;\ngot:\n%s", want, records)
		}
	}
}

// A terminal record can carry its result by reference (#251 piece 2b). Before
// that, spinclass's only pointer was `result_ref: "spinclass
// session-job-status"` — a wake telling the reader to call another tool.
//
// Worth driving against the real binary rather than a stub for the specific
// reason that a stub cannot reject a flag: `--resource` is repeatable and
// takes a URI, and the only way to know ringmaster parses our argv the way we
// think is to hand it to ringmaster and read the journal back.
func TestRingmasterDoneAttachesResources(t *testing.T) {
	realRingmaster(t)
	ctx := context.Background()

	id, err := StartJob(ctx, "merge", Source)
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}

	// Two attachments, since the flag is documented as repeatable and
	// spinclass may grow a second one (hook output alongside the verdict
	// ladder). Digests here are synthetic — ringmaster records the URI and
	// does not resolve it, so no blob store is needed.
	first := "madder://blobs/blake2b256-aaaa"
	second := "madder://blobs/blake2b256-bbbb"
	if err := FinishJob(ctx, id, "succeeded", "merge succeeded", "", first, second); err != nil {
		t.Fatalf("FinishJob with resources: %v", err)
	}

	// --json is required to see the URIs at all: plain `read` renders the
	// notification-line form, which summarizes attachments as a count
	// ("· 2 resource(s)") rather than listing them. Worth knowing for #251 —
	// an agent reading only the wake line learns that a result is attached
	// and how many, but must go to the JSON to fetch one.
	records := ringmasterOut(t, "read", "--job", id, "--json")
	for _, want := range []string{first, second} {
		if !strings.Contains(records, want) {
			t.Errorf("journal did not carry attached resource %q;\ngot:\n%s", want, records)
		}
	}
}

// Empty resource entries must be skipped rather than passed through as an
// empty --resource value. runner.go relies on this: when no madder pin exists
// there is no blob, and it passes "" instead of branching at the call site. If
// that reached ringmaster as a flag with an empty argument it could be a usage
// error, turning a routine degrade into a lost wake.
func TestRingmasterDoneSkipsEmptyResources(t *testing.T) {
	realRingmaster(t)
	ctx := context.Background()

	id, err := StartJob(ctx, "check", Source)
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	if err := FinishJob(ctx, id, "succeeded", "check succeeded", "spinclass session-job-status", ""); err != nil {
		t.Fatalf("FinishJob with an empty resource: %v", err)
	}

	records := ringmasterOut(t, "read", "--job", id)
	if !strings.Contains(records, "succeeded") {
		t.Errorf("terminal record missing after an empty-resource emit;\ngot:\n%s", records)
	}
}

// spool-path is a pure path computation, NOT a lookup: ringmaster(1) documents
// that it creates the channel directory but leaves the spool file to the
// producer, and only a *malformed* job-id is an error (exit 2). An id for a job
// that never existed is well-formed, so it is answered normally — the first
// version of this test asserted the opposite and the real binary corrected it.
//
// What matters to spinclass is therefore not existence-checking but that a
// genuine failure is reported rather than swallowed: runner.go degrades to
// "job.log alone" on any SpoolPath error, which is only safe if errors surface.
// A path-separator id is the documented malformed case and doubles as a
// traversal guard — the answer must never be a path outside the channel dir.
func TestRingmasterSpoolPathRejectsMalformedJobID(t *testing.T) {
	realRingmaster(t)

	path, err := SpoolPath(context.Background(), "../../escape")
	if err == nil {
		t.Fatalf("SpoolPath accepted a path-separator job id, answering %q; the "+
			"tee would write outside the channel directory", path)
	}
	// The wrapper must name the subcommand, so a failure recorded in the job
	// log is attributable without re-deriving which call broke.
	if !strings.Contains(err.Error(), "spool-path") {
		t.Errorf("error %q does not name the failing subcommand", err)
	}
}

// The core #26 contract: the jobwake ProtocolVersion this binary LINKED at
// build time must equal the integer the checkPhase-pinned ringmaster prints for
// `version --protocol`. Both come from the same flake input, so they SHOULD
// match — this proves it, and fails loudly if a future ringmaster bump moves
// the runtime protocol without the linked lib following (or vice versa), which
// is exactly the skew the serve-start gate degrades on in production. A stub
// cannot cover this: it would print whatever we tell it, agreeing with the
// linked const by construction.
func TestCheckProtocolMatchesRealRingmaster(t *testing.T) {
	realRingmaster(t)
	resetProtocolCheck()

	ok, want, got, err := CheckProtocol(context.Background())
	if err != nil {
		t.Fatalf("CheckProtocol against real ringmaster: %v", err)
	}
	if !ok {
		t.Errorf("linked jobwake ProtocolVersion=%d != `ringmaster version --protocol`=%d; "+
			"the build-time pin has drifted from the pinned binary", want, got)
	}
	if want != jobwake.ProtocolVersion {
		t.Errorf("want=%d, expected the linked const %d", want, jobwake.ProtocolVersion)
	}
}

// The #22 observer contract: WaitForCancel must report true when ringmaster has
// recorded a cancel-requested. This is the exact live behavior a stub cannot
// prove — `ringmaster wait --on-cancel` reports the DERIVED state "running" for
// a cancel-requested job (the record is non-terminal), never "cancel-requested",
// and WaitForCancel maps a non-terminal return to cancelRequested=true. A stub
// would just echo whatever we told it; only the real binary confirms the
// derived-state quirk this depends on.
func TestWaitForCancelObservesRealCancelRequested(t *testing.T) {
	realRingmaster(t)
	ctx := context.Background()

	id, err := StartJob(ctx, "obs", Source)
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	// `ringmaster cancel` records the non-terminal cancel-requested — what an
	// external canceller does; the producer is expected to observe and abort.
	ringmasterOut(t, "cancel", id)

	cancelRequested, err := WaitForCancel(ctx, id)
	if err != nil {
		t.Fatalf("WaitForCancel: %v", err)
	}
	if !cancelRequested {
		t.Error("WaitForCancel returned false for a cancel-requested job; the observer " +
			"would never tear down the hook")
	}
}

// The terminal counterpart: WaitForCancel must report false when the job ended
// on its own, so the observer no-ops instead of firing a spurious cancel.
func TestWaitForCancelReturnsFalseForTerminal(t *testing.T) {
	realRingmaster(t)
	ctx := context.Background()

	id, err := StartJob(ctx, "obs", Source)
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	if err := FinishJob(ctx, id, "succeeded", "done", ""); err != nil {
		t.Fatalf("FinishJob: %v", err)
	}

	cancelRequested, err := WaitForCancel(ctx, id)
	if err != nil {
		t.Fatalf("WaitForCancel: %v", err)
	}
	if cancelRequested {
		t.Error("WaitForCancel returned true for a succeeded (terminal) job; the observer " +
			"would fire a spurious cancel")
	}
}
