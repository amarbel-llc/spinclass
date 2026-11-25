package bridgeserver

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"code.linenisgreat.com/spinclass/internal/0/bridge"
)

// maxCgroupFileBytes bounds what is read from the cgroup file: a cgroup path is
// far shorter, and the bound keeps a hostile file from being slurped.
const maxCgroupFileBytes = 4096

// EnvCgroupFile is the cgroup file's path as clown exported it; empty counts
// as unset.
func EnvCgroupFile(getenv func(string) string) string {
	return strings.TrimSpace(getenv(bridge.EnvCgroupFile))
}

// checkCgroupFilePath requires the cgroup file's path to be absolute, clean and
// outside the bridge directory: a file inside the bind could be written by the
// unit it is meant to name.
func checkCgroupFilePath(dir, file string) error {
	if file == "" {
		return fmt.Errorf("bridge: %s is not set; the bridge cannot know the unit's cgroup", bridge.EnvCgroupFile)
	}
	if !filepath.IsAbs(file) || filepath.Clean(file) != file {
		return fmt.Errorf("bridge: %s=%q is not a clean absolute path", bridge.EnvCgroupFile, file)
	}
	d := filepath.Clean(dir)
	if file == d || strings.HasPrefix(file, d+string(filepath.Separator)) {
		return fmt.Errorf("bridge: %s=%q is inside the bridge directory %q, which the unit can reach", bridge.EnvCgroupFile, file, d)
	}
	return nil
}

// ReadCgroupFile reads the unit's cgroup path from the file clown wrote. It
// opens without following a link and accepts only a regular file of this uid
// that nobody else can read or write, holding exactly an absolute, clean,
// control-free path with no trailing newline. Anything else is an error (the
// listener then admits nobody); a missing file is an error wrapping
// fs.ErrNotExist.
func ReadCgroupFile(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("bridge: cgroup file %s is not a regular file", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("bridge: cgroup file %s has mode %o; it must be 0600", path, fi.Mode().Perm())
	}
	if uid, ok := statUID(fi); !ok || int(uid) != os.Getuid() {
		return "", fmt.Errorf("bridge: cgroup file %s is not owned by this uid", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxCgroupFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxCgroupFileBytes {
		return "", fmt.Errorf("bridge: cgroup file %s is over %d bytes", path, maxCgroupFileBytes)
	}
	cg := string(b)
	if cg == "" || cg == "/" || cg[0] != '/' || filepath.Clean(cg) != cg {
		return "", fmt.Errorf("bridge: cgroup file %s does not hold one clean absolute cgroup path", path)
	}
	for _, r := range cg {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("bridge: cgroup file %s holds a control character (a trailing newline is one)", path)
		}
	}
	return cg, nil
}

// cgroupFileGone is the reason a listener is dead once its cgroup file was
// valid and then removed: clown removes it when it drops the bridge. A file
// that was never valid is not death, only a bridge that is not ready.
func (s *Server) cgroupFileGone() error {
	if s.opts.PeerCheck != nil || s.opts.CgroupFile == "" {
		return nil
	}
	_, err := ReadCgroupFile(s.opts.CgroupFile)
	switch {
	case err == nil:
		s.cgroupSeen.Store(true)
	case s.cgroupSeen.Load() && errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("bridge: the cgroup file %s was removed; the launcher dropped the bridge", s.opts.CgroupFile)
	}
	return nil
}
