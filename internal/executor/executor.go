package executor

import (
	tap "github.com/amarbel-llc/tap/go/pkgs/writer"
)

type Executor interface {
	Attach(dir string, key string, command []string, dryRun bool, tp *tap.TestPoint) error
	Detach() error
}
