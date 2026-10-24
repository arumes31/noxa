package main

import (
	"bytes"
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
	var approval []byte
	app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		var r netproto.DeviceRequest
		if err := netproto.Decode(frame, &r); err != nil {
			t.Fatal(err)
		}
		switch r.Action {
		case netproto.DeviceActionLinkCreate:
			return netproto.MsgDeviceResult, netproto.DeviceResult{
				Action: r.Action, LinkID: "link-1", Secret: secret, Code: "K7QM-4XPA",
				URL: "https://chat.example/link#id=link-1&k=x", ExpiresAt: 1, State: netproto.LinkStatePending,
			}, true
		case netproto.DeviceActionLinkDecide:
			approval = r.Signature
			return netproto.MsgDeviceResult, netproto.DeviceResult{Action: r.Action, LinkID: r.LinkID, State: netproto.LinkStateApproved}, true
		}
		return 0, nil, false
	})
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	cm.mu.Lock()
	cm.supportsDevices, cm.uniqueID, cm.deviceID = true, "user-uid", "dev-desktop"
	cm.mu.Unlock()
	capture := &deviceEventCapture{}
	cm.sink = capture

	link, err := app.CreateDeviceLinkForTab("a")
	if err != nil || link.LinkID != "link-1" || link.Code != "K7QM-4XPA" {
		t.Fatalf("create = %+v, %v", link, err)
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
