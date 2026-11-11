package hostqueue

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"code.linenisgreat.com/spinclass/internal/0/sweatfile"
)

// ConfigFrom turns the trusted [host-queue] keys (sweatfileio.TrustedHostQueue)
// into a Config over DefaultConfig. enabled is false unless `enable = true`:
// the queue is opt-in (operator ruling, 2026-11-11). Every invalid key is
// reported in err, and an invalid enabled config is not used: the caller leaves
// the queue off rather than guess a threshold.
func ConfigFrom(hq sweatfile.HostQueue) (cfg Config, enabled bool, err error) {
	cfg = DefaultConfig()
	var errs []error
	size := func(key string, v *string, dst *uint64) {
		if v == nil {
			return
		}
		n, perr := ParseSize(*v)
		if perr != nil {
			errs = append(errs, fmt.Errorf("[host-queue] %s: %w", key, perr))
			return
		}
		*dst = n
	}
	duration := func(key string, v *string, dst *time.Duration, allowZero bool) {
		if v == nil {
			return
		}
		d, perr := time.ParseDuration(*v)
		switch {
		case perr != nil:
			errs = append(errs, fmt.Errorf("[host-queue] %s: %q is not a Go duration", key, *v))
		case d < 0 || (d == 0 && !allowZero):
			errs = append(errs, fmt.Errorf("[host-queue] %s: %q must be positive", key, *v))
		default:
			*dst = d
		}
	}
	nonNegative := func(key string, v *float64, dst *float64) {
		if v == nil {
			return
		}
		if *v < 0 {
			errs = append(errs, fmt.Errorf("[host-queue] %s: %v must not be negative", key, *v))
			return
		}
		*dst = *v
	}

	if hq.MaxGates != nil {
		if *hq.MaxGates < 1 {
			errs = append(errs, fmt.Errorf("[host-queue] max-gates: %d must be at least 1", *hq.MaxGates))
		} else {
			cfg.MaxGates = *hq.MaxGates
		}
	}
	size("memory-floor", hq.MemoryFloor, &cfg.MemoryFloor)
	size("memory-resume", hq.MemoryResume, &cfg.MemoryResume)
	size("swap-ceiling", hq.SwapCeiling, &cfg.SwapCeiling)
	size("disk-floor", hq.DiskFloor, &cfg.DiskFloor)
	nonNegative("pressure-memory-avg10", hq.PressureMemoryAvg10, &cfg.PressureAvg10)
	nonNegative("load-threshold", hq.LoadThreshold, &cfg.LoadThreshold)
	duration("settle", hq.Settle, &cfg.Settle, true)
	duration("poll", hq.Poll, &cfg.Poll, false)
	duration("max-wait", hq.MaxWait, &cfg.MaxWait, true)
	duration("idle-grace", hq.IdleGrace, &cfg.IdleGrace, true)
	if cfg.MemoryResume < cfg.MemoryFloor {
		errs = append(errs, fmt.Errorf("[host-queue] memory-resume (%s) is below memory-floor (%s)", gibString(cfg.MemoryResume), gibString(cfg.MemoryFloor)))
	}

	enabled = hq.Enable != nil && *hq.Enable
	return cfg, enabled, errors.Join(errs...)
}

var sizeUnits = []struct {
	suffix string
	factor uint64
}{
	{"TiB", 1 << 40},
	{"GiB", 1 << 30},
	{"MiB", 1 << 20},
	{"KiB", 1 << 10},
	{"TB", 1e12},
	{"GB", 1e9},
	{"MB", 1e6},
	{"KB", 1e3},
	{"B", 1},
}

// ParseSize reads a byte size: a number with an optional unit (KiB, MiB, GiB,
// TiB, their decimal KB to TB, or B), such as "4GiB" or "512 MiB".
func ParseSize(s string) (uint64, error) {
	t := strings.TrimSpace(s)
	factor := uint64(1)
	for _, u := range sizeUnits {
		if rest, ok := strings.CutSuffix(t, u.suffix); ok {
			t, factor = strings.TrimSpace(rest), u.factor
			break
		}
	}
	n, err := strconv.ParseFloat(t, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q is not a size (like \"4GiB\")", s)
	}
	return uint64(n * float64(factor)), nil
}
