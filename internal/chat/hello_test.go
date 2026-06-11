package chat

import (
	"strings"
	"testing"
	"time"
)

func TestHelloRoundTrip(t *testing.T) {
	setChatroom(t)
	const (
		driver = "spinclass/bright-cedar"
		worker = "otherrepo/calm-willow"
	)

	if err := SendHello(worker, driver); err != nil {
		t.Fatalf("SendHello: %v", err)
	}

	start := time.Now()
	if err := WaitForHello(driver, worker, time.Now().Add(-time.Second), 2*time.Second); err != nil {
		t.Fatalf("WaitForHello: %v", err)
	}
	// The hello was already present, so the immediate pre-tick check must
	// satisfy the wait well before the first 250ms poll tick.
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("WaitForHello took %v; expected the immediate check to succeed", elapsed)
	}
}

func TestWaitForHelloTimesOut(t *testing.T) {
	setChatroom(t)
	const (
		driver = "spinclass/bright-cedar"
		worker = "otherrepo/calm-willow"
	)

	deadline := 300 * time.Millisecond
	start := time.Now()
	err := WaitForHello(driver, worker, time.Now(), deadline)
	if err == nil {
		t.Fatal("WaitForHello: expected deadline error, got nil")
	}
	if !strings.Contains(err.Error(), deadline.String()) {
		t.Errorf("error %q does not mention the deadline %s", err, deadline)
	}
	if !strings.Contains(err.Error(), worker) {
		t.Errorf("error %q does not mention the worker key %q", err, worker)
	}
	if elapsed := time.Since(start); elapsed > deadline+500*time.Millisecond {
		t.Errorf("WaitForHello took %v; expected prompt return near the %s deadline", elapsed, deadline)
	}
}

func TestWaitForHelloIgnoresOlder(t *testing.T) {
	setChatroom(t)
	const (
		driver = "spinclass/bright-cedar"
		worker = "otherrepo/calm-willow"
	)

	if err := SendHello(worker, driver); err != nil {
		t.Fatalf("SendHello: %v", err)
	}

	// since is in the future of the send: the existing hello must not
	// satisfy the gate, so the wait times out.
	since := time.Now().Add(time.Hour)
	err := WaitForHello(driver, worker, since, 300*time.Millisecond)
	if err == nil {
		t.Fatal("WaitForHello: a pre-since hello must not satisfy the gate")
	}
}

func TestWaitForHelloIgnoresOtherSubjects(t *testing.T) {
	setChatroom(t)
	const (
		driver = "spinclass/bright-cedar"
		worker = "otherrepo/calm-willow"
	)

	mustSend(t, Message{
		From: worker, To: driver,
		Subject: "status update", Body: "still working",
	})

	err := WaitForHello(driver, worker, time.Now().Add(-time.Second), 300*time.Millisecond)
	if err == nil {
		t.Fatal("WaitForHello: a non-hello message must not satisfy the gate")
	}
}

func TestWaitForHelloDoesNotAdvanceCursor(t *testing.T) {
	setChatroom(t)
	const (
		driver = "spinclass/bright-cedar"
		worker = "otherrepo/calm-willow"
	)

	if err := SendHello(worker, driver); err != nil {
		t.Fatalf("SendHello: %v", err)
	}
	if err := WaitForHello(driver, worker, time.Now().Add(-time.Second), 2*time.Second); err != nil {
		t.Fatalf("WaitForHello: %v", err)
	}

	// The wait used peek reads only: the driver's own cursor is untouched,
	// so a normal read still surfaces the hello.
	got, err := Read(driver, ReadFilter{}, false)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected the hello to still be unread, got %d messages: %+v", len(got), got)
	}
	if !strings.HasPrefix(got[0].Subject, HelloSubject) {
		t.Errorf("unexpected message surfaced: %+v", got[0])
	}
}

func TestSendHelloSubjectWithinCap(t *testing.T) {
	// WaitForHello keys on the subject; SendHello builds it as
	// HelloSubject + " " + from. Verify a realistically long session key
	// (~80 chars) keeps the subject valid under ValidateSubject.
	from := strings.Repeat("a", 40) + "/" + strings.Repeat("b", 39)
	subject := HelloSubject + " " + from
	if err := ValidateSubject(subject); err != nil {
		t.Errorf("ValidateSubject(%q): %v", subject, err)
	}
}
