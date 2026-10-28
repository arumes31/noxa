package main

import (
	"bytes"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

type deviceEventCapture struct{ views []deviceEventView }

func (c *deviceEventCapture) Emit(name string, payload any) {
	if view, ok := payload.(deviceEventView); ok && name == "device_event" {
		c.views = append(c.views, view)
	}
}

func TestDeviceLinkDesktopDerivesSASAndSignsApproval(t *testing.T) {
	secret := bytes.Repeat([]byte{7}, 32)
	phonePublic, _, err := auth.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	var approval, bindingSig []byte
	var binding string
	app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		var r netproto.DeviceRequest
		if err := netproto.Decode(frame, &r); err != nil {
			t.Fatal(err)
		}
		switch r.Action {
		case netproto.DeviceActionLinkCreate:
			return netproto.MsgDeviceResult, netproto.DeviceResult{
				Action: r.Action, LinkID: "link-1", Secret: secret, Code: "K7QM-4XPA",
				URL: "https://chat.example/link#id=evil&k=x&a=0000+0000+0000+0000&zz=1", ExpiresAt: 1, State: netproto.LinkStatePending,
			}, true
		case netproto.DeviceActionLinkDecide:
			approval, binding, bindingSig = r.Signature, r.ServerBinding, r.BindingSignature
			return netproto.MsgDeviceResult, netproto.DeviceResult{Action: r.Action, LinkID: r.LinkID, State: netproto.LinkStateApproved}, true
		}
		return 0, nil, false
	})
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	cm.mu.Lock()
	cm.supportsDevices, cm.uniqueID, cm.deviceID = true, "user-uid", "dev-desktop"
	cm.fingerprint = strings.TrimSuffix(strings.Repeat("AB:", 32), ":") // pinned TLS certificate
	cm.mu.Unlock()
	capture := &deviceEventCapture{}
	cm.sink = capture

	link, err := app.CreateDeviceLinkForTab("a")
	if err != nil || link.LinkID != "link-1" || link.Code != "K7QM-4XPA" {
		t.Fatalf("create = %+v, %v", link, err)
	}
	// The QR names this desktop's key fingerprint (out of band), in a
	// fragment the desktop builds itself: the server's injected a= is gone.
	own, _ := cm.identity()
	base, frag, _ := strings.Cut(link.URL, "#")
	values, err := url.ParseQuery(frag)
	if base != "https://chat.example/link" || err != nil || len(values) != 3 || len(values["a"]) != 1 || values.Get("a") != netproto.KeyFingerprint(own.PublicKey) ||
		values.Get("id") != "link-1" || values.Get("k") != base64.RawURLEncoding.EncodeToString(secret) {
		t.Fatalf("link URL = %q", link.URL)
	}

	claim := func(linkID, via string) {
		frame, err := netproto.Encode(netproto.MsgDeviceEvent, netproto.DeviceEvent{
			Event: netproto.DeviceEventLinkClaimed, LinkID: linkID, Ed25519PublicKey: phonePublic, X25519PublicKey: "x", Via: via,
		})
		if err != nil {
			t.Fatal(err)
		}
		cm.handleDeviceEvent(frame)
	}
	claim("someone-elses-link", "qr")
	if len(capture.views) != 0 {
		t.Fatal("claim of a foreign link reached the webview")
	}
	transcript := netproto.LinkClaimTranscript("link-1", phonePublic, "x")
	claim("link-1", "qr")
	claim("link-1", "code")
	if len(capture.views) != 2 {
		t.Fatalf("views = %+v", capture.views)
	}
	if capture.views[0].SAS != netproto.LinkSAS(secret, transcript) || capture.views[1].SAS != netproto.LinkSAS([]byte("K7QM4XPA"), transcript) {
		t.Fatalf("SAS = %q / %q", capture.views[0].SAS, capture.views[1].SAS)
	}
	if capture.views[0].Fingerprint != netproto.KeyFingerprint(phonePublic) {
		t.Fatalf("fingerprint = %q", capture.views[0].Fingerprint)
	}

	result, err := app.DecideDeviceLinkForTab("a", "link-1", true, phonePublic, "x")
	if err != nil || result.State != netproto.LinkStateApproved {
		t.Fatalf("decide = %+v, %v", result, err)
	}
	id, err := cm.identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.VerifyChallenge(id.PublicKey, netproto.DeviceApprovalTranscript("user-uid", "dev-desktop", phonePublic, "x"), approval); err != nil {
		t.Fatalf("approval signature: %v", err)
	}
	// 1c-iii-a item 1: the pinned TLS binding, signed for exactly these keys.
	if binding != "tls-sha256:"+strings.Repeat("ab", 32) ||
		auth.VerifyChallenge(id.PublicKey, netproto.DeviceLinkBindingTranscript("user-uid", "dev-desktop", phonePublic, "x", binding), bindingSig) != nil {
		t.Fatalf("binding statement %q not signed for the claimed keys", binding)
	}
	if _, err := app.DecideDeviceLinkForTab("a", "link-1", true, phonePublic, "x"); err == nil {
		t.Fatal("a decided link was decided twice")
	}
}

func TestDeviceRequestsRequireServerSupport(t *testing.T) {
	app, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) { return 0, nil, false })
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	if _, err := app.CreateDeviceLinkForTab("a"); err != errDevicesUnsupported {
		t.Fatalf("create on an old server err = %v", err)
	}
}
