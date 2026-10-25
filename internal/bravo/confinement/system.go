package confinement

import "io/fs"

// System is every observation the probes make of the host. Production is
// Host (Linux); tests substitute a fake. Paths are absolute host paths. A
// probe that looks into the unit resolves its path through the unit's root
// itself (prober.at), one component at a time, so no symlink is ever
// followed by the kernel against the frontend's root.
type System interface {
	ReadFile(path string) ([]byte, error)
	// ReadDir returns the entry names of a directory.
	ReadDir(path string) ([]string, error)
	Readlink(path string) (string, error)
	// Stat follows symlinks (so /proc/<pid>/fd/N yields the open file).
	Stat(path string) (fs.FileInfo, error)
	// Lstat does not follow a final symlink.
	Lstat(path string) (fs.FileInfo, error)
	// CharDevice reports, for a character device at path (following
	// symlinks), its device number and the device of the filesystem holding
	// it (st_dev: which devpts instance a pty belongs to); ok=false for
	// anything that is not a character device.
	CharDevice(path string) (rdev, fsdev uint64, ok bool, err error)
	// Connect attempts a unix stream connection to path (or, if abstract,
	// to the abstract-namespace name) and closes it at once. nil means the
	// endpoint accepted the connection.
	Connect(path string, abstract bool) error
	// Writable reports nil when the caller could open path for writing.
	// It must honour read-only mounts.
	Writable(path string) error
	// PeerPID connects to a unix socket and returns the peer's pid from
	// SO_PEERCRED.
	PeerPID(socket string) (int, error)
	// Getuid is the frontend's (and, per FDR 0035, the unit's) uid.
	Getuid() int
	Getenv(key string) string
}
