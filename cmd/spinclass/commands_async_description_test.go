package main

import (
	"strings"
	"testing"
)

// The async tools are registered only under clown (registerMCPOnlyCommands), so
// the description always documents the job-wakeup contract — there is no
// clown-absent variant. Inspection points at ringmaster's own surfaces, and the
// retired session-job-status must never reappear.
func TestBuildMergeAsyncDescription(t *testing.T) {
	got := buildMergeAsyncDescription("just")
	if !strings.Contains(got, "job-wakeup") {
		t.Fatalf("description missing wake guidance: %q", got)
	}
	if !strings.Contains(got, "task list is the test") {
		t.Fatalf("description missing the task-list decision clause: %q", got)
	}
	if !strings.Contains(got, "job_status") {
		t.Fatalf("description should point inspection at ringmaster's job_status: %q", got)
	}
	if strings.Contains(got, "session-job-status") {
		t.Fatalf("description references the retired session-job-status: %q", got)
	}
	if !strings.Contains(got, "`just`") {
		t.Fatalf("description lost the hook preview: %q", got)
	}
}

func TestBuildCheckAsyncDescription(t *testing.T) {
	got := buildCheckAsyncDescription("")
	if !strings.Contains(got, "job-wakeup") {
		t.Fatalf("description missing wake guidance: %q", got)
	}
	if !strings.Contains(got, "task list is the test") {
		t.Fatalf("description missing the task-list decision clause: %q", got)
	}
	if !strings.Contains(got, "job_status") {
		t.Fatalf("description should point inspection at ringmaster's job_status: %q", got)
	}
	if strings.Contains(got, "session-job-status") {
		t.Fatalf("description references the retired session-job-status: %q", got)
	}
}
