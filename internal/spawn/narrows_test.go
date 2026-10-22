package spawn

import (
	"slices"
	"testing"
)

func TestCatalogWidenings(t *testing.T) {
	base := ResolvedProfile{
		Name: "base", Provider: "claude", Backend: "api", Model: "opus",
		KeptServers: []string{"a/a", "b/b", "just-us/just-us"},
		Excluded:    map[string][]string{"b/b": {"danger", "risky"}},
		EnvKnown:    true,
		Env:         map[string]string{"K1": "d1", "K2": "d2"},
	}
	with := func(f func(*ResolvedProfile)) ResolvedProfile {
		c := ResolvedProfile{
			Name: "child", Provider: base.Provider, Backend: base.Backend, Model: base.Model,
			KeptServers: slices.Clone(base.KeptServers),
			Excluded:    map[string][]string{"b/b": {"danger", "risky"}},
			EnvKnown:    true,
			Env:         map[string]string{"K1": "d1", "K2": "d2"},
		}
		f(&c)
		return c
	}
	cases := []struct {
		name  string
		child ResolvedProfile
		base  ResolvedProfile
		want  []string
	}{
		{name: "identical", child: with(func(*ResolvedProfile) {}), base: base},
		{
			name: "pure subset: fewer servers, more exclusions, same env",
			child: with(func(c *ResolvedProfile) {
				c.KeptServers = []string{"b/b", "just-us/just-us"}
				c.Excluded["b/b"] = []string{"danger", "extra", "risky"}
			}),
			base: base,
		},
		{
			name:  "adds a server",
			child: with(func(c *ResolvedProfile) { c.KeptServers = append(c.KeptServers, "z/z", "y/y") }),
			base:  base,
			want:  []string{"adds server y/y", "adds server z/z"},
		},
		{
			name:  "adds an env key",
			child: with(func(c *ResolvedProfile) { c.Env["K0"] = "d0" }),
			base:  base,
			want:  []string{"adds env key K0"},
		},
		{
			name:  "changes an env value",
			child: with(func(c *ResolvedProfile) { c.Env["K2"] = "other" }),
			base:  base,
			want:  []string{"changes env value of K2"},
		},
		{
			name:  "changes the model",
			child: with(func(c *ResolvedProfile) { c.Model = "haiku" }),
			base:  base,
			want:  []string{"changes model opus to haiku"},
		},
		{
			name:  "an unset child model widens a base that names one",
			child: with(func(c *ResolvedProfile) { c.Model = "" }),
			base:  base,
			want:  []string{"leaves the model unset (base names opus; clown would run its default)"},
		},
		{
			name:  "an unset model on both is no change",
			child: with(func(c *ResolvedProfile) { c.Model = "" }),
			base:  func() ResolvedProfile { b := base; b.Model = ""; return b }(),
		},
		{
			name:  "changes provider and backend",
			child: with(func(c *ResolvedProfile) { c.Provider = "codex"; c.Backend = "cli" }),
			base:  base,
			want:  []string{"changes provider claude to codex", "changes backend api to cli"},
		},
		{
			name:  "drops an exclusion",
			child: with(func(c *ResolvedProfile) { c.Excluded["b/b"] = []string{"danger"} }),
			base:  base,
			want:  []string{"drops exclusion b/b:risky"},
		},
		{
			name:  "dropping the whole server drops no exclusion",
			child: with(func(c *ResolvedProfile) { c.KeptServers = []string{"just-us/just-us"}; c.Excluded = nil }),
			base:  base,
		},
		{
			name:  "non-selective child of a selective base",
			child: with(func(c *ResolvedProfile) { c.KeptServers = nil }),
			base:  base,
			want:  []string{"keeps every server (base keeps only: a/a, b/b, just-us/just-us)"},
		},
		{
			name:  "env unknown on the child",
			child: with(func(c *ResolvedProfile) { c.EnvKnown = false; c.Env = nil }),
			base:  base,
			want:  []string{"env cannot be compared (clown does not expose a secrets-safe env view yet)"},
		},
		{
			name:  "env unknown on the base",
			child: with(func(*ResolvedProfile) {}),
			base:  func() ResolvedProfile { b := base; b.EnvKnown = false; return b }(),
			want:  []string{"env cannot be compared (clown does not expose a secrets-safe env view yet)"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := catalogWidenings(c.base, c.child)
			if !slices.Equal(got, c.want) {
				t.Errorf("catalogWidenings = %q, want %q", got, c.want)
			}
		})
	}
}
