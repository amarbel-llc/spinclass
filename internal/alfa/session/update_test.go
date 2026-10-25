package session

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestUpdateSerialisesConcurrentWriters(t *testing.T) {
	s := setupTestSession(t, "upd")
	s.Description = "0"
	if err := Write(s); err != nil {
		t.Fatal(err)
	}

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- Update(s.RepoPath, s.Branch, func(st *State) error {
				v, err := strconv.Atoi(st.Description)
				if err != nil {
					return err
				}
				st.Description = strconv.Itoa(v + 1)
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	got, err := Read(s.RepoPath, s.Branch)
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != strconv.Itoa(n) {
		t.Errorf("Description = %q, want %d (lost update)", got.Description, n)
	}
}

func TestWriteIsAtomic(t *testing.T) {
	s := setupTestSession(t, "atomic")
	s.Description = "hello"
	if err := Write(s); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.WorktreePath, ".spinclass"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
	got, err := Read(s.RepoPath, s.Branch)
	if err != nil {
		t.Fatalf("state does not parse: %v", err)
	}
	if got.Description != "hello" {
		t.Errorf("Description = %q", got.Description)
	}
}
