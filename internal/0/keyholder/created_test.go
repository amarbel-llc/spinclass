package keyholder

import (
	"context"
	"testing"
)

// Created tells a caller whether THIS Start launched the pair, so a launch that
// fails afterwards stops only a pair it made, never one a running session uses.
func TestStartReportsCreatedOnlyForAPairItLaunched(t *testing.T) {
	l, f, _ := launcher(t)
	p1, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	if !p1.Created {
		t.Error("a freshly launched pair is not marked Created")
	}
	p2, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	if p2.Created {
		t.Error("a live pair that Start found and left alone is marked Created")
	}
	if rp, ok, err := l.Running(context.Background(), "s/a"); err != nil || !ok || rp.Created {
		t.Errorf("Running = (%+v, %v, %v), want a pair that is not Created", rp, ok, err)
	}
	// A lost pair that Start replaces is a new launch.
	f.mu.Lock()
	f.active[p1.AgentUnit] = false
	f.mu.Unlock()
	p3, err := l.Start(context.Background(), "s/a")
	if err != nil {
		t.Fatal(err)
	}
	if !p3.Created {
		t.Error("a replaced pair is not marked Created")
	}
}
