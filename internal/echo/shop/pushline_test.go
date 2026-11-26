package shop

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
	"code.linenisgreat.com/spinclass/internal/alfa/sweatfileio"
	"code.linenisgreat.com/spinclass/internal/charlie/auth"
	"code.linenisgreat.com/spinclass/internal/charlie/executor"
)

// An origin that is no forge URL is never quoted back: it may carry userinfo.
func TestThePushStatusNeverQuotesAnOriginItCouldNotParse(t *testing.T) {
	a := setupAuthRepo(t)
	writeFile(t, a.parentLayer(), "[auth]\nmint-command = \"echo tok\"\nrevoke-command = \"true\"\n")
	if out, err := exec.Command("git", "-C", a.repo, "remote", "set-url", "origin", "/srv/userinfo-marker/repo").CombinedOutput(); err != nil {
		t.Fatalf("set-url: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", a.repo, "worktree", "add", "-b", "feature-x", a.rp.AbsPath).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
	h, err := sweatfileio.LoadHierarchy(a.root, a.rp.AbsPath)
	if err != nil {
		t.Fatal(err)
	}
	var push auth.PushStatus
	if err := ProvisionCredential(context.Background(), nil, h, a.rp, false, &push); err != nil {
		t.Fatalf("ProvisionCredential: %v", err)
	}
	if push.Kind != auth.NoForgeOrigin || strings.Contains(push.Line(true, "repo/feature-x"), "userinfo-marker") {
		t.Errorf("kind %q; the line quotes the origin: %v", push.Kind, strings.Contains(push.Line(true, "repo/feature-x"), "userinfo-marker"))
	}
}

// A start and the resume after it each print one push token line on stderr, and
// the line follows the environment the entrypoint is really given.
func TestStartAndResumeSayWhetherThereIsAPushToken(t *testing.T) {
	rp, _ := startWorkerWithPair(t, &fakeUnits{})
	var said bytes.Buffer
	prev := PushLineWriter
	PushLineWriter = func() io.Writer { return &said }
	t.Cleanup(func() { PushLineWriter = prev })

	attach := func(exec executor.Executor) (line, stdout string) {
		t.Helper()
		said.Reset()
		var out bytes.Buffer
		if err := Attach(&out, exec, rp, sweatfile.Sweatfile{}, "tap", false, true /*noAttach*/, false, false, false); err != nil {
			t.Fatalf("Attach: %v\n%s", err, out.String())
		}
		return said.String(), out.String()
	}

	line, stdout := attach(executor.SessionExecutor{Entrypoint: []string{"true"}})
	// The fixture has no origin: spinclass says it does not know how a push goes.
	if line != "spinclass: push_token: no ([auth] mint-command is not set); spinclass could not read origin's push URL, so it does not know whether a push works\n" {
		t.Errorf("a start without a holder said %q", line)
	}
	if strings.Contains(stdout, "push_token") {
		t.Errorf("the line reached stdout:\n%s", stdout)
	}

	line, _ = attach(executor.SessionExecutor{Entrypoint: []string{"true"}, Env: map[string]string{keyholder.CertEnv: "/pair/cert/cert.pub"}})
	for _, want := range []string{"spinclass: push_token: no (none was minted when the session was created); ", ". next: ", "`sc close repo/worker`"} {
		if !strings.Contains(line, want) {
			t.Errorf("a resume on a holder said %q, lacks %q", line, want)
		}
	}
}
