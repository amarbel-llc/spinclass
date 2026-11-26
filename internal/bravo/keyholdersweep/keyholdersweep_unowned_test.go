package keyholdersweep

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"code.linenisgreat.com/spinclass/internal/0/keyholder"
)

// Units this state root recorded nothing for are not its leftovers: they are
// never an orphan, whatever their age, so a sweep never stops them.
func TestClassifyNeverCallsAnotherStateRootsUnitsAnOrphan(t *testing.T) {
	got := Classify([]keyholder.Found{{Hash: "0123456789abcdef", Units: []string{"u"}, Unowned: true}}, map[string]bool{}, 0)
	if len(got) != 1 || got[0].Orphan || !strings.Contains(got[0].Why, "not mine") || !got[0].Unowned {
		t.Fatalf("Classify = %+v, want not an orphan, Unowned, 'not mine'", got)
	}
}

// accountUnits is a runner whose systemd lists units that no directory of this
// state root records, as it does for a shell under another XDG_STATE_HOME.
type accountUnits struct{ units []string }

func (accountUnits) Start(context.Context, string, []string, []string) error { return nil }
func (a accountUnits) Active(_ context.Context, u string) (bool, error) {
	for _, x := range a.units {
		if x == u {
			return true, nil
		}
	}
	return false, nil
}
func (accountUnits) Stop(context.Context, string) error                         { return nil }
func (accountUnits) Output(context.Context, []string, []string) ([]byte, error) { return nil, nil }
func (a accountUnits) ListUnits(context.Context) ([]string, error)              { return a.units, nil }

// `sc clean` reads Orphans: with every unit of the account listed and nothing
// recorded under this state root, none of them may come back as an orphan.
func TestOrphansNeverReturnsUnitsThisStateRootDidNotRecord(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	prev := NewLauncher
	t.Cleanup(func() { NewLauncher = prev })
	h := "0123456789abcdef"
	NewLauncher = func() *keyholder.Launcher {
		return &keyholder.Launcher{
			Base: filepath.Join(t.TempDir(), "kh"),
			Run:  accountUnits{units: []string{"spinclass-keyholder-agent-" + h + ".service", "spinclass-keyholder-fibby-" + h + ".service"}},
		}
	}
	orphans, err := Orphans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 0 {
		t.Fatalf("Orphans = %+v, want none: the units are another state root's", orphans)
	}
	pairs, err := Survey(context.Background())
	if err != nil || len(pairs) != 1 || !pairs[0].Unowned {
		t.Fatalf("Survey = %+v (%v), want the one pair reported Unowned", pairs, err)
	}
}
