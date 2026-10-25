//go:build !linux

package confinement

import (
	"errors"
	"io/fs"
	"os"
)

// Host is the production System. Off Linux there is no cgroup v2 and no
// /proc, so every observation fails and the check fails closed. Connect and
// Writable fail too rather than report "unreachable" or "denied", which a
// probe would read as proof.
type Host struct{}

var errUnsupported = errors.New("session confinement is only probed on Linux")

func (Host) ReadFile(string) ([]byte, error)                 { return nil, errUnsupported }
func (Host) ReadDir(string) ([]string, error)                { return nil, errUnsupported }
func (Host) Readlink(string) (string, error)                 { return "", errUnsupported }
func (Host) Stat(string) (fs.FileInfo, error)                { return nil, errUnsupported }
func (Host) Lstat(string) (fs.FileInfo, error)               { return nil, errUnsupported }
func (Host) CharDevice(string) (uint64, uint64, bool, error) { return 0, 0, false, errUnsupported }
func (Host) Connect(string, bool) error                      { return errUnsupported }
func (Host) Writable(string) error                           { return errUnsupported }
func (Host) PeerPID(string) (int, error)                     { return 0, errUnsupported }
func (Host) Getuid() int                                     { return os.Getuid() }
func (Host) Getenv(key string) string                        { return os.Getenv(key) }
