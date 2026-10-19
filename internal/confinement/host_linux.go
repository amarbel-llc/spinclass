//go:build linux

package confinement

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"syscall"
	"time"
)

// Host is the production System.
type Host struct{}

const connectTimeout = 2 * time.Second

func (Host) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (Host) ReadDir(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names, nil
}

func (Host) Readlink(path string) (string, error) { return os.Readlink(path) }

func (Host) Stat(path string) (fs.FileInfo, error) { return os.Stat(path) }

func (Host) Lstat(path string) (fs.FileInfo, error) { return os.Lstat(path) }

func (Host) CharDevice(path string) (uint64, uint64, bool, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, 0, false, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFCHR {
		return 0, 0, false, nil
	}
	return uint64(st.Rdev), uint64(st.Dev), true, nil //nolint:unconvert // widths differ by arch
}

func (Host) Connect(path string, abstract bool) error {
	addr := path
	if abstract {
		addr = "@" + path
	}
	c, err := net.DialTimeout("unix", addr, connectTimeout)
	if err != nil {
		return err
	}
	return c.Close()
}

func (Host) Writable(path string) error {
	// access(2) checks the real uid, which is the effective uid here; it
	// returns EROFS for a read-only mount, which is the point.
	return syscall.Access(path, 0x2)
}

func (Host) PeerPID(socket string) (int, error) {
	c, err := net.DialTimeout("unix", socket, connectTimeout)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return int(cred.Pid), nil
}

func (Host) Getuid() int { return os.Getuid() }

func (Host) Getenv(key string) string { return os.Getenv(key) }
