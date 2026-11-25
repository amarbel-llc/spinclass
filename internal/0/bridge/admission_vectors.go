package bridge

import _ "embed"

// AdmissionVectors is testdata/admission-vectors.json: the listener's admission
// decision as cases (a cgroup file as placed, a peer's cgroup, admit or not),
// run by both spinclass and clown. It is embedded so a test in another package
// reads this one file, not a copy; its sha256 is pinned beside it.
//
//go:embed testdata/admission-vectors.json
var AdmissionVectors []byte
