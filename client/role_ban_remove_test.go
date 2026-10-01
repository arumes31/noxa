package main

import (
	"testing"

	"noxa/internal/netproto"
)

func TestRoleBanRemovalRejectsMismatchedAcknowledgement(t *testing.T) {
	a, _ := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		if netproto.MessageType(frame.Type) == netproto.MsgRoleBanRemove {
			return netproto.MsgRoleBanRemoved, netproto.RoleBanRemoved{BanID: 18}, true
		}
		return 0, nil, false
	})
	a.tabs = map[string]*tabState{"a": {cm: a.cmLoad()}}
	a.activeID = "a"
	if result, err := a.RemoveRoleBanForTab("a", 17); err == nil || result.BanID != 0 {
		t.Fatalf("mismatched result: %+v %v", result, err)
	}
}
