package sshsig

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/ssh"
)

// AllowedSigners is a parsed subset of OpenSSH's allowed_signers file.
type AllowedSigners struct {
	entries []allowedEntry
}

type allowedEntry struct {
	principals []string // "*" matches any principal
	namespaces []string // nil: the entry permits any namespace
	key        ssh.PublicKey
}

// ParseAllowedSigners reads one entry per line:
//
//	principals [namespaces="a,b"] keytype base64key [comment]
//
// Blank lines and lines starting with '#' are ignored. Principals are
// comma-separated exact names, or a lone "*" for any principal (no globs, no
// negation, no quoting). namespaces is the only supported option; any other
// option (cert-authority, valid-before, ...) is a parse error naming it, so an
// entry can never silently carry a restriction we do not enforce.
//
// An entry without namespaces permits any namespace. Spinclass callers must
// still always pass a pinned, non-empty namespace to Verify and KeysFor.
func ParseAllowedSigners(r io.Reader) (*AllowedSigners, error) {
	a := &AllowedSigners{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		e, err := parseAllowedLine(line)
		if err != nil {
			return nil, fmt.Errorf("sshsig: allowed signers line %d: %w", n, err)
		}
		a.entries = append(a.entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("sshsig: reading allowed signers: %w", err)
	}
	return a, nil
}

func parseAllowedLine(line string) (allowedEntry, error) {
	var e allowedEntry
	principals, rest, found := strings.Cut(line, " ")
	if !found {
		principals, rest, found = strings.Cut(line, "\t")
	}
	if !found {
		return e, errors.New("expected principals followed by a key")
	}
	for _, p := range strings.Split(principals, ",") {
		if p == "" || (p != "*" && strings.ContainsAny(p, "*?!\"")) {
			return e, fmt.Errorf("unsupported principal pattern %q", p)
		}
		e.principals = append(e.principals, p)
	}

	// ParseAuthorizedKey splits [options] keytype base64 [comment], honouring
	// quoted commas inside options.
	key, _, options, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(rest)))
	if err != nil {
		return e, fmt.Errorf("parsing key: %w", err)
	}
	if !supportedKeyType(key.Type()) {
		return e, fmt.Errorf("unsupported key type %q", key.Type())
	}
	e.key = key

	for _, opt := range options {
		name, value, _ := strings.Cut(opt, "=")
		if name != "namespaces" {
			return e, fmt.Errorf("unsupported option %q", name)
		}
		if e.namespaces != nil {
			return e, errors.New("duplicate namespaces option")
		}
		if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
			return e, errors.New(`namespaces option must be quoted: namespaces="a,b"`)
		}
		for _, ns := range strings.Split(value[1:len(value)-1], ",") {
			if ns == "" || strings.ContainsAny(ns, "*?!\"") {
				return e, fmt.Errorf("unsupported namespace pattern %q", ns)
			}
			e.namespaces = append(e.namespaces, ns)
		}
	}
	return e, nil
}

// KeysFor returns the keys whose principals include principal and whose
// namespaces permit namespace. An empty namespace matches nothing.
func (a *AllowedSigners) KeysFor(principal, namespace string) []ssh.PublicKey {
	if namespace == "" {
		return nil
	}
	var keys []ssh.PublicKey
	for _, e := range a.entries {
		if e.matchesPrincipal(principal) && e.permitsNamespace(namespace) {
			keys = append(keys, e.key)
		}
	}
	return keys
}

func (e allowedEntry) matchesPrincipal(principal string) bool {
	for _, p := range e.principals {
		if p == "*" || p == principal {
			return true
		}
	}
	return false
}

func (e allowedEntry) permitsNamespace(namespace string) bool {
	if e.namespaces == nil {
		return true
	}
	for _, ns := range e.namespaces {
		if ns == namespace {
			return true
		}
	}
	return false
}
