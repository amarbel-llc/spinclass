//go:build !unix

package grantverify

import "io/fs"

// checkTrustworthy: ownership and mode bits are not meaningful here; the
// signers file's protection is the platform's ACLs.
func checkTrustworthy(fs.FileInfo) error { return nil }
