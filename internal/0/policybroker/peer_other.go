//go:build !linux

package policybroker

import (
	"errors"
	"net"
)

// SameUID fails closed where SO_PEERCRED is unavailable: no peer is admitted.
func SameUID(net.Conn) (int, error) {
	return 0, errors.New("peer credentials are not supported on this platform")
}
