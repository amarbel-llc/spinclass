package bridgeserver

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
)

// recordingAdmit stands in for clown's peercheck.AdmitPeer: it records the file
// path it was handed and answers with err.
type recordingAdmit struct {
	mu    sync.Mutex
	files []string
	err   error
}

func (r *recordingAdmit) admit(_ net.Conn, file string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files = append(r.files, file)
	return r.err
}

func (r *recordingAdmit) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.files...)
}

// With no CheckPeer, the verdict is Admit's, made on the cgroup file's path.
func TestAdmitIsTheVerdictWhenNoCheckPeerIsGivenAndGetsTheFilePath(t *testing.T) {
	file := writeCgroupFile(t, "/user.slice/clown-agent.slice/"+testUnit)
	rec := &recordingAdmit{}
	dir := shortDir(t)
	s, err := Listen(dir, testUnit, Options{Admit: rec.admit, CgroupFile: file})
	if err != nil {
		t.Fatalf("Listen with Admit only: %v", err)
	}
	go func() { _ = s.Serve(context.Background()) }()
	t.Cleanup(func() { _ = s.Close() })

	c := dial(t, dir)
	send(t, c, "BRIDGE/1 control\n")
	if got := readN(t, c, 3); got != "OK\n" {
		t.Fatalf("an admitted peer got %q, want OK", got)
	}
	if calls := rec.calls(); len(calls) != 1 || calls[0] != file {
		t.Fatalf("Admit was handed %v, want exactly the cgroup file %q", calls, file)
	}

	rec.mu.Lock()
	rec.err = errors.New("peer is not in the unit")
	rec.mu.Unlock()
	c2 := dial(t, dir)
	send(t, c2, "BRIDGE/1 session\n")
	if got := readAll(t, c2); !strings.HasPrefix(got, "ERR refused ") {
		t.Fatalf("a refused peer got %q, want ERR refused", got)
	}
}

// A CheckPeer wins over Admit (tests inject fakes that way): Admit is not asked.
func TestACheckPeerWinsOverAdmit(t *testing.T) {
	file := writeCgroupFile(t, "/user.slice/clown-agent.slice/"+testUnit)
	rec := &recordingAdmit{err: errors.New("Admit must not be asked")}
	fake := &fakeExact{}
	dir := shortDir(t)
	s, err := Listen(dir, testUnit, Options{CheckPeer: fake.check, Admit: rec.admit, CgroupFile: file})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = s.Serve(context.Background()) }()
	t.Cleanup(func() { _ = s.Close() })

	c := dial(t, dir)
	send(t, c, "BRIDGE/1 control\n")
	if got := readN(t, c, 3); got != "OK\n" {
		t.Fatalf("got %q, want OK from the CheckPeer verdict", got)
	}
	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("Admit was asked despite a CheckPeer: %v", calls)
	}
}
