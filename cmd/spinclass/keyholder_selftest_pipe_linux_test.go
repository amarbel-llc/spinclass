package main

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

// `sc keyholder-selftest | head -1`: a write to a standard output nobody reads
// must cancel the self-test, so the pair is stopped. Uncaught, the Go runtime
// kills the process on that write (here: this test binary).
func TestTheSelftestSurvivesAReaderThatWentAway(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	saved, err := syscall.Dup(1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := selftestContext(context.Background())
	defer stop()
	if err := syscall.Dup3(int(w.Fd()), 1, 0); err != nil {
		t.Fatal(err)
	}
	_, werr := os.Stdout.Write([]byte("keyholder-selftest: ok preconditions\n"))
	restore := syscall.Dup3(saved, 1, 0)
	_ = syscall.Close(saved)
	_ = w.Close()
	if restore != nil {
		t.Fatal(restore)
	}
	if werr == nil {
		t.Fatal("the write to a closed pipe succeeded: this proves nothing")
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Error("the broken pipe did not cancel the self-test")
	}
}
