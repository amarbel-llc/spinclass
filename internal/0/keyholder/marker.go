package keyholder

// BuildMarker is a string only a spinclass binary that carries the key holder
// code contains. `sc host-load` reads each live `spinclass serve` binary for it
// (the host queue's QUEUE BYPASS mechanism, cmd/spinclass/hostload_bypass.go): a
// serve that started before the key holder was deployed keeps its old binary, so
// the workers it spawns get no pair and the operator's agent. Never change it
// without changing every build that must be told from an older one.
const BuildMarker = "spinclass-keyholder-build-v1"
