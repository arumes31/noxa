package main

import (
	"encoding/base64"
	"strings"
	"sync/atomic"
	"testing"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

// P3-6: ConfirmDeviceForTab validates the relayed claim keys exactly like a
// device link before signing DeviceApprovalTranscript over them: a key with
// an embedded line break (Go's base64 decoder skips CR/LF, so the X25519
// parse alone accepts it) is never signed.
func TestConfirmDeviceRefusesNonCanonicalClaimKeys(t *testing.T) {
	signingKey, _, err := auth.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	x := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	for name, tc := range map[string]struct {
		ed, x  string
		signed bool
	}{
		"canonical":          {signingKey, x, true},
		"x25519 line break":  {signingKey, x[:20] + "\r\n" + x[20:], false},
		"x25519 extra line":  {signingKey, x + "\nextra", false},
		"ed25519 extra line": {signingKey + "x: y\n", x, false},
	} {
		t.Run(name, func(t *testing.T) {
			target := netproto.DeviceInfo{DeviceID: "phone", Kind: "web", Ed25519Public: tc.ed, X25519Public: tc.x, Fingerprint: netproto.KeyFingerprint(tc.ed)}
			var endorsements atomic.Int32
			a, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				var req netproto.DeviceRequest
				if err := netproto.Decode(f, &req); err != nil {
					t.Error(err)
				}
				switch req.Action {
				case netproto.DeviceActionList:
					return netproto.MsgDeviceResult, netproto.DeviceResult{Action: req.Action, Devices: []netproto.DeviceInfo{target}}, true
				case netproto.DeviceActionEndorse:
					endorsements.Add(1)
					return netproto.MsgDeviceResult, netproto.DeviceResult{Action: req.Action, Device: &target}, true
				}
				return netproto.MsgDeviceResult, netproto.DeviceResult{Action: req.Action}, true
			})
			a.tabs = map[string]*tabState{"a": {cm: cm}}
			a.activeID = "a"
			cm.mu.Lock()
			cm.uniqueID, cm.deviceID, cm.supportsDevices = "account", "self", true
			cm.mu.Unlock()
			_, err := a.ConfirmDeviceForTab("a", "phone")
			if tc.signed && endorsements.Load() != 1 {
				t.Fatalf("a canonical device must be endorsed: %v", err)
			}
			if !tc.signed && (err == nil || endorsements.Load() != 0) {
				t.Fatalf("non-canonical keys must never be signed: err=%v endorsements=%d", err, endorsements.Load())
			}
			if name == "x25519 line break" && !strings.Contains(err.Error(), "expected format") {
				t.Fatalf("refused by the claimed-key validator, got %v", err)
			}
		})
	}
}
