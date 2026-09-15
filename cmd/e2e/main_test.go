package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
	"noxa/internal/netproto"
)

func TestDialGuestPublishesUsableEncryptionKey(t *testing.T) {
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	var scopeKey [32]byte
	if _, err := rand.Read(scopeKey[:]); err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- func() error {
			conn, err := listener.Accept()
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
			var request netproto.Authenticate
			if err := netproto.Decode(frame, &request); err != nil {
				return err
			}
			pub, err := base64.StdEncoding.DecodeString(request.X25519PublicKey)
			if err != nil || len(pub) != 32 {
				return fmt.Errorf("guest authentication public key has %d bytes, error %v", len(pub), err)
			}
			var publicKey [32]byte
			copy(publicKey[:], pub)
			if publicKey == [32]byte{} {
				return fmt.Errorf("guest published the all-zero public key")
			}
			sealed, err := box.SealAnonymous(nil, scopeKey[:], &publicKey, rand.Reader)
			if err != nil {
				return err
			}
			if err := writeMsg(conn, netproto.MsgAuthResponse, netproto.AuthResponse{OK: true, UniqueID: "guest:test", ClientID: "client-test", Nickname: "guest", ChatKeys: []netproto.ChannelKey{{KeyID: 7, SealedKey: base64.StdEncoding.EncodeToString(sealed)}}}); err != nil {
				return err
			}
			if err := netproto.WriteFrame(conn, &netproto.Frame{Type: uint16(netproto.MsgSnapshot), Payload: []byte("{}")}); err != nil {
				return err
			}
			published, err := netproto.ReadFrame(conn)
			if err != nil {
				return err
			}
			var key netproto.KeyPublish
			if err := netproto.Decode(published, &key); err != nil {
				return err
			}
			if published.Type != uint16(netproto.MsgKeyPublish) || key.PublicKey != request.X25519PublicKey {
				return fmt.Errorf("guest did not publish the authenticated encryption key")
			}
			return nil
		}()
	}()
	guest, dialErr := dialGuest(listener.Addr().String(), "guest", "")
	if guest != nil {
		defer func() { _ = guest.conn.Close() }()
		defer clientsByConn.Delete(guest.conn)
	}
	if serverErr := <-serverDone; serverErr != nil {
		t.Fatalf("server verification: %v (dial error: %v)", serverErr, dialErr)
	}
	if dialErr != nil {
		t.Fatal(dialErr)
	}
	if got := guest.scopeKeys[0][7]; got != scopeKey {
		t.Fatal("guest cannot decrypt global key included in authentication response")
	}
	if guest.scopeLatest[0] != 7 {
		t.Fatal("guest did not retain the current global key generation")
	}
}
