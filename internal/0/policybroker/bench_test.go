package policybroker

import (
	"context"
	"slices"
	"testing"
	"time"
)

// BenchmarkRoundTrip is one check over the unix socket against a stand-in
// client: encode, write, the broker's read and dispatch, an evaluator that
// answers at once, the verdict's write and the client's read. The
// evaluator's own cost is measured in folioeval; the two add up to what the
// client's deadline (moxy's default 100 ms) must cover. p50 and p99 are
// reported beside the mean.
func BenchmarkRoundTrip(b *testing.B) {
	path := b.TempDir() + "/b.sock"
	broker, err := Listen(Options{
		Path: path, SessionKey: testKey, BrokerName: "bench",
		Evaluator: EvaluatorFunc(func(_ context.Context, _ Check) (string, string, error) {
			return VerdictAllow, "bench", nil
		}),
	})
	if err != nil {
		b.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go broker.Serve(ctx)
	defer broker.Close()

	c := dial(b, broker)
	c.send(Message{V: Version, Type: TypeHello, Client: "bench", Capabilities: []string{CapCheck}})
	if h, err := c.read(); err != nil || h.Type != TypeHello {
		b.Fatalf("hello: %+v %v", h, err)
	}
	check := fileCheck(0, testKey, OpRead, "/w/repo/.worktrees/kid/.spinclass/spawn.log")
	durations := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := int64(1); b.Loop(); i++ {
		check.ID = i
		start := time.Now()
		c.send(check)
		m, err := c.read()
		durations = append(durations, time.Since(start))
		if err != nil || m.Type != TypeVerdict || m.ID != i {
			b.Fatalf("round trip %d: %+v %v", i, m, err)
		}
	}
	slices.Sort(durations)
	at := func(q float64) float64 {
		return float64(durations[int(q*float64(len(durations)-1))].Nanoseconds())
	}
	b.ReportMetric(at(0.50), "p50-ns/op")
	b.ReportMetric(at(0.99), "p99-ns/op")
}
