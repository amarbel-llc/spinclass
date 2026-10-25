//go:build unix

package grantverify

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// checkTrustworthy refuses a signers file that is group- or world-writable or
// not owned by the current user.
func checkTrustworthy(info fs.FileInfo) error {
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("mode %04o lets group or others write it; chmod go-w", info.Mode().Perm())
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot determine the file's owner")
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("owned by uid %d, not the current user (uid %d)", st.Uid, os.Getuid())
	}
	return nil
}
