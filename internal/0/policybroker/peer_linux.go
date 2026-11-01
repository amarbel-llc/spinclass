package policybroker

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

// SameUID admits a unix-socket peer that runs as this process's uid, read
// from SO_PEERCRED, and returns its pid (in this process's pid namespace) for
// a stricter check by the caller. It fails closed: a peer whose credentials
// cannot be read is refused.
func SameUID(c net.Conn) (pid int, err error) {
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
		return 0, fmt.Errorf("peer credentials unreadable: %w", credErr)
	}
	if int(cred.Uid) != os.Getuid() {
		return 0, fmt.Errorf("peer pid %d runs as uid %d, not %d", cred.Pid, cred.Uid, os.Getuid())
	}
	return int(cred.Pid), nil
}
