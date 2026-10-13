package netproto

import (
	"errors"
	"io"
	"net"
	"os"
	"testing"
)

type unformattableCloseError struct{ inner error }

func (e unformattableCloseError) Error() string { panic("diagnostics formatted a private error") }
func (e unformattableCloseError) Unwrap() error { return e.inner }

func TestControlCloseReasonsDoNotFormatErrors(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		read, write ControlCloseReason
	}{
		{"eof", io.EOF, ControlCloseEOF, ControlCloseWriteError},
		{"partial_frame", io.ErrUnexpectedEOF, ControlCloseUnexpectedEOF, ControlCloseWriteError},
		{"local_closed", net.ErrClosed, ControlCloseLocal, ControlCloseLocal},
		{"deadline", os.ErrDeadlineExceeded, ControlCloseReadTimeout, ControlCloseWriteTimeout},
		{"network_deadline", &net.OpError{Op: "private-operation", Net: "private-network", Err: os.ErrDeadlineExceeded}, ControlCloseReadTimeout, ControlCloseWriteTimeout},
		{"unknown", errors.New("private endpoint/token/payload"), ControlCloseReadError, ControlCloseWriteError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// errors.Is/As may inspect the cause; diagnostics must never call Error.
			wrapped := unformattableCloseError{tc.err}
			if got := ControlReadCloseReason(wrapped); got != tc.read {
				t.Fatalf("read reason = %s, want %s", got, tc.read)
			}
			if got := ControlWriteCloseReason(wrapped); got != tc.write {
				t.Fatalf("write reason = %s, want %s", got, tc.write)
			}
		})
	}
}
