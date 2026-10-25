package grantstore

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/profilegrant"
)

// spinclass#377: BindNew is create-if-absent. A second binding for the same
// grantee is refused, and the first stays current.
func TestBindNewRefusesAnExistingBinding(t *testing.T) {
	s, _ := openTemp(t)
	first := recordOf(t, grant("op", "repo/child", "n1", "-", profilegrant.OnwardNone), false)
	second := recordOf(t, grant("op", "repo/child", "n2", "-", profilegrant.OnwardNone), false)
	for _, rec := range []profilegrant.Record{first, second} {
		if err := s.Put(rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.BindNew("repo/child", first.Digest); err != nil {
		t.Fatalf("first BindNew: %v", err)
	}
	if err := s.BindNew("repo/child", second.Digest); !errors.Is(err, ErrAlreadyBound) {
		t.Fatalf("second BindNew = %v, want ErrAlreadyBound", err)
	}
	if got, ok := s.Current("repo/child"); !ok || got.Digest != first.Digest {
		t.Errorf("Current = %s, %v; want the first record %s", got.Digest, ok, first.Digest)
	}
}

// Concurrent BindNew calls for one grantee: exactly one wins.
func TestConcurrentBindNewOneWinner(t *testing.T) {
	for round := 0; round < 20; round++ {
		s, _ := openTemp(t)
		const n = 8
		digests := make([]string, n)
		for i := range digests {
			rec := recordOf(t, grant("op", "repo/child", fmt.Sprintf("n%d", i), "-", profilegrant.OnwardNone), false)
			if err := s.Put(rec); err != nil {
				t.Fatal(err)
			}
			digests[i] = rec.Digest
		}
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := range digests {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = s.BindNew("repo/child", digests[i])
			}()
		}
		wg.Wait()
		wins := 0
		for _, err := range errs {
			switch {
			case err == nil:
				wins++
			case !errors.Is(err, ErrAlreadyBound):
				t.Fatalf("round %d: unexpected error %v", round, err)
			}
		}
		if wins != 1 {
			t.Fatalf("round %d: %d BindNew calls won, want exactly 1", round, wins)
		}
	}
}
