//go:build darwin

package auth

import (
	"net"

	"golang.org/x/sys/unix"
)

// PeerUID returns the uid of the process on the other end of a Unix socket
// (getpeereid / LOCAL_PEERCRED).
func PeerUID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	uid := -1
	var serr error
	err = raw.Control(func(fd uintptr) {
		var x *unix.Xucred
		x, serr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if serr == nil {
			uid = int(x.Uid)
		}
	})
	if err != nil {
		return -1, err
	}
	return uid, serr
}
