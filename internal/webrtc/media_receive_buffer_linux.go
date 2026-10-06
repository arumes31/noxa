//go:build linux

package webrtc

import (
	"errors"
	"math"
	"net"
	"syscall"
)

func effectiveMediaReceiveBuffer(conn net.PacketConn) (int, error) {
	socket, ok := conn.(syscall.Conn)
	if !ok {
		return 0, errors.ErrUnsupported
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return 0, err
	}
	var size int
	var readErr error
	if err := raw.Control(func(fd uintptr) {
		if fd > math.MaxInt {
			readErr = errors.ErrUnsupported
			return
		}
		size, readErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
	}); err != nil {
		return 0, err
	}
	return size, readErr
}
