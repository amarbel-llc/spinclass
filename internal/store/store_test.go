package store

import (
	"errors"
	"testing"
)

func TestClassOfFailsClosed(t *testing.T) {
	if ClassOf("nope", "nope") != ClassFloor {
		t.Fatal("unknown verb must be ClassFloor")
	}
	if ClassOf("stage", "status") != ClassRead || ClassOf("stage", "add") != ClassWrite || ClassOf("worktree", "remove") != ClassFloor {
		t.Fatal("verb class table mismatch")
	}
}

func TestStaticVerdict(t *testing.T) {
	if StaticVerdict(Op{Tool: "tag", Subcommand: "list"}) != Allow {
		t.Fatal("read verb must Allow")
	}
	if StaticVerdict(Op{Tool: "tag", Subcommand: "create"}) != Ask {
		t.Fatal("write verb must Ask")
	}
	var zero Verdict
	if zero != Ask {
		t.Fatal("zero Verdict must be Ask")
	}
}

func TestCapabilitiesSupportsAndUnsupported(t *testing.T) {
	c := Capabilities{"tag": {"list"}}
	if !c.Supports("tag", "list") || c.Supports("tag", "create") || c.Supports("x", "list") {
		t.Fatal("Supports mismatch")
	}
	if err := Unsupported(Op{Tool: "tag", Subcommand: "create"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}
