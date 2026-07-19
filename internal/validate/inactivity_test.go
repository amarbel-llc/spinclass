package validate

import (
	"testing"

	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

func TestCheckHooksInactivityTimeout(t *testing.T) {
	mk := func(v string) sweatfile.Sweatfile {
		return sweatfile.Sweatfile{Hooks: &sweatfile.Hooks{InactivityTimeout: &v}}
	}

	// Unparseable duration → one error issue.
	issues := CheckHooks(mk("nope"))
	if len(issues) != 1 || issues[0].Severity != SeverityError ||
		issues[0].Field != "hooks.inactivity-timeout" {
		t.Fatalf("expected one error on hooks.inactivity-timeout, got %+v", issues)
	}

	// Negative duration → error.
	if issues := CheckHooks(mk("-5s")); len(issues) != 1 || issues[0].Severity != SeverityError {
		t.Fatalf("expected error for negative duration, got %+v", issues)
	}

	// Valid duration → no issues.
	if issues := CheckHooks(mk("180s")); len(issues) != 0 {
		t.Fatalf("expected no issues for valid duration, got %+v", issues)
	}

	// Empty / unset → no issues.
	if issues := CheckHooks(mk("")); len(issues) != 0 {
		t.Fatalf("expected no issues for empty value, got %+v", issues)
	}
}
