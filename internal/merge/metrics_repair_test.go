package merge

import (
	"context"
	"fmt"
	"testing"

	"code.linenisgreat.com/spinclass/internal/hookrun"
	"code.linenisgreat.com/spinclass/internal/statsd"
)

// A repair killed by its own cap reads merge.<repo>.repair.timeout, not failed
// (#401), even through runRepairPhase's wrapping.
func TestOutcomeOfARepairTimeoutIsTimeout(t *testing.T) {
	err := fmt.Errorf("repair hook failed: %w", hookrun.ErrRepairTimeout)
	if got := outcomeOf(context.Background(), err); got != statsd.OutcomeTimeout {
		t.Errorf("outcomeOf(repair timeout) = %q, want %q", got, statsd.OutcomeTimeout)
	}
}
