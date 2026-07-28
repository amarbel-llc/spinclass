package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	osexec "os/exec"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/embeds"
	"code.linenisgreat.com/spinclass/internal/repoinfo"
)

// halfBriefed is the refusal every --issue failure carries. Prepending an
// issue is not decoration: the worker's brief is its ONLY context, so a
// brief that silently lost the issue text produces a worker confidently
// working on nothing. Every path in this file is therefore a hard error
// rather than a degrade-to-empty.
const halfBriefed = "refusing to spawn a half-briefed worker"

// pasteHint is the actionable escape hatch offered on every refusal: the
// user always has a way to get the issue text into the brief, even when
// spinclass can't reach (or can't identify) the forge itself.
const pasteHint = "pass the full issue URL (https://<host>/<owner>/<repo>/issues/<n>), " +
	"or paste the issue body into the brief instead"

// issueForgeResolveTimeout bounds the forge-kind resolution for a bare
// `--issue N` (internal/repoinfo, which shells out to papi and may probe the
// forge). repoinfo is best-effort by construction — every failure degrades to
// an empty field rather than an error — so the only thing a deadline buys is
// the guarantee that a wedged papi or an unreachable identity domain becomes
// a prompt "could not resolve a supported forge" refusal instead of a spawn
// that hangs the driver. The issue fetch itself is deliberately left
// unbounded, as it always has been: that is the work the user asked for, not
// a lookup, and capping it would invent a policy nobody asked for.
const issueForgeResolveTimeout = 5 * time.Second

// issueSource is the fully-resolved address of the issue `--issue` names:
// which forge family it lives on, plus the coordinates needed to fetch it
// from that forge. It comes from one of two places — a full issue URL
// (host/owner/repo/number read straight out of the URL, so the issue need
// not live on the target repo's own forge at all) or, for a bare number,
// the TARGET repo's resolved forge identity (internal/repoinfo).
type issueSource struct {
	forgeKind string // papi enum: github|gitea|gitlab|codeberg|forgejo|bare
	host      string
	owner     string
	name      string
	number    string
	// fromURL records that owner/name were read from an explicit URL rather
	// than inferred from the target repo. It decides whether the gh path
	// pins `--repo`: a bare number keeps the historical argv and lets gh
	// resolve the repo from the working directory, exactly as before.
	fromURL bool
}

// issueDeps are the external seams the --issue flow depends on, injected
// (mirroring internal/repoinfo's deps struct) so the URL parsing, the forge
// dispatch, and the argv each forge gets are all unit-testable without a
// real gh, fj, forge, or network.
type issueDeps struct {
	repoInfo func(ctx context.Context, repoPath string) repoinfo.RepoInfo
	run      func(ctx context.Context, dir, name string, args ...string) (string, error)
	lookPath func(file string) (string, error)
	ghBin    string
}

// prependIssueToBrief fetches the issue `--issue` names and prepends
// "<title>\n\n<body>\n\n---\n\n" to the brief. The issue may be a bare
// number (resolved against the TARGET repo's own forge) or a full issue URL
// on any forge. Any failure — unknown forge, missing client binary, API
// error, bad JSON — is a hard error; see halfBriefed.
func prependIssueToBrief(ctx context.Context, repoPath, issue, brief string) (string, error) {
	return prependIssueToBriefWith(ctx, repoPath, issue, brief, issueDeps{
		repoInfo: repoinfo.Fetch,
		run:      runIssueClient,
		lookPath: osexec.LookPath,
		ghBin:    issueBinOr(embeds.GhBin(), "gh"),
	})
}

func prependIssueToBriefWith(ctx context.Context, repoPath, issue, brief string, d issueDeps) (string, error) {
	src, err := resolveIssueSource(ctx, repoPath, issue, d)
	if err != nil {
		return "", err
	}
	title, body, err := fetchIssue(ctx, src, repoPath, d)
	if err != nil {
		return "", err
	}
	return title + "\n\n" + body + "\n\n---\n\n" + brief, nil
}

// resolveIssueSource turns the raw --issue value into forge coordinates.
// A value carrying a scheme is required to be a well-formed issue URL — it
// is never quietly retried as a bare number, because a URL we can't read (a
// pull-request link, a GitLab /-/issues/ path) means the user asked for
// something specific that we would otherwise go fetch from the wrong place.
func resolveIssueSource(ctx context.Context, repoPath, issue string, d issueDeps) (issueSource, error) {
	issue = strings.TrimSpace(issue)
	if strings.Contains(issue, "://") {
		return parseIssueURL(issue)
	}

	number, ok := issueNumber(issue)
	if !ok {
		return issueSource{}, fmt.Errorf(
			"--issue %q is neither an issue number nor a full issue URL (%s): %s",
			issue, halfBriefed, pasteHint,
		)
	}

	// A bare number is only meaningful relative to a repo, so the forge is
	// the TARGET repo's own — resolved through the same path the dynamic
	// system prompt uses (internal/repoinfo: git remote for host/owner/name,
	// papi for the forge kind), rather than a second remote parser here.
	resolveCtx, cancel := context.WithTimeout(ctx, issueForgeResolveTimeout)
	defer cancel()
	info := d.repoInfo(resolveCtx, repoPath)
	return issueSource{
		forgeKind: info.ForgeKind,
		host:      info.Host,
		owner:     info.Owner,
		name:      info.Name,
		number:    number,
	}, nil
}

// parseIssueURL reads forge coordinates out of a full issue URL. GitHub,
// Gitea and Forgejo all address an issue as
// https://<host>/<owner>/<repo>/issues/<number>, so a single shape covers
// every forge whose API we can actually speak. Exactly four path segments
// are required: that rejects a pull-request link, a bare repo link, and
// GitLab's /<group>/<repo>/-/issues/<n> form (which we could not fetch
// anyway) with a message naming the shape we wanted, instead of silently
// mis-splitting them into an owner and repo that don't exist.
func parseIssueURL(raw string) (issueSource, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return issueSource{}, fmt.Errorf(
			"--issue %q looks like a URL but does not parse as one (%s): %s",
			raw, halfBriefed, pasteHint,
		)
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) != 4 || segs[2] != "issues" || segs[0] == "" || segs[1] == "" {
		return issueSource{}, fmt.Errorf(
			"--issue %q is not an issue URL; want https://<host>/<owner>/<repo>/issues/<number> (%s): %s",
			raw, halfBriefed, pasteHint,
		)
	}
	number, ok := issueNumber(segs[3])
	if !ok {
		return issueSource{}, fmt.Errorf(
			"--issue %q has a non-numeric issue number %q (%s): %s",
			raw, segs[3], halfBriefed, pasteHint,
		)
	}

	// Forge kind from the URL's host alone: github.com is unambiguous, and
	// every other forge we can talk to (Gitea, Forgejo, Codeberg) shares the
	// one /api/v1 surface `fj` speaks. Deliberately NOT resolved via papi
	// here — the URL may name a forge the operator's identity has never
	// heard of, and an explicit URL is the user stating where the issue is.
	// A host that turns out to be neither fails at the fj call carrying fj's
	// own error, which is a clean refusal rather than a wrong brief.
	kind := "forgejo"
	if u.Hostname() == "github.com" {
		kind = "github"
	}
	return issueSource{
		forgeKind: kind,
		host:      u.Hostname(),
		owner:     segs[0],
		name:      segs[1],
		number:    number,
		fromURL:   true,
	}, nil
}

// issueNumber normalizes a bare issue reference ("244", "#244") to its
// digits. Anything else is rejected: before forge dispatch existed a junk
// value was handed straight to gh and surfaced as gh's own confusing error,
// which read as a forge problem rather than a typo.
func issueNumber(s string) (string, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if s == "" {
		return "", false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return s, true
}

// fetchIssue dispatches to the client that speaks the resolved forge's API.
//
// There is deliberately no fallback to gh for an unresolved or unsupported
// forge. Several repos here are Forgejo-canonical with a read-only GitHub
// mirror, so an opportunistic gh call would often SUCCEED and return the
// mirror's stale copy of the issue — a silently stale brief, which is worse
// than a refusal the user can see and act on (spinclass#245).
func fetchIssue(ctx context.Context, src issueSource, repoPath string, d issueDeps) (title, body string, err error) {
	switch src.forgeKind {
	case "github":
		return fetchIssueGitHub(ctx, src, repoPath, d)
	case "gitea", "forgejo", "codeberg":
		return fetchIssueForgejo(ctx, src, repoPath, d)
	default:
		kind := src.forgeKind
		if kind == "" {
			kind = "unresolved"
		}
		return "", "", fmt.Errorf(
			"--issue %s: no supported issue client for forge kind %q (host %q) of %s (%s): %s",
			src.number, kind, src.host, repoPath, halfBriefed, pasteHint,
		)
	}
}

// fetchIssueGitHub reads the issue with `gh issue view`. For a bare number
// the argv is unchanged from before forge dispatch existed (gh resolves the
// repo from cmd.Dir); a URL-sourced issue pins --repo instead, since the URL
// may name a repo other than the spawn target.
func fetchIssueGitHub(ctx context.Context, src issueSource, repoPath string, d issueDeps) (string, string, error) {
	args := []string{"issue", "view", src.number, "--json", "title,body"}
	if src.fromURL {
		args = append(args, "--repo", src.owner+"/"+src.name)
	}
	out, err := d.run(ctx, repoPath, d.ghBin, args...)
	if err != nil {
		return "", "", fmt.Errorf("gh issue view %s in %s failed (%s): %w", src.number, repoPath, halfBriefed, err)
	}
	return parseIssueJSON(out, "gh issue view "+src.number)
}

// fetchIssueForgejo reads the issue from the Gitea/Forgejo REST API through
// `fj`, whose per-instance auth the user already maintains — far less to own
// than a second HTTP client with its own token handling.
//
// -H pins the instance so the fetch does not depend on cmd.Dir's remote: a
// URL may name a forge the target repo has nothing to do with. fj is
// resolved from PATH only; unlike gh there is no build-time pin for it
// (spinclass-build-pins(7)), so a missing fj is a plain, named refusal.
func fetchIssueForgejo(ctx context.Context, src issueSource, repoPath string, d issueDeps) (string, string, error) {
	if src.host == "" || src.owner == "" || src.name == "" {
		return "", "", fmt.Errorf(
			"--issue %s: incomplete %s coordinates (host=%q owner=%q repo=%q) for %s (%s): %s",
			src.number, src.forgeKind, src.host, src.owner, src.name, repoPath, halfBriefed, pasteHint,
		)
	}
	fjBin, err := d.lookPath("fj")
	if err != nil {
		return "", "", fmt.Errorf(
			"--issue %s lives on %s (%s), whose issues are read with `fj`, but fj was not found on PATH (%s): install fj, or %s",
			src.number, src.host, src.forgeKind, halfBriefed, pasteHint,
		)
	}
	// No leading slash on the API path: fj joins it onto the instance's
	// /api/v1 base itself.
	apiPath := "repos/" + src.owner + "/" + src.name + "/issues/" + src.number
	out, err := d.run(ctx, repoPath, fjBin, "-H", src.host, "api", apiPath)
	if err != nil {
		return "", "", fmt.Errorf("fj api %s on %s failed (%s): %w", apiPath, src.host, halfBriefed, err)
	}
	return parseIssueJSON(out, "fj api "+apiPath)
}

// parseIssueJSON pulls title and body out of a fetched issue. GitHub's
// `gh issue view --json title,body` and Forgejo's issue object agree on both
// field names, so one decoder serves both forges.
func parseIssueJSON(out, what string) (string, string, error) {
	var parsed struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return "", "", fmt.Errorf("parsing %s output (%s): %w", what, halfBriefed, err)
	}
	return parsed.Title, parsed.Body, nil
}

// runIssueClient runs a forge client and returns its stdout. stderr is folded
// into the error so the forge's own diagnosis ("could not resolve to an
// Issue", "not authenticated") reaches the user, who is the only one who can
// act on it.
func runIssueClient(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := osexec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%s: %w", msg, err)
		}
		return "", err
	}
	return stdout.String(), nil
}

// issueBinOr prefers a build-time pinned absolute path over a bare name
// resolved through PATH (mirroring internal/repoinfo's binOr).
func issueBinOr(pinned, fallback string) string {
	if pinned != "" {
		return pinned
	}
	return fallback
}
