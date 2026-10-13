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
	ControlCloseEOF              ControlCloseReason = "eof"
	ControlCloseUnexpectedEOF    ControlCloseReason = "unexpected_eof"
	ControlCloseLocal            ControlCloseReason = "local_close"
	ControlCloseReadTimeout      ControlCloseReason = "read_timeout"
	ControlCloseReadError        ControlCloseReason = "read_error"
	ControlCloseWriteTimeout     ControlCloseReason = "write_timeout"
	ControlCloseWriteError       ControlCloseReason = "write_error"
	ControlCloseRequestTimeout   ControlCloseReason = "request_timeout"
	ControlCloseProtocolError    ControlCloseReason = "protocol_error"
	ControlClosePanic            ControlCloseReason = "panic"
	ControlCloseServerInactivity ControlCloseReason = "server_inactivity"
)

// ControlReadCloseReason classifies a failed framed read without formatting err.
func ControlReadCloseReason(err error) ControlCloseReason {
	if errors.Is(err, net.ErrClosed) {
		return ControlCloseLocal
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
