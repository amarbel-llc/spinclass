package git

import (
	"slices"
	"testing"
)

func TestInvocationUnregisteredIsUnchanged(t *testing.T) {
	argv, env := invocation("/not/registered", nil, []string{"status"})
	if !slices.Equal(argv, []string{"-C", "/not/registered", "status"}) || env != nil {
		t.Errorf("argv=%v env=%v, want the plain -C form and no env override", argv, env)
	}
	_, env = invocation("/not/registered", []string{"A=b"}, []string{"status"})
	if len(env) == 0 || env[len(env)-1] != "A=b" {
		t.Errorf("caller env must still be appended: %v", env)
	}
}

func TestConfineAppliesToDescendantsAndReleases(t *testing.T) {
	dir := t.TempDir()
	release := Confine(dir, Hardening{Env: []string{"X=1"}, Args: []string{"-c", "a.b=c"}})
	argv, env := invocation(dir+"/sub", nil, []string{"log"})
	if !slices.Equal(argv, []string{"-C", dir + "/sub", "-c", "a.b=c", "log"}) {
		t.Errorf("argv = %v", argv)
	}
	if env[len(env)-1] != "X=1" {
		t.Errorf("hardening env must come last so it wins: %v", env[len(env)-3:])
	}
	again := Confine(dir, Hardening{Env: []string{"X=1"}})
	release()
	if _, ok := HardeningFor(dir); !ok {
		t.Error("a nested registration must survive the first release")
	}
	again()
	if _, ok := HardeningFor(dir); ok {
		t.Error("still registered after every release")
	}
}
