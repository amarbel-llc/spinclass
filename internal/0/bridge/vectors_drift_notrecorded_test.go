//go:build !clownvectors

package bridge

import "testing"

// The drift test against clown's recorded vector digests
// (vectors_drift_clown_test.go, build tag clownvectors) cannot compile against
// the clown the flake locks today, which has no such record. Say so in the test
// log instead of staying silent; the bump checklist issue turns the real test on.
func TestVectorsDriftAgainstTheLockedClown(t *testing.T) {
	t.Skip("locked clown has no vectors record yet (needs clown ed73ff6 or later); the clownvectors drift test is off until the lock is bumped")
}
