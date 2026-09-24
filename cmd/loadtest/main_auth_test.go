package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"noxa/internal/netproto"
)

func TestSimulateClientRecordsSafeAuthFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame *netproto.Frame
		want  string
	}{
		{name: "server rejection", frame: &netproto.Frame{Type: uint16(netproto.MsgError), Payload: []byte(`{"code":5,"message":"peer-secret"}`)}, want: "stage=read category=server_error server_code=5"},
		{name: "auth rejection", frame: &netproto.Frame{Type: uint16(netproto.MsgAuthResponse), Payload: []byte(`{"ok":false,"reason":"peer-secret"}`)}, want: "stage=rejected category=rejected"},
		{name: "incompatible authorization model", frame: &netproto.Frame{Type: uint16(netproto.MsgAuthResponse), Payload: []byte(`{"ok":false,"authorization_model":"roles-v2","reason":"peer-secret"}`)}, want: "stage=model category=authorization_model_mismatch"},
		{name: "malformed authentication", frame: &netproto.Frame{Type: uint16(netproto.MsgAuthResponse), Payload: []byte("peer-secret")}, want: "stage=decode category=malformed_response"},
		{name: "malformed server error", frame: &netproto.Frame{Type: uint16(netproto.MsgError), Payload: []byte("peer-secret")}, want: "stage=read category=malformed_response"},
		{name: "transport closed", want: "stage=read category=transport"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			serverDone := make(chan error, 1)
			go func() {
				serverDone <- func() error {
					conn, err := listener.Accept()
					if err != nil {
						return err
					}
					defer func() { _ = conn.Close() }()
					if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
						return err
					}
					if _, err := netproto.ReadFrame(conn); err != nil {
						return err
					}
					if tc.frame != nil {
						return netproto.WriteFrame(conn, tc.frame)
					}
					return nil
				}()
			}()
			var st stats
			simulateClient(t.Context(), options{addr: listener.Addr().String(), uniqueID: "account-secret", password: "credential-secret"}, &st, 3)
			if err := <-serverDone; err != nil {
				t.Fatalf("fake server: %v", err)
			}
			if st.authFail.Load() != 1 || st.authOK.Load() != 0 {
				t.Fatalf("auth counters ok=%d fail=%d", st.authOK.Load(), st.authFail.Load())
			}
			failure, ok := st.authFailures.Load(3)
			if !ok {
				t.Fatal("missing per-client authentication diagnostic")
			}
			if got := fmt.Sprint(failure); got != tc.want {
				t.Fatalf("diagnostic = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAuthFailureDiagnosticsBoundAndClassifyTransportErrors(t *testing.T) {
	var st stats
	st.recordAuthFailure(0, "read", os.ErrDeadlineExceeded)
	st.recordAuthFailure(1, "write", errors.New("credential-secret peer-controlled-error"))
	st.recordAuthFailure(1, "read", errors.New("duplicate should not add output"))
	for index, want := range []string{"stage=read category=timeout", "stage=write category=transport"} {
		failure, ok := st.authFailures.Load(index)
		if !ok || fmt.Sprint(failure) != want {
			t.Errorf("client %d diagnostic = %v, want %q", index, failure, want)
		}
	}
	if got := st.authFail.Load(); got != 2 {
		t.Fatalf("auth failures = %d, want one per client", got)
	}
}

func TestReadOfTypeReturnsServerErrorImmediately(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, client := net.Pipe()
		defer func() { _ = server.Close(); _ = client.Close() }()
		const sensitiveMessage = "peer-password-secret-must-not-appear"
		go func() {
			_ = writeMsg(server, netproto.MsgError, netproto.Error{Code: 5, Message: sensitiveMessage})
		}()
		started := time.Now()
		_, err := readOfType(client, netproto.MsgAuthResponse, 5*time.Second)
		if err == nil || !strings.Contains(err.Error(), "code=5") {
			t.Fatalf("error = %v, want numeric server code 5", err)
		}
		if strings.Contains(err.Error(), sensitiveMessage) {
			t.Fatal("error exposes peer-controlled message")
		}
		if elapsed := time.Since(started); elapsed != 0 {
			t.Fatalf("server rejection waited %s for the response deadline", elapsed)
		}
	})
}

func TestReadOfTypePreservesRequestedErrorFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, client := net.Pipe()
		defer func() { _ = server.Close(); _ = client.Close() }()
		go func() {
			_ = writeMsg(server, netproto.MsgError, netproto.Error{Code: 4})
		}()
		frame, err := readOfType(client, netproto.MsgError, time.Second)
		if err != nil || frame == nil || frame.Type != uint16(netproto.MsgError) {
			t.Fatalf("requested Error frame = %v, %v", frame, err)
		}
	})
}
