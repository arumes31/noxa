//go:build !linux

package webrtc

import (
	"errors"
	"net"
)

func effectiveMediaReceiveBuffer(net.PacketConn) (int, error) {
	return 0, errors.ErrUnsupported
}
