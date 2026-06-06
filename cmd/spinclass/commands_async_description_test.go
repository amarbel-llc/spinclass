package main

import (
	"strings"
	"testing"
)

func TestBuildMergeAsyncDescription_NoClownKeepsPollGuidance(t *testing.T) {
	got := buildMergeAsyncDescription("just", false)
	if !strings.Contains(got, "Do NOT pick async") {
		t.Fatalf("no-clown description lost the anti-polling guidance: %q", got)
	}
	if strings.Contains(got, "job-wakeup") {
		t.Fatalf("no-clown description mentions the wake it cannot deliver: %q", got)
	}
	if !strings.Contains(got, "`just`") {
		t.Fatalf("description lost the hook preview: %q", got)
	}
}

func TestBuildMergeAsyncDescription_ClownWakeGuidance(t *testing.T) {
	got := buildMergeAsyncDescription("just", true)
	if !strings.Contains(got, "job-wakeup") {
		t.Fatalf("clown description missing wake guidance: %q", got)
	}
	if strings.Contains(got, "Do NOT pick async") {
		t.Fatalf("clown description kept the anti-async warning that no longer applies: %q", got)
	}
	if !strings.Contains(got, "`just`") {
		t.Fatalf("description lost the hook preview: %q", got)
	}
	if !strings.Contains(got, "task list is the test") {
		t.Fatalf("clown description missing the task-list decision clause: %q", got)
	}
}

func TestBuildCheckAsyncDescription_NoClownKeepsPollGuidance(t *testing.T) {
	got := buildCheckAsyncDescription("", false)
	if !strings.Contains(got, "Do NOT pick async") {
		t.Fatalf("no-clown description lost the anti-polling guidance: %q", got)
	}
}

func TestBuildCheckAsyncDescription_ClownWakeGuidance(t *testing.T) {
	got := buildCheckAsyncDescription("", true)
	if !strings.Contains(got, "job-wakeup") {
		t.Fatalf("clown description missing wake guidance: %q", got)
	}
	if strings.Contains(got, "Do NOT pick async") {
		t.Fatalf("clown description kept the anti-async warning: %q", got)
	}
	if !strings.Contains(got, "task list is the test") {
		t.Fatalf("clown description missing the task-list decision clause: %q", got)
	}
}
