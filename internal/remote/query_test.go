package remote

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"code.linenisgreat.com/spinclass/internal/session"
	"code.linenisgreat.com/spinclass/internal/sweatfile"
)

// stubSSH writes an executable `ssh` shell script into a temp dir prepended
// to PATH, so QueryHost's exec resolves to it. The script records its argv
// (one element per line, appended) into argsFile, then runs body — canned
// JSON on stdout, stderr noise, non-zero exits, or a sleep, per test case.
// Mirrors internal/clown/clown_test.go's stubClown.
func stubSSH(t *testing.T, argsFile, body string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "ssh")
	content := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + argsFile + "\n" + body
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write stub ssh: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// recordedArgs reads back the argv lines the stub recorded.
func recordedArgs(t *testing.T, argsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// cannedRows is the fixture row set the happy-path stubs serve and the
// cache tests round-trip.
func cannedRows() []session.ListRow {
	return []session.ListRow{
		{
			ID:          "crisp-catalpa",
			SessionKey:  "spinclass/crisp-catalpa",
			State:       "active",
			Description: "fix login bug",
			Repo:        "spinclass",
		},
		{
			ID:          "molten-mango",
			SessionKey:  "clown/molten-mango",
			State:       "inactive",
			Description: "",
			Repo:        "clown",
		},
	}
}

const cannedJSON = `[{"id":"crisp-catalpa","session_key":"spinclass/crisp-catalpa","state":"active","description":"fix login bug","repo":"spinclass"},{"id":"molten-mango","session_key":"clown/molten-mango","state":"inactive","description":"","repo":"clown"}]`

func TestQueryHostParsesRowsAndArgv(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	stubSSH(t, argsFile, "echo '"+cannedJSON+"'\nexit 0\n")

	r := sweatfile.Remote{Name: "devbox", SSH: "sasha@devbox.lan"}
	rows, err := QueryHost(context.Background(), r)
	if err != nil {
		t.Fatalf("QueryHost: %v", err)
	}
	if want := cannedRows(); !reflect.DeepEqual(rows, want) {
		t.Errorf("rows: got %+v, want %+v", rows, want)
	}
	// The stub records everything after argv[0], i.e. ssh's arguments.
	want := []string{"sasha@devbox.lan", "spinclass", "list", "--format", "json"}
	if got := recordedArgs(t, argsFile); !reflect.DeepEqual(got, want) {
		t.Errorf("ssh argv: got %q, want %q", got, want)
	}
}

func TestQueryHostDestDefaultsToName(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	stubSSH(t, argsFile, "echo '[]'\nexit 0\n")

	if _, err := QueryHost(context.Background(), sweatfile.Remote{Name: "devbox"}); err != nil {
		t.Fatalf("QueryHost: %v", err)
	}
	want := []string{"devbox", "spinclass", "list", "--format", "json"}
	if got := recordedArgs(t, argsFile); !reflect.DeepEqual(got, want) {
		t.Errorf("ssh argv: got %q, want %q", got, want)
	}
}

func TestQueryHostFailureSurfacesStderr(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	stubSSH(t, argsFile, "echo 'boom detail' >&2\nexit 1\n")

	rows, err := QueryHost(context.Background(), sweatfile.Remote{Name: "devbox"})
	if err == nil {
		t.Fatal("failing ssh: want error, got nil")
	}
	if !strings.Contains(err.Error(), "boom detail") {
		t.Errorf("error missing stderr detail: %v", err)
	}
	if rows != nil {
		t.Errorf("failing ssh: want nil rows, got %+v", rows)
	}
}

func TestQueryHostGarbageJSONErrors(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	stubSSH(t, argsFile, "echo 'spinclass: command not found'\nexit 0\n")

	rows, err := QueryHost(context.Background(), sweatfile.Remote{Name: "devbox"})
	if err == nil {
		t.Fatal("garbage stdout: want error, got nil")
	}
	if rows != nil {
		t.Errorf("garbage stdout: want nil rows, got %+v", rows)
	}
}

func TestQueryHostHonorsContextDeadline(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	// exec so the sleeper itself is the process CommandContext kills —
	// a forked child would keep the output pipes open past the kill.
	stubSSH(t, argsFile, "exec sleep 10\n")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := QueryHost(ctx, sweatfile.Remote{Name: "devbox"})
	if err == nil {
		t.Fatal("deadline exceeded: want error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("caller deadline not honored: took %v", elapsed)
	}
}

func TestCacheRoundTrip(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	rows := cannedRows()
	if err := WriteCache("devbox", rows); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateHome, "spinclass", "remotes", "devbox.json")); err != nil {
		t.Fatalf("cache file location: %v", err)
	}
	got, err := ReadCache("devbox")
	if err != nil {
		t.Fatalf("ReadCache: %v", err)
	}
	if !reflect.DeepEqual(got, rows) {
		t.Errorf("round-trip: got %+v, want %+v", got, rows)
	}
}

func TestReadCacheMissingIsEmpty(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	rows, err := ReadCache("never-listed")
	if err != nil {
		t.Fatalf("missing cache: want nil error, got %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("missing cache: want empty rows, got %+v", rows)
	}
}

func TestReadAllCaches(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	rows := cannedRows()
	if err := WriteCache("devbox", rows); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	remotes := []sweatfile.Remote{
		{Name: "devbox", SSH: "sasha@devbox.lan"},
		{Name: "lab"}, // no cache file: silently empty
	}
	got := ReadAllCaches(remotes)
	if len(got) != 2 {
		t.Fatalf("ReadAllCaches: got %d entries (%+v), want 2", len(got), got)
	}
	if !reflect.DeepEqual(got["devbox"], rows) {
		t.Errorf("devbox rows: got %+v, want %+v", got["devbox"], rows)
	}
	if len(got["lab"]) != 0 {
		t.Errorf("lab rows: want empty, got %+v", got["lab"])
	}
}
