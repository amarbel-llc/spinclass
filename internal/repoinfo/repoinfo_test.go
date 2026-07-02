package repoinfo

import (
	"context"
	"fmt"
	"testing"
)

func TestParseRemoteURL(t *testing.T) {
	cases := []struct {
		name              string
		raw               string
		host, owner, repo string
		ok                bool
	}{
		{"ssh scp-like .git", "git@github.com:amarbel-llc/spinclass.git", "github.com", "amarbel-llc", "spinclass", true},
		{"ssh scp-like no .git", "git@github.com:amarbel-llc/spinclass", "github.com", "amarbel-llc", "spinclass", true},
		{"https .git", "https://github.com/amarbel-llc/spinclass.git", "github.com", "amarbel-llc", "spinclass", true},
		{"https no .git", "https://github.com/amarbel-llc/spinclass", "github.com", "amarbel-llc", "spinclass", true},
		{"ssh url with port", "ssh://git@git.example.com:2222/team/repo.git", "git.example.com", "team", "repo", true},
		{"self-hosted https", "https://git.example.com/team/repo.git", "git.example.com", "team", "repo", true},
		{"gitlab subgroup", "git@gitlab.com:group/sub/repo.git", "gitlab.com", "group/sub", "repo", true},
		{"trailing slash", "https://github.com/amarbel-llc/spinclass/", "github.com", "amarbel-llc", "spinclass", true},
		{"empty", "", "", "", "", false},
		{"no owner", "git@github.com:spinclass.git", "", "", "", false},
		{"garbage", "not a url", "", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			host, owner, repo, ok := parseRemoteURL(c.raw)
			if ok != c.ok || host != c.host || owner != c.owner || repo != c.repo {
				t.Errorf("parseRemoteURL(%q) = (%q,%q,%q,%v), want (%q,%q,%q,%v)",
					c.raw, host, owner, repo, ok, c.host, c.owner, c.repo, c.ok)
			}
		})
	}
}

func TestParseGitHubRepoJSON(t *testing.T) {
	body := `{"description":"a tool","html_url":"https://github.com/o/r","owner":{"type":"Organization"}}`
	r, ok := parseGitHubRepoJSON([]byte(body))
	if !ok {
		t.Fatal("expected ok")
	}
	if r.Description != "a tool" || r.HTMLURL != "https://github.com/o/r" || r.OwnerType != "org" {
		t.Errorf("got %+v", r)
	}

	if _, ok := parseGitHubRepoJSON([]byte("not json")); ok {
		t.Error("expected parse failure")
	}
}

func TestMatchForge(t *testing.T) {
	// papi query emits one JSON object per result (forges then orgs).
	out := `{"kind":"github","base_url":"https://github.com","identity_type":"org"}
{"kind":"forgejo","base_url":"https://git.example.com","identity_type":"user"}`

	kind, ownerType, base := matchForge(out, "git.example.com")
	if kind != "forgejo" || ownerType != "user" || base != "https://git.example.com" {
		t.Errorf("got (%q,%q,%q)", kind, ownerType, base)
	}

	if kind, _, _ := matchForge(out, "nope.example.com"); kind != "" {
		t.Errorf("expected no match, got %q", kind)
	}
}

func TestNormalizeOwnerType(t *testing.T) {
	cases := map[string]string{
		"Organization": "org",
		"org":          "org",
		"User":         "user",
		"user":         "user",
		"":             "",
		"weird":        "weird",
	}
	for in, want := range cases {
		if got := normalizeOwnerType(in); got != want {
			t.Errorf("normalizeOwnerType(%q) = %q, want %q", in, got, want)
		}
	}
}

// fetch orchestration: GitHub path enriches description/owner/link from gh.
func TestFetch_GitHub(t *testing.T) {
	d := deps{
		getRemote: func(string) (string, error) { return "git@github.com:amarbel-llc/spinclass.git", nil },
		papiBin:   "papi",
		ghBin:     "gh",
		run: func(_ context.Context, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 1 && args[0] == "api" {
				return `{"description":"worktree mgr","html_url":"https://github.com/amarbel-llc/spinclass","owner":{"type":"Organization"}}`, nil
			}
			return "", fmt.Errorf("unexpected call %s %v", name, args)
		},
		httpGet: func(context.Context, string) ([]byte, error) { return nil, fmt.Errorf("no http expected") },
	}

	got := fetch(context.Background(), "/w", d)
	want := RepoInfo{
		ForgeKind:   "github",
		Host:        "github.com",
		Owner:       "amarbel-llc",
		OwnerType:   "org",
		Name:        "spinclass",
		URL:         "https://github.com/amarbel-llc/spinclass",
		Description: "worktree mgr",
	}
	if got != want {
		t.Errorf("fetch = %+v, want %+v", got, want)
	}
}

// fetch orchestration: non-github host resolves kind via papi and
// description via the Gitea/Forgejo API.
func TestFetch_SelfHostedForgejo(t *testing.T) {
	d := deps{
		getRemote: func(string) (string, error) { return "git@git.example.com:team/repo.git", nil },
		papiBin:   "papi",
		ghBin:     "gh",
		run: func(_ context.Context, name string, args ...string) (string, error) {
			switch {
			case name == "papi" && len(args) == 2 && args[0] == "identity" && args[1] == "domain":
				return "example.com", nil
			case name == "papi" && len(args) >= 1 && args[0] == "query":
				return `{"kind":"forgejo","base_url":"https://git.example.com","identity_type":"user"}`, nil
			}
			return "", fmt.Errorf("unexpected call %s %v", name, args)
		},
		httpGet: func(_ context.Context, u string) ([]byte, error) {
			if u != "https://git.example.com/api/v1/repos/team/repo" {
				return nil, fmt.Errorf("unexpected url %q", u)
			}
			return []byte(`{"description":"self hosted repo"}`), nil
		},
	}

	got := fetch(context.Background(), "/w", d)
	want := RepoInfo{
		ForgeKind:   "forgejo",
		Host:        "git.example.com",
		Owner:       "team",
		OwnerType:   "user",
		Name:        "repo",
		URL:         "https://git.example.com/team/repo",
		Description: "self hosted repo",
	}
	if got != want {
		t.Errorf("fetch = %+v, want %+v", got, want)
	}
}

// A remote that doesn't parse yields a zero RepoInfo and makes no external
// calls.
func TestFetch_UnparseableRemote(t *testing.T) {
	d := deps{
		getRemote: func(string) (string, error) { return "garbage", nil },
		run:       func(context.Context, string, ...string) (string, error) { t.Fatal("no run expected"); return "", nil },
		httpGet:   func(context.Context, string) ([]byte, error) { t.Fatal("no http expected"); return nil, nil },
	}
	if got := fetch(context.Background(), "/w", d); got != (RepoInfo{}) {
		t.Errorf("expected zero RepoInfo, got %+v", got)
	}
}

// GitHub host with gh unavailable still yields provider/owner/name/link.
func TestFetch_GitHubGhUnavailable(t *testing.T) {
	d := deps{
		getRemote: func(string) (string, error) { return "https://github.com/o/r.git", nil },
		papiBin:   "papi",
		ghBin:     "gh",
		run:       func(context.Context, string, ...string) (string, error) { return "", fmt.Errorf("gh: not found") },
		httpGet:   func(context.Context, string) ([]byte, error) { return nil, fmt.Errorf("no http") },
	}
	got := fetch(context.Background(), "/w", d)
	want := RepoInfo{ForgeKind: "github", Host: "github.com", Owner: "o", Name: "r", URL: "https://github.com/o/r"}
	if got != want {
		t.Errorf("fetch = %+v, want %+v", got, want)
	}
}
