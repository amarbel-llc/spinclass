package hungstatus

import "testing"

func TestStartTicksReadsField22AndAgreesWithFrontendState(t *testing.T) {
	root := fakeProc(t, 42, "777")
	got, ok := StartTicks(root, 42)
	if !ok || got != 777 {
		t.Fatalf("StartTicks = %d, %v, want 777, true", got, ok)
	}
	if st := FrontendState(root, 42, got); st != FrontendLive {
		t.Errorf("the ticks StartTicks read are not live for FrontendState: %v", st)
	}
}

func TestStartTicksSaysNoForAPidItCannotRead(t *testing.T) {
	root := fakeProc(t, 42, "777")
	for name, pid := range map[string]int{"no such pid": 43, "zero": 0, "negative": -1} {
		if got, ok := StartTicks(root, pid); ok || got != 0 {
			t.Errorf("%s: StartTicks = %d, %v, want 0, false", name, got, ok)
		}
	}
}
