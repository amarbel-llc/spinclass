package spawn

// TEMPORARY (spinclass#384 stopgap, docs/plans/2026-11-12-spawn-allowlist-stopgap.md):
// delete with willallow.go when ajour plus profiles enforce this.

const workerNotDontAskReason = "the worker would not start in dontAsk, and the allow-list only covers dontAsk workers " +
	"(it needs [session-entry].unattended-permission-mode = \"dontAsk\" in a trusted sweatfile layer and a spawn-entry that takes it)"

// DiagnoseSpawnAllow explains why a spawn-session call that the always-ask
// floor is about to ask for was not passed by the TEMPORARY allow-list, for the
// ask text and the journal. ok is false when there is nothing to say: no valid
// entry lists the caller (or none exists), so the generic ask stands. It judges
// in the order the pass itself does, so the reason is the one that actually
// stopped it:
//
//   - a caller that cannot be identified, expired, or at its cap (an
//     unattended caller only; the list is not consulted for an attended one);
//   - then the worker: it must start in dontAsk (WillBeUnattended).
//
// It reads and writes nothing else and never turns a refusal into an allow.
func DiagnoseSpawnAllow(home, cwd, principal, repo string, driverUnattended bool) (AllowRefusal, bool) {
	ev := evaluateAllow(home, cwd, principal)
	if ev.refusal != nil {
		// A caller the list cannot be consulted for. Say so only when the list
		// matters here: the driver is unattended, and (when it could be read)
		// the list has entries at all.
		if driverUnattended && (ev.hasEntries || ev.refusal.Kind == RefusalNoRepo || ev.refusal.Kind == RefusalUnreadable) {
			return *ev.refusal, true
		}
		return AllowRefusal{}, false
	}
	if len(ev.matched) == 0 {
		return AllowRefusal{}, false // not listed: the generic ask stands
	}
	if driverUnattended {
		if len(ev.unexpired) == 0 {
			return *expiredRefusal(ev), true
		}
		pass := false
		for _, e := range ev.unexpired {
			if ev.running < e.MaxWorkers {
				pass = true
			}
		}
		if !pass {
			return *capRefusal(ev), true
		}
	}
	if WillBeUnattended(home, cwd, repo) {
		return AllowRefusal{}, false // the list would pass it; nothing refused
	}
	if !driverUnattended {
		return AllowRefusal{
			Kind:       RefusalDriverAttended,
			SessionKey: ev.key,
			Reason:     "this session is attended, so the spawn allow-list is not consulted; " + workerNotDontAskReason,
		}, true
	}
	return AllowRefusal{Kind: RefusalWorkerNotDontAsk, SessionKey: ev.key, Reason: workerNotDontAskReason}, true
}
