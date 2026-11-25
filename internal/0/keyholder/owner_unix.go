package keyholder

import (
	"os"
	"syscall"
)

// ownedByUs reports whether fi belongs to the calling user.
func ownedByUs(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
