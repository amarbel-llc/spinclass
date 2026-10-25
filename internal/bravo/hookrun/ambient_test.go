package hookrun

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCommandCaptureAmbientSplitsStreams(t *testing.T) {
	dir := t.TempDir()

	out, err := CommandCaptureAmbient(context.Background(), dir, "echo out; echo err >&2", nil)
	if err != nil || out != "out\n" {
		t.Fatalf("stdout/stderr split: out=%q err=%v", out, err)
	}

	_, err = CommandCaptureAmbient(context.Background(), dir, "echo out; echo boom >&2; exit 3", nil)
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("failure must fold stderr and exit status into the error, got %v", err)
	}

	out, err = CommandCaptureAmbient(context.Background(), dir, `printf %s "$FOO"`, []string{"FOO=bar"})
	if err != nil || out != "bar" {
		t.Fatalf("extraEnv: out=%q err=%v", out, err)
	}

	// `sleep 5` without exec leaves a grandchild holding the pipes, so this
	// exercises WaitDelay, not just the kill.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := CommandCaptureAmbient(ctx, dir, "sleep 5; true", nil); err == nil {
		t.Fatal("expected an error on timeout")
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("timeout took %v, WaitDelay did not bound it", d)
	}
}
