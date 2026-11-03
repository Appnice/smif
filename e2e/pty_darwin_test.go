package e2e

import (
	"bytes"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPTY opens a new pseudo terminal and returns its master and slave.
func openPTY() (master, slave *os.File, err error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	master = os.NewFile(uintptr(fd), "/dev/ptmx")
	fail := func(e error) (*os.File, *os.File, error) { master.Close(); return nil, nil, e }
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		return fail(fmt.Errorf("TIOCPTYGRANT: %w", err))
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		return fail(fmt.Errorf("TIOCPTYUNLK: %w", err))
	}
	var name [128]byte // TIOCPTYGNAME writes up to 128 bytes
	// x/sys/unix has no wrapper that passes a 128-byte buffer to ioctl.
	//lint:ignore SA1019 test-only ioctl with a buffer argument; no libSystem wrapper exists in x/sys/unix
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); e != 0 {
		return fail(fmt.Errorf("TIOCPTYGNAME: %w", e))
	}
	path := string(name[:bytes.IndexByte(name[:], 0)])
	slave, err = os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return fail(err)
	}
	return master, slave, nil
}
