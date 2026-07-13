// Package repoinfo resolves a git repository's forge identity — provider,
// owner, name, web link, and one-line description — from a worktree or
// checkout path. It backs the repository line the dynamic system-prompt
// fragment contributes (internal/sysprompt).
//
// Resolution is best-effort and bounded by the caller's context: the local
// git remote yields host/owner/name/link with no network; the precise forge
// kind for a non-github.com host is looked up against the operator's
// published PAPI (`papi identity domain` + `papi query`); and the repo
// description is fetched live from the forge (`gh api` for GitHub, the
// Gitea/Forgejo REST API for a self-hosted forge). Any failure — missing
// binary, offline, unparseable remote, deadline — leaves the corresponding
// field empty rather than erroring, so the fragment degrades to whatever
// could be resolved (worst case: nothing, exactly as the pre-enrichment
// behaviour). The forge-kind vocabulary is papi's (RFC-0001 §1.1):
// github | gitea | gitlab | codeberg | forgejo | bare.
package repoinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"

	"github.com/amarbel-llc/spinclass/internal/embeds"
	"github.com/amarbel-llc/spinclass/internal/git"
)

// RepoInfo is the resolved forge identity of a repository. Every field is
// best-effort; an empty field means "could not resolve".
type RepoInfo struct {
	ForgeKind   string // papi enum: github|gitea|gitlab|codeberg|forgejo|bare
	Host        string // remote host, e.g. github.com
	Owner       string // owner/org login, e.g. amarbel-llc
	OwnerType   string // "org" | "user" (best-effort)
	Name        string // repo name, e.g. spinclass
	URL         string // web link, e.g. https://github.com/amarbel-llc/spinclass
	Description string // one-line summary, live-fetched
}

// commandRunner runs an external command under ctx and returns trimmed
// stdout. name is either an absolute path (a build-time pin) or a bare
// binary name resolved through PATH.
type commandRunner func(ctx context.Context, name string, args ...string) (string, error)

// deps are the external seams fetch depends on, injected so the
// orchestration is unit-testable without a real repo, papi, gh, or network.
type deps struct {
	getRemote func(path string) (string, error)
	run       commandRunner
	httpGet   func(ctx context.Context, rawURL string) ([]byte, error)
	papiBin   string
	ghBin     string
}

// Fetch resolves the repository at path best-effort under ctx's deadline.
func Fetch(ctx context.Context, path string) RepoInfo {
	return fetch(ctx, path, deps{
		getRemote: git.RemoteURL,
		run:       execRunner,
		httpGet:   defaultHTTPGet,
		papiBin:   binOr(embeds.PapiBin(), "papi"),
		ghBin:     binOr(embeds.GhBin(), "gh"),
	})
}

func fetch(ctx context.Context, path string, d deps) RepoInfo {
	var info RepoInfo

	raw, err := d.getRemote(path)
	if err != nil || raw == "" {
		return info
	}
	host, owner, name, ok := parseRemoteURL(raw)
	if !ok {
		return info
	}
	info.Host, info.Owner, info.Name = host, owner, name

	// Forge kind: github.com is unambiguous; any other host is resolved
	// against the operator's published PAPI forges/organizations.
	var baseURL string
	if host == "github.com" {
		info.ForgeKind = "github"
	} else {
		var papiLogin string
		info.ForgeKind, info.OwnerType, baseURL, papiLogin = papiForge(ctx, d, host)
		// Vanity remote: owner was absent in the remote path; fill from the
		// papi organization login when available (spinclass#221).
		if info.Owner == "" && papiLogin != "" {
			info.Owner = papiLogin
		}
	}
	// Construct URL once all owner sources are resolved.
	if info.Owner != "" {
		if baseURL != "" {
			info.URL = strings.TrimRight(baseURL, "/") + "/" + info.Owner + "/" + info.Name
		} else {
			info.URL = "https://" + host + "/" + info.Owner + "/" + info.Name
		}
	}

	// Description (and, for GitHub, an authoritative link + owner type) is
	// fetched live from the forge, dispatched on the resolved kind.
	switch info.ForgeKind {
	case "github":
		if info.Owner != "" {
			if r, ok := githubRepo(ctx, d, info.Owner, name); ok {
				if r.Description != "" {
					info.Description = r.Description
				}
				if r.OwnerType != "" {
					info.OwnerType = r.OwnerType
				}
				if r.HTMLURL != "" {
					info.URL = r.HTMLURL
				}
			}
		}
	case "gitea", "forgejo", "codeberg":
		// Gitea and its fork Forgejo (and codeberg.org, a Forgejo host)
		// share the /api/v1 REST surface. Unauthenticated best-effort: a
		// public repo returns its description; a private one 404s and the
		// description is simply omitted.
		if baseURL != "" && info.Owner != "" {
			if desc := giteaDescription(ctx, d, baseURL, info.Owner, name); desc != "" {
				info.Description = desc
			}
		}
	}

	return info
}

// parseRemoteURL extracts host, owner, and name from a git remote URL in
// either scp-like SSH form (git@host:owner/repo.git), ssh:// URL form, or
// http(s):// form. ok is false when the shape is unrecognized or a name
// can't be isolated. owner may be empty for vanity single-segment paths
// (e.g. git@code.example.com:repo.git) — see splitOwnerRepo.
func parseRemoteURL(raw string) (host, owner, name string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", false
	}

	var path string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return "", "", "", false
		}
		host = u.Hostname()
		path = u.Path
	case isSCPLike(raw):
		// git@host:owner/repo(.git) — split on the first ':'.
		at := strings.IndexByte(raw, '@')
		colon := strings.IndexByte(raw, ':')
		host = raw[at+1 : colon]
		path = raw[colon+1:]
	default:
		return "", "", "", false
	}
	if host == "" {
		return "", "", "", false
	}

	owner, name, ok = splitOwnerRepo(path)
	if !ok {
		return "", "", "", false
	}
	return host, owner, name, true
}

// isSCPLike reports whether raw is a scp-like SSH remote
// (user@host:path), which git accepts but which is not a valid URL. It is
// distinguished from a Windows-style path or an http URL by requiring an
// '@' before the first ':' and no '//' scheme separator.
func isSCPLike(raw string) bool {
	colon := strings.IndexByte(raw, ':')
	at := strings.IndexByte(raw, '@')
	return at > 0 && colon > at && !strings.Contains(raw, "://")
}

// splitOwnerRepo turns a remote path ("/amarbel-llc/spinclass.git",
// "group/sub/repo") into owner + name. name is the last segment (minus a
// trailing .git); owner is everything before it joined by '/', which
// preserves GitLab-style subgroups. A single-segment path (vanity remote
// like git@host:repo.git) returns empty owner with ok=true; the caller
// resolves the owner via papi (spinclass#221).
func splitOwnerRepo(path string) (owner, name string, ok bool) {
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	segs := strings.Split(path, "/")
	if segs[0] == "" || segs[len(segs)-1] == "" {
		return "", "", false
	}
	if len(segs) == 1 {
		return "", segs[0], true
	}
	name = segs[len(segs)-1]
	owner = strings.Join(segs[:len(segs)-1], "/")
	return owner, name, true
}

// ghRepo is the subset of the GitHub repo API response we consume.
type ghRepo struct {
	Description string
	OwnerType   string // normalized "org" | "user"
	HTMLURL     string
}

func githubRepo(ctx context.Context, d deps, owner, name string) (ghRepo, bool) {
	out, err := d.run(ctx, d.ghBin, "api", "repos/"+owner+"/"+name)
	if err != nil {
		return ghRepo{}, false
	}
	return parseGitHubRepoJSON([]byte(out))
}

func parseGitHubRepoJSON(b []byte) (ghRepo, bool) {
	var raw struct {
		Description string `json:"description"`
		HTMLURL     string `json:"html_url"`
		Owner       struct {
			Type string `json:"type"`
		} `json:"owner"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return ghRepo{}, false
	}
	return ghRepo{
		Description: raw.Description,
		HTMLURL:     raw.HTMLURL,
		OwnerType:   normalizeOwnerType(raw.Owner.Type),
	}, true
}

// papiForge looks up the forge kind (and owner type, base URL, and org login)
// for host by querying the operator's published PAPI. Best-effort: no papi,
// no identity domain, or no matching forge yields empty strings. login is the
// organization login found in a matching .organizations[] entry — non-empty
// only when the papi identity includes an org whose base_url matches host,
// used to resolve the owner for vanity remotes (spinclass#221).
func papiForge(ctx context.Context, d deps, host string) (kind, ownerType, baseURL, login string) {
	domain, err := d.run(ctx, d.papiBin, "identity", "domain")
	if err != nil || domain == "" {
		return
	}
	out, err := d.run(ctx, d.papiBin, "query", domain, ".forges[], .organizations[]")
	if err != nil || out == "" {
		return
	}
	return matchForge(out, host)
}

// matchForge scans papi query output — a stream of JSON objects (forges then
// organizations) — for entries whose base_url host matches host. It
// accumulates forge kind + owner type + base URL from forge entries and
// org login from organization entries; multiple entries for the same host
// are merged so a forge entry and an org entry together yield all four
// values (spinclass#221).
func matchForge(papiOut, host string) (kind, ownerType, baseURL, login string) {
	dec := json.NewDecoder(strings.NewReader(papiOut))
	for {
		var e struct {
			Kind         string `json:"kind"`
			BaseURL      string `json:"base_url"`
			IdentityType string `json:"identity_type"`
			Login        string `json:"login"`
		}
		if err := dec.Decode(&e); err != nil {
			break
		}
		if e.BaseURL == "" || hostOf(e.BaseURL) != host {
			continue
		}
		if e.Kind != "" && kind == "" {
			kind = e.Kind
			ownerType = normalizeOwnerType(e.IdentityType)
			baseURL = e.BaseURL
		}
		if e.Login != "" && login == "" {
			login = e.Login
			if ownerType == "" {
				ownerType = normalizeOwnerType(e.IdentityType)
			}
			if baseURL == "" {
				baseURL = e.BaseURL
			}
		}
		if kind != "" && login != "" {
			break
		}
	}
	return
}

// giteaDescription fetches a repo's description from a Gitea/Forgejo forge's
// unauthenticated REST API. Empty on any failure.
func giteaDescription(ctx context.Context, d deps, baseURL, owner, name string) string {
	api := strings.TrimRight(baseURL, "/") + "/api/v1/repos/" + owner + "/" + name
	b, err := d.httpGet(ctx, api)
	if err != nil {
		return ""
	}
	var raw struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return ""
	}
	return raw.Description
}

// normalizeOwnerType maps a forge's owner/identity type onto "org"/"user".
// Handles GitHub's "Organization"/"User" and papi's lowercased variants;
// an unrecognized value passes through lowercased.
func normalizeOwnerType(t string) string {
	switch lt := strings.ToLower(t); {
	case lt == "":
		return ""
	case strings.Contains(lt, "org"):
		return "org"
	case strings.Contains(lt, "user"):
		return "user"
	default:
		return lt
	}
}

// hostOf returns the hostname of a URL, or "" if it doesn't parse.
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func binOr(pinned, fallback string) string {
	if pinned != "" {
		return pinned
	}
	return fallback
}

func execRunner(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func defaultHTTPGet(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("forge api %s: %s", rawURL, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
