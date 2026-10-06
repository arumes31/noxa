package webrtc

import (
	"net"

	"go.uber.org/zap"
)

const mediaReceiveBufferBytes = 4 * 1024 * 1024

// Ask the kernel for burst headroom before ICE starts reading the shared
// socket. The operating system may clamp this; no host setting is changed.
func configureMediaReceiveBuffer(conn net.PacketConn, logger *zap.Logger) {
	buffer, ok := conn.(interface{ SetReadBuffer(int) error })
	if !ok {
		return
	}
	if err := buffer.SetReadBuffer(mediaReceiveBufferBytes); err != nil {
		logger.Warn("webrtc: shared UDP receive buffer request failed", zap.Int("requested_bytes", mediaReceiveBufferBytes), zap.Error(err))
		return
	}
	fields := []zap.Field{zap.Int("requested_bytes", mediaReceiveBufferBytes)}
	if actual, err := effectiveMediaReceiveBuffer(conn); err == nil {
		// Linux reports its actual socket allocation, including kernel overhead.
		fields = append(fields, zap.Int("effective_bytes", actual))
	}
	logger.Info("webrtc: shared UDP receive buffer configured", fields...)
}
