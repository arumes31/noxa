package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
	"noxa/internal/broadcast"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

func TestE2ENativeSelectedModelBeforeKeysOrSession(t *testing.T) {
	for _, guest := range []bool{false, true} {
		for _, selected := range []string{"", "roles-v1"} {
			for _, returned := range []string{"", "roles-v1", "future"} {
				t.Run(fmt.Sprintf("guest=%v/%s/%s", guest, selected, returned), func(t *testing.T) {
					ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = ln.Close() }()
					done := make(chan error, 1)
					go func() {
						done <- func() error {
							conn, err := ln.Accept()
							if err != nil {
								return err
							}
							defer func() { _ = conn.Close() }()
							if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
								return err
							}
							frame, err := netproto.ReadFrame(conn)
							if err != nil {
								return err
							}
							var req netproto.Authenticate
							if err := netproto.Decode(frame, &req); err != nil {
								return err
							}
							if req.Anonymous != guest || !reflect.DeepEqual(req.AuthorizationModels, e2eAdvertisedModels(selected)) {
								return fmt.Errorf("wrong authentication request")
							}
							if err := writeMsg(conn, netproto.MsgAuthResponse, netproto.AuthResponse{OK: true, AuthorizationModel: returned, ClientID: "self", UniqueID: "uid"}); err != nil {
								return err
							}
							if selected != returned {
								if _, err := netproto.ReadFrame(conn); err == nil {
									return fmt.Errorf("published after mismatch")
								} else if n, ok := err.(net.Error); ok && n.Timeout() {
									return fmt.Errorf("mismatch waited for session data")
								}
								return nil
							}
							pub, err := base64.StdEncoding.DecodeString(req.X25519PublicKey)
							if err != nil || len(pub) != 32 {
								return fmt.Errorf("missing authentication key")
							}
							var public [32]byte
							copy(public[:], pub)
							sealed, err := box.SealAnonymous(nil, bytes.Repeat([]byte{8}, 32), &public, rand.Reader)
							if err != nil {
								return err
							}
							if err := writeMsg(conn, netproto.MsgChannelKey, netproto.ChannelKey{KeyID: 7, SealedKey: base64.StdEncoding.EncodeToString(sealed)}); err != nil {
								return err
							}
							if err := writeMsg(conn, netproto.MsgSnapshot, broadcast.TreeSnapshot{}); err != nil {
								return err
							}
							frame, err = netproto.ReadFrame(conn)
							if err != nil {
								return err
							}
							if frame.Type != uint16(netproto.MsgKeyPublish) {
								return fmt.Errorf("expected key publication after compatible session")
							}
							return nil
						}()
					}()
					var c *client
					if guest {
						c, err = dialGuest(ln.Addr().String(), "test", "", selected)
					} else {
						c, err = dialAuth(ln.Addr().String(), "nickname", "pw", "", selected)
					}
					if c != nil {
						defer func() { _ = c.conn.Close(); clientsByConn.Delete(c.conn) }()
					}
					if serverErr := <-done; serverErr != nil {
						t.Fatal(serverErr)
					}
					if selected == returned {
						if err != nil || c == nil || c.scopeLatest[0] != 7 {
							t.Fatalf("compatible session lost setup key: %v", err)
						}
						if c.uid != "uid" {
							t.Fatal("nickname was retained instead of the canonical authenticated unique ID")
						}
					} else if err == nil || c != nil || !strings.Contains(err.Error(), "profile") {
						t.Fatalf("mismatch accepted: %v", err)
					}
				})
			}
		}
	}
}

func TestE2EQueryModelAndEscapedCredentials(t *testing.T) {
	for _, confirmed := range []bool{true, false} {
		t.Run(fmt.Sprint(confirmed), func(t *testing.T) {
			ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ln.Close() }()
			done := make(chan error, 1)
			go func() {
				done <- func() error {
					conn, err := ln.Accept()
					if err != nil {
						return err
					}
					defer func() { _ = conn.Close() }()
					if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
						return err
					}
					if _, err := conn.Write([]byte("banner\nhint\n")); err != nil {
						return err
					}
					line, err := bufio.NewReader(conn).ReadString('\n')
					if err != nil {
						return err
					}
					if line != "login uid= p\\sa\\\\ss\\nword authorization_model=roles-v1\n" {
						return fmt.Errorf("login escaping or model declaration changed")
					}
					reply := "error id=0 msg=ok\n"
					if confirmed {
						reply = "authorization_model=roles-v1\n" + reply
					}
					if _, err := conn.Write([]byte(reply)); err != nil {
						return err
					}
					if !confirmed {
						if _, err := conn.Read(make([]byte, 1)); err == nil {
							return fmt.Errorf("mismatch retained connection")
						} else if n, ok := err.(net.Error); ok && n.Timeout() {
							return fmt.Errorf("mismatch did not close connection")
						}
					}
					return nil
				}()
			}()
			q, err := dialQuery(ln.Addr().String(), "uid=", "p a\\ss\nword", "roles-v1")
			if q != nil {
				defer func() { _ = q.conn.Close() }()
			}
			if serverErr := <-done; serverErr != nil {
				t.Fatal(serverErr)
			}
			if (err == nil) != confirmed {
				t.Fatalf("confirmation=%v err=%v", confirmed, err)
			}
		})
	}
}

func TestE2ERoleMembershipChecksExactSnapshotAndCapturesKeys(t *testing.T) {
	conn := &deadlineRecordingConn{}
	c := &client{conn: conn, clientID: "self"}
	if err := initClientKeys(c); err != nil {
		t.Fatal(err)
	}
	clientsByConn.Store(conn, c)
	defer clientsByConn.Delete(conn)
	sealed, err := box.SealAnonymous(nil, bytes.Repeat([]byte{8}, 32), &c.e2ePub, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	queueDeadlineTestFrame(t, conn, netproto.MsgChannelKey, netproto.ChannelKey{ChannelID: 42, KeyID: 7, SealedKey: base64.StdEncoding.EncodeToString(sealed)})
	for _, id := range []string{"other", "self"} {
		queueDeadlineTestFrame(t, conn, netproto.MsgSnapshot, broadcast.TreeSnapshot{RootChannels: []*broadcast.ChannelNode{{Channel: state.Channel{ChannelID: 42}, Clients: []*broadcast.ClientInfo{{ClientID: id, ChannelID: 42}}}}})
	}
	if err := waitForRoleMembership(conn, "self", 42); err != nil {
		t.Fatal(err)
	}
	if conn.input.Len() != 0 || !conn.readDeadline.IsZero() || c.scopeLatest[42] != 7 {
		t.Fatal("membership skipped exact identity, key capture or deadline cleanup")
	}
	queueDeadlineTestFrame(t, conn, netproto.MsgError, netproto.Error{Code: 4, Message: "private"})
	if err := waitForRoleMembership(conn, "self", 42); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("rejected membership not reported safely")
	}
}

func TestE2ERoleQueryJSONRequiresAcknowledgedSingleResult(t *testing.T) {
	type result struct {
		Value string `json:"value"`
	}
	want := result{Value: "space slash / backslash \\ newline\n tab\t |"}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, response string
		ok             bool
	}{
		{"success", "data=" + escapeE2EQuery(string(payload)) + "\nerror id=0 msg=ok\n", true},
		{"rejected", "data=" + escapeE2EQuery(string(payload)) + "\nerror id=2568 msg=denied\n", false},
		{"duplicate", "data={}\ndata={}\nerror id=0 msg=ok\n", false},
		{"invalid JSON", "data=not-json\nerror id=0 msg=ok\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			conn := &deadlineRecordingConn{}
			conn.input.WriteString(tt.response)
			q := &querySession{conn: conn, r: &lineReader{conn: conn}}
			got, err := e2eQueryJSON[result](q, "rolechange", want)
			if (err == nil) != tt.ok || (tt.ok && got != want) {
				t.Fatalf("result=%+v err=%v", got, err)
			}
		})
	}
}

func TestE2EReadOfTypeRejectsServerErrorsImmediately(t *testing.T) {
	conn := &deadlineRecordingConn{}
	queueDeadlineTestFrame(t, conn, netproto.MsgError, netproto.Error{Code: 4, Message: "private"})
	if _, err := readOfType(conn, netproto.MsgRoleState, time.Second); err == nil || !strings.Contains(err.Error(), "code=4") || strings.Contains(err.Error(), "private") {
		t.Fatalf("rejection=%v", err)
	}
	if !conn.readDeadline.IsZero() {
		t.Fatal("deadline retained after rejection")
	}
}

func TestE2EQueryResponseLimitCountsRawCarriageReturns(t *testing.T) {
	conn := &deadlineRecordingConn{}
	for range 2 {
		conn.input.WriteString(strings.Repeat("\r", 4<<20))
		conn.input.WriteByte('\n')
	}
	conn.input.WriteString("error id=0 msg=ok\n")
	q := &querySession{conn: conn, r: &lineReader{conn: conn}}
	if _, err := q.cmd("rolelist"); err == nil || !strings.Contains(err.Error(), "response exceeds limit") {
		t.Fatalf("CR-padded response bypassed byte limit: %v", err)
	}
}
