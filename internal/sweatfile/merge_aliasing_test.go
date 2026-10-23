package sweatfile

import (
	"maps"
	"slices"
	"testing"
)

// #351: merging one base with two different overrides must leave the base's
// [hooks] untouched and must not carry the first override into the second
// result. [auth] already merges by copy; [hooks] must too.
func TestMergeWithDoesNotMutateTheBaseHooks(t *testing.T) {
	strp := func(s string) *string { return &s }
	base := Sweatfile{Hooks: &Hooks{PreMerge: strp("just")}}

	first := base.MergeWith(Sweatfile{Hooks: &Hooks{Repair: strp("repair-one")}})
	second := base.MergeWith(Sweatfile{Hooks: &Hooks{PostMerge: strp("deploy")}})

	if base.Hooks.Repair != nil || base.Hooks.PostMerge != nil {
		t.Errorf("the base was mutated: repair=%v post-merge=%v", base.Hooks.Repair, base.Hooks.PostMerge)
	}
	if second.Hooks.Repair != nil {
		t.Errorf("the second merge carries the first override's repair %q", *second.Hooks.Repair)
	}
	if first.Hooks.Repair == nil || *first.Hooks.Repair != "repair-one" || first.Hooks.PostMerge != nil {
		t.Errorf("first merge = %+v", *first.Hooks)
	}
	if second.Hooks.PreMerge == nil || *second.Hooks.PreMerge != "just" {
		t.Errorf("second merge lost the base pre-merge: %+v", *second.Hooks)
	}
}

// withSpare returns items in a slice with spare capacity, so an append that
// reuses the backing array, rather than a struct shared through a pointer,
// shows up as one merge overwriting another's element (#351).
func withSpare(items ...string) []string {
	return append(make([]string, 0, len(items)+4), items...)
}

// The other sections' appended string arrays and per-key maps must not reach
// back into the base or into each other's results either. One section per
// test (#351).
func TestMergeWithDoesNotMutateTheBaseClaude(t *testing.T) {
	base := Sweatfile{Claude: &Claude{Allow: withSpare("a")}}
	first := base.MergeWith(Sweatfile{Claude: &Claude{Allow: []string{"b"}}})
	second := base.MergeWith(Sweatfile{Claude: &Claude{Allow: []string{"c"}}})
	if !slices.Equal(base.Claude.Allow, []string{"a"}) || !slices.Equal(first.Claude.Allow, []string{"a", "b"}) || !slices.Equal(second.Claude.Allow, []string{"a", "c"}) {
		t.Errorf("allow: base %v, first %v, second %v", base.Claude.Allow, first.Claude.Allow, second.Claude.Allow)
	}
}

func TestMergeWithDoesNotMutateTheBaseGit(t *testing.T) {
	base := Sweatfile{Git: &Git{Excludes: withSpare("a")}}
	first := base.MergeWith(Sweatfile{Git: &Git{Excludes: []string{"b"}}})
	second := base.MergeWith(Sweatfile{Git: &Git{Excludes: []string{"c"}}})
	if !slices.Equal(base.Git.Excludes, []string{"a"}) || !slices.Equal(first.Git.Excludes, []string{"a", "b"}) || !slices.Equal(second.Git.Excludes, []string{"a", "c"}) {
		t.Errorf("excludes: base %v, first %v, second %v", base.Git.Excludes, first.Git.Excludes, second.Git.Excludes)
	}
}

func TestMergeWithDoesNotMutateTheBaseDirenv(t *testing.T) {
	base := Sweatfile{Direnv: &Direnv{Envrc: withSpare("a"), Dotenv: map[string]string{"K": "base"}}}
	first := base.MergeWith(Sweatfile{Direnv: &Direnv{Envrc: []string{"b"}, Dotenv: map[string]string{"ONE": "1"}}})
	second := base.MergeWith(Sweatfile{Direnv: &Direnv{Envrc: []string{"c"}, Dotenv: map[string]string{"K": "two"}}})
	if !slices.Equal(base.Direnv.Envrc, []string{"a"}) || !slices.Equal(first.Direnv.Envrc, []string{"a", "b"}) || !slices.Equal(second.Direnv.Envrc, []string{"a", "c"}) {
		t.Errorf("envrc: base %v, first %v, second %v", base.Direnv.Envrc, first.Direnv.Envrc, second.Direnv.Envrc)
	}
	if !maps.Equal(base.Direnv.Dotenv, map[string]string{"K": "base"}) ||
		!maps.Equal(first.Direnv.Dotenv, map[string]string{"K": "base", "ONE": "1"}) ||
		!maps.Equal(second.Direnv.Dotenv, map[string]string{"K": "two"}) {
		t.Errorf("dotenv: base %v, first %v, second %v", base.Direnv.Dotenv, first.Direnv.Dotenv, second.Direnv.Dotenv)
	}
}

func TestMergeWithDoesNotMutateTheBaseSysprompt(t *testing.T) {
	intp := func(n int) *int { return &n }
	base := Sweatfile{Sysprompt: &Sysprompt{ManIndex: []string{"base"}}}
	first := base.MergeWith(Sweatfile{Sysprompt: &Sysprompt{IndexLimit: intp(5)}})
	second := base.MergeWith(Sweatfile{Sysprompt: &Sysprompt{RepoIndex: []string{"r"}}})
	if base.Sysprompt.IndexLimit != nil || base.Sysprompt.RepoIndex != nil {
		t.Errorf("the base was mutated: %+v", *base.Sysprompt)
	}
	if second.Sysprompt.IndexLimit != nil || first.Sysprompt.RepoIndex != nil {
		t.Errorf("one merge carries the other's override: first %+v, second %+v", *first.Sysprompt, *second.Sysprompt)
	}
	if !slices.Equal(second.Sysprompt.ManIndex, []string{"base"}) {
		t.Errorf("second merge lost the base man-index: %+v", *second.Sysprompt)
	}
}

func TestMergeWithDoesNotMutateTheBaseSessionEntry(t *testing.T) {
	base := Sweatfile{SessionEntry: &SessionEntry{
		Start: []string{"base"}, Env: map[string]string{"G": "base"},
		ModelFlags: map[string]string{"claude": "--model"}, ModelIDs: map[string]string{"opus": "base"},
	}}
	first := base.MergeWith(Sweatfile{SessionEntry: &SessionEntry{Start: []string{"one"}, Env: map[string]string{"ONE": "1"}}})
	second := base.MergeWith(Sweatfile{SessionEntry: &SessionEntry{
		ModelIDs: map[string]string{"opus": "two"}, ModelFlags: map[string]string{"other": "-m"},
	}})
	if !slices.Equal(base.SessionEntry.Start, []string{"base"}) ||
		!maps.Equal(base.SessionEntry.Env, map[string]string{"G": "base"}) ||
		!maps.Equal(base.SessionEntry.ModelFlags, map[string]string{"claude": "--model"}) ||
		!maps.Equal(base.SessionEntry.ModelIDs, map[string]string{"opus": "base"}) {
		t.Errorf("the base was mutated: %+v", *base.SessionEntry)
	}
	if !slices.Equal(second.SessionEntry.Start, []string{"base"}) || !maps.Equal(second.SessionEntry.Env, map[string]string{"G": "base"}) {
		t.Errorf("the second merge carries the first override: %+v", *second.SessionEntry)
	}
	if !maps.Equal(first.SessionEntry.ModelIDs, map[string]string{"opus": "base"}) ||
		!maps.Equal(first.SessionEntry.ModelFlags, map[string]string{"claude": "--model"}) {
		t.Errorf("the first merge carries the second override: %+v", *first.SessionEntry)
	}
}
