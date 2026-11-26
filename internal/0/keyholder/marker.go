package keyholder

// BuildMarker is a string only a spinclass binary that carries the key holder
// code contains. `sc host-load` reads each live `spinclass serve` binary for it
// (the host queue's QUEUE BYPASS mechanism, cmd/spinclass/hostload_bypass.go): a
// serve that started before the key holder was deployed keeps its old binary, so
// the workers it spawns get no pair and the operator's agent. Never change it
// without changing every build that must be told from an older one.
//
// Bump it (the "-v1" suffix, and the same text in zz-tests_bats/serve.bats)
// whenever a later change makes the key-holder serves that carry the older marker
// unsafe at switch-on, so those serves are listed as stale. It was not bumped for
// the evidence rule that followed the merge of 07f71750 because the build that
// carries the marker without that rule was never deployed to a host.
const BuildMarker = "spinclass-keyholder-build-v1"
