//go:build linux

package auth

import (
	"net"

	"golang.org/x/sys/unix"
)

// PeerUID returns the uid of the process on the other end of a Unix socket
// (SO_PEERCRED).
func PeerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid := -1
	var serr error
	err = raw.Control(func(fd uintptr) {
		var u *unix.Ucred
		u, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if serr == nil {
			uid = int(u.Uid)
		}
	})
	if err != nil {
		return -1, err
	}
	return uid, serr
}
