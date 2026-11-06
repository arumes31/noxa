package netproto

import (
	"errors"
	"io"
	"net"
)

// ControlCloseReason is a fixed diagnostic category, never a peer-supplied
// error string. It describes the observed termination, not its network cause.
type ControlCloseReason string

const (
	ControlCloseEOF                 ControlCloseReason = "eof"
	ControlCloseUnexpectedEOF       ControlCloseReason = "unexpected_eof"
	ControlCloseLocal               ControlCloseReason = "local_close"
	ControlCloseReadTimeout         ControlCloseReason = "read_timeout"
	ControlCloseReadError           ControlCloseReason = "read_error"
	ControlCloseWriteTimeout        ControlCloseReason = "write_timeout"
	ControlCloseWriteError          ControlCloseReason = "write_error"
	ControlCloseRequestTimeout      ControlCloseReason = "request_timeout"
	ControlCloseRequestDrainTimeout ControlCloseReason = "request_drain_timeout"
	ControlCloseProtocolError       ControlCloseReason = "protocol_error"
	ControlClosePanic               ControlCloseReason = "panic"
	ControlCloseServerInactivity    ControlCloseReason = "server_inactivity"
	ControlCloseAuthTimeout         ControlCloseReason = "auth_timeout"
	ControlCloseFrameTooLarge       ControlCloseReason = "frame_too_large"
	ControlClosePreauthRejected     ControlCloseReason = "preauth_rejected"
	ControlCloseShutdown            ControlCloseReason = "shutdown"
	ControlCloseKicked              ControlCloseReason = "kicked"
)

// ControlCloseReasons lists every reason, for bounded metric labels.
var ControlCloseReasons = []ControlCloseReason{
	ControlCloseEOF, ControlCloseUnexpectedEOF, ControlCloseLocal,
	ControlCloseReadTimeout, ControlCloseReadError, ControlCloseWriteTimeout,
	ControlCloseWriteError, ControlCloseRequestTimeout, ControlCloseRequestDrainTimeout,
	ControlCloseProtocolError, ControlClosePanic, ControlCloseServerInactivity,
	ControlCloseAuthTimeout, ControlCloseFrameTooLarge, ControlClosePreauthRejected,
	ControlCloseShutdown, ControlCloseKicked,
}

// ControlReadCloseReason classifies a failed framed read without formatting err.
func ControlReadCloseReason(err error) ControlCloseReason {
	if errors.Is(err, net.ErrClosed) {
		return ControlCloseLocal
	}
	if errors.Is(err, ErrFrameTooLarge) {
		return ControlCloseFrameTooLarge
	}
	if errors.Is(err, ErrInvalidFrameLength) {
		return ControlCloseProtocolError
	}
	if errors.Is(err, io.EOF) {
		return ControlCloseEOF
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return ControlCloseUnexpectedEOF
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return ControlCloseReadTimeout
	}
	return ControlCloseReadError
}

// ControlWriteCloseReason classifies a failed write without formatting err.
func ControlWriteCloseReason(err error) ControlCloseReason {
	if errors.Is(err, net.ErrClosed) {
		return ControlCloseLocal
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return ControlCloseWriteTimeout
	}
	return ControlCloseWriteError
}
