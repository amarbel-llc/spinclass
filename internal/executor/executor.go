package executor

import (
	tap "code.linenisgreat.com/tap/go/pkgs/writer"
)

type Executor interface {
	Attach(dir string, key string, command []string, dryRun bool, tp *tap.TestPoint) error
	Detach() error
}
