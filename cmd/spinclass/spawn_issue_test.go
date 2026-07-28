package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/repoinfo"
)

func TestParseIssueURL(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want issueSource
	}{
		{
			"forgejo issue url",
			"https://code.example.com/theowner/therepo/issues/244",
			issueSource{forgeKind: "forgejo", host: "code.example.com", owner: "theowner", name: "therepo", number: "244", fromURL: true},
		},
		{
			"github issue url",
			"https://github.com/owner/repo/issues/12",
			issueSource{forgeKind: "github", host: "github.com", owner: "owner", name: "repo", number: "12", fromURL: true},
		},
		{
			// A fragment/query is url.Parse's business, not the path's, so a
			// link copied from a comment anchor still resolves.
			"github issue url with fragment",
			"https://github.com/owner/repo/issues/12#issuecomment-99",
			issueSource{forgeKind: "github", host: "github.com", owner: "owner", name: "repo", number: "12", fromURL: true},
		},
		{
			"trailing slash",
			"https://code.example.com/theowner/therepo/issues/7/",
			issueSource{forgeKind: "forgejo", host: "code.example.com", owner: "theowner", name: "therepo", number: "7", fromURL: true},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseIssueURL(c.raw)
			if err != nil {
				t.Fatalf("parseIssueURL(%q): %v", c.raw, err)
			}
			if got != c.want {
				t.Errorf("parseIssueURL(%q) = %+v, want %+v", c.raw, got, c.want)
			}
		})
	}
}

// Anything that is not an issue URL must be refused outright rather than
// re-read as a bare number — otherwise a pull-request link would be fetched
// as if it were an issue in whatever repo happened to be the spawn target.
func TestParseIssueURLRejections(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"pull request url", "https://github.com/owner/repo/pull/12"},
		{"repo url", "https://github.com/owner/repo"},
		{"gitlab dash form", "https://gitlab.com/group/repo/-/issues/12"},
		{"non-numeric issue", "https://github.com/owner/repo/issues/abc"},
		{"no host", "https:///owner/repo/issues/12"},
		{"extra path segments", "https://code.example.com/sub/owner/repo/issues/12"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, err := parseIssueURL(c.raw); err == nil {
				t.Fatalf("parseIssueURL(%q) = %+v, want an error", c.raw, got)
			}
		})
	}
}

func TestIssueNumber(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"244":     {"244", true},
		"#244":    {"244", true},
		" 244 ":   {"244", true},
		"":        {"", false},
		"#":       {"", false},
		"abc":     {"", false},
		"12a":     {"", false},
		"owner/r": {"", false},
	}
	for in, want := range cases {
		got, ok := issueNumber(in)
		if got != want.want || ok != want.ok {
			t.Errorf("issueNumber(%q) = (%q,%v), want (%q,%v)", in, got, ok, want.want, want.ok)
		}
	}
}

// recordingDeps builds an issueDeps whose repoInfo is fixed and whose run
// records the argv it was handed, returning a canned stdout.
type issueCall struct {
	dir  string
	name string
	args []string
}

func recordingDeps(info repoinfo.RepoInfo, stdout string, calls *[]issueCall) issueDeps {
	return issueDeps{
		repoInfo: func(context.Context, string) repoinfo.RepoInfo { return info },
		run: func(_ context.Context, dir, name string, args ...string) (string, error) {
			*calls = append(*calls, issueCall{dir: dir, name: name, args: args})
			return stdout, nil
		},
		lookPath: func(file string) (string, error) { return "/usr/bin/" + file, nil },
		ghBin:    "gh",
	}
}

// A bare number against a GitHub target repo keeps the historical gh argv
// (no --repo; gh resolves the repo from cmd.Dir) and the exact composed
// brief shape.
func TestPrependIssueToBriefGitHubBareNumber(t *testing.T) {
	var calls []issueCall
	d := recordingDeps(
		repoinfo.RepoInfo{ForgeKind: "github", Host: "github.com", Owner: "owner", Name: "repo"},
		`{"title":"the title","body":"the body"}`,
		&calls,
	)

	got, err := prependIssueToBriefWith(context.Background(), "/repos/target", "244", "the brief", d)
	if err != nil {
		t.Fatalf("prependIssueToBriefWith: %v", err)
	}
	if want := "the title\n\nthe body\n\n---\n\nthe brief"; got != want {
		t.Errorf("brief = %q, want %q", got, want)
	}
	if len(calls) != 1 {
		t.Fatalf("expected exactly one client call, got %v", calls)
	}
	c := calls[0]
	if c.name != "gh" || c.dir != "/repos/target" {
		t.Errorf("call = %s in %s, want gh in /repos/target", c.name, c.dir)
	}
	if want := "issue view 244 --json title,body"; strings.Join(c.args, " ") != want {
		t.Errorf("gh args = %q, want %q", strings.Join(c.args, " "), want)
	}
}

// A bare number against a Forgejo-canonical target repo routes to fj with
// the REST path (no leading slash) and the instance pinned by -H.
func TestPrependIssueToBriefForgejoBareNumber(t *testing.T) {
	var calls []issueCall
	d := recordingDeps(
		repoinfo.RepoInfo{ForgeKind: "forgejo", Host: "code.example.com", Owner: "theowner", Name: "therepo"},
		`{"title":"forge title","body":"forge body"}`,
		&calls,
	)

	got, err := prependIssueToBriefWith(context.Background(), "/repos/target", "244", "the brief", d)
	if err != nil {
		t.Fatalf("prependIssueToBriefWith: %v", err)
	}
	if want := "forge title\n\nforge body\n\n---\n\nthe brief"; got != want {
		t.Errorf("brief = %q, want %q", got, want)
	}
	if len(calls) != 1 {
		t.Fatalf("expected exactly one client call, got %v", calls)
	}
	c := calls[0]
	if c.name != "/usr/bin/fj" {
		t.Errorf("client = %q, want the PATH-resolved fj", c.name)
	}
	want := "-H code.example.com api repos/theowner/therepo/issues/244"
	if strings.Join(c.args, " ") != want {
		t.Errorf("fj args = %q, want %q", strings.Join(c.args, " "), want)
	}
}

// gitea and codeberg share Forgejo's /api/v1 surface, so both must reach fj.
func TestPrependIssueToBriefGiteaFamilyRoutesToFj(t *testing.T) {
	for _, kind := range []string{"gitea", "codeberg"} {
		t.Run(kind, func(t *testing.T) {
			var calls []issueCall
			d := recordingDeps(
				repoinfo.RepoInfo{ForgeKind: kind, Host: "h.example.com", Owner: "o", Name: "r"},
				`{"title":"t","body":"b"}`,
				&calls,
			)
			if _, err := prependIssueToBriefWith(context.Background(), "/repos/target", "1", "brief", d); err != nil {
				t.Fatalf("prependIssueToBriefWith: %v", err)
			}
			if len(calls) != 1 || calls[0].name != "/usr/bin/fj" {
				t.Fatalf("expected an fj call, got %v", calls)
			}
		})
	}
}

// A full URL is fetched from the URL's own forge regardless of the target
// repo's remote — the headline ask of spinclass#245. The GitHub variant also
// pins --repo, since the URL may name a repo other than the spawn target.
func TestPrependIssueToBriefURLIgnoresTargetForge(t *testing.T) {
	cases := []struct {
		name     string
		url      string
		wantBin  string
		wantArgs string
	}{
		{
			"forgejo url from a github target repo",
			"https://code.example.com/theowner/therepo/issues/244",
			"/usr/bin/fj",
			"-H code.example.com api repos/theowner/therepo/issues/244",
		},
		{
			"github url from a github target repo",
			"https://github.com/other/mirror/issues/9",
			"gh",
			"issue view 9 --json title,body --repo other/mirror",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []issueCall
			// The target repo is GitHub-canonical; the URL must win anyway.
			d := recordingDeps(
				repoinfo.RepoInfo{ForgeKind: "github", Host: "github.com", Owner: "mirrorowner", Name: "mirrorrepo"},
				`{"title":"t","body":"b"}`,
				&calls,
			)
			got, err := prependIssueToBriefWith(context.Background(), "/repos/target", c.url, "brief", d)
			if err != nil {
				t.Fatalf("prependIssueToBriefWith: %v", err)
			}
			if want := "t\n\nb\n\n---\n\nbrief"; got != want {
				t.Errorf("brief = %q, want %q", got, want)
			}
			if len(calls) != 1 {
				t.Fatalf("expected exactly one client call, got %v", calls)
			}
			if calls[0].name != c.wantBin {
				t.Errorf("client = %q, want %q", calls[0].name, c.wantBin)
			}
			if got := strings.Join(calls[0].args, " "); got != c.wantArgs {
				t.Errorf("args = %q, want %q", got, c.wantArgs)
			}
		})
	}
}

// An unsupported or unresolvable forge must refuse loudly — and must NOT
// quietly try gh, which on a mirrored repo would succeed with stale text.
func TestPrependIssueToBriefUnsupportedForge(t *testing.T) {
	for _, kind := range []string{"gitlab", "bare", ""} {
		name := kind
		if name == "" {
			name = "unresolved"
		}
		t.Run(name, func(t *testing.T) {
			var calls []issueCall
			d := recordingDeps(
				repoinfo.RepoInfo{ForgeKind: kind, Host: "gitlab.example.com", Owner: "o", Name: "r"},
				`{"title":"t","body":"b"}`,
				&calls,
			)
			_, err := prependIssueToBriefWith(context.Background(), "/repos/target", "244", "brief", d)
			if err == nil {
				t.Fatal("expected an error for an unsupported forge")
			}
			if !strings.Contains(err.Error(), "paste the issue body into the brief") {
				t.Errorf("error %q does not offer the paste-into-the-brief escape hatch", err)
			}
			if len(calls) != 0 {
				t.Errorf("expected no forge client to run, got %v", calls)
			}
		})
	}
}

// A Forgejo target whose owner could not be resolved (vanity remote, no papi
// org entry) has nowhere to send the REST call; refuse rather than guess.
func TestPrependIssueToBriefForgejoIncompleteCoordinates(t *testing.T) {
	var calls []issueCall
	d := recordingDeps(
		repoinfo.RepoInfo{ForgeKind: "forgejo", Host: "code.example.com", Name: "therepo"},
		`{"title":"t","body":"b"}`,
		&calls,
	)
	_, err := prependIssueToBriefWith(context.Background(), "/repos/target", "244", "brief", d)
	if err == nil {
		t.Fatal("expected an error when the owner is unresolved")
	}
	if !strings.Contains(err.Error(), "paste the issue body into the brief") {
		t.Errorf("error %q does not offer the paste-into-the-brief escape hatch", err)
	}
	if len(calls) != 0 {
		t.Errorf("expected no forge client to run, got %v", calls)
	}
}

// fj is PATH-resolved only (no build-time pin), so its absence must name
// itself rather than surfacing as an exec error from deep inside.
func TestPrependIssueToBriefMissingFj(t *testing.T) {
	var calls []issueCall
	d := recordingDeps(
		repoinfo.RepoInfo{ForgeKind: "forgejo", Host: "code.example.com", Owner: "o", Name: "r"},
		`{"title":"t","body":"b"}`,
		&calls,
	)
	d.lookPath = func(string) (string, error) {
		return "", fmt.Errorf("exec: \"fj\": executable file not found in $PATH")
	}

	_, err := prependIssueToBriefWith(context.Background(), "/repos/target", "244", "brief", d)
	if err == nil {
		t.Fatal("expected an error when fj is missing")
	}
	if !strings.Contains(err.Error(), "fj was not found on PATH") {
		t.Errorf("error %q does not name the missing fj", err)
	}
	if len(calls) != 0 {
		t.Errorf("expected no forge client to run, got %v", calls)
	}
}

// A failing fetch and unparseable output are both hard errors carrying the
// half-briefed refusal — the pre-existing contract, preserved across the
// forge-dispatch rewrite.
func TestPrependIssueToBriefFetchAndParseFailures(t *testing.T) {
	t.Run("fetch failure", func(t *testing.T) {
		d := issueDeps{
			repoInfo: func(context.Context, string) repoinfo.RepoInfo {
				return repoinfo.RepoInfo{ForgeKind: "github", Host: "github.com", Owner: "o", Name: "r"}
			},
			run: func(context.Context, string, string, ...string) (string, error) {
				return "", fmt.Errorf("could not resolve to an Issue: exit status 1")
			},
			lookPath: func(f string) (string, error) { return f, nil },
			ghBin:    "gh",
		}
		_, err := prependIssueToBriefWith(context.Background(), "/repos/target", "244", "brief", d)
		if err == nil {
			t.Fatal("expected an error when the fetch fails")
		}
		if !strings.Contains(err.Error(), halfBriefed) {
			t.Errorf("error %q lost the half-briefed refusal", err)
		}
		if !strings.Contains(err.Error(), "could not resolve to an Issue") {
			t.Errorf("error %q dropped the forge client's own diagnosis", err)
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		var calls []issueCall
		d := recordingDeps(
			repoinfo.RepoInfo{ForgeKind: "forgejo", Host: "code.example.com", Owner: "o", Name: "r"},
			"not json",
			&calls,
		)
		_, err := prependIssueToBriefWith(context.Background(), "/repos/target", "244", "brief", d)
		if err == nil {
			t.Fatal("expected an error for malformed output")
		}
		if !strings.Contains(err.Error(), halfBriefed) {
			t.Errorf("error %q lost the half-briefed refusal", err)
		}
	})
}

// A value that is neither a number nor a URL is rejected before any forge
// resolution happens.
func TestPrependIssueToBriefGarbageValue(t *testing.T) {
	var calls []issueCall
	d := recordingDeps(repoinfo.RepoInfo{ForgeKind: "github"}, "{}", &calls)
	_, err := prependIssueToBriefWith(context.Background(), "/repos/target", "not-an-issue", "brief", d)
	if err == nil {
		t.Fatal("expected an error for a non-number, non-URL value")
	}
	if len(calls) != 0 {
		t.Errorf("expected no forge client to run, got %v", calls)
	}
}
