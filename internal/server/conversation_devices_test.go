package server

import (
	"context"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"testing"
)

type deviceGroupTestStore struct{ *privateCallTestStore }

func (s *deviceGroupTestStore) SendConversation(context.Context, string, netproto.ConversationRequest) (int64, []string, error) {
	return 42, nil, nil
}

func TestSinglePhoneCanSendGroupWithOwnEnvelope(t *testing.T) {
	devices := &fakeDeviceMessages{fakePushes: &fakePushes{fakeDevices: &fakeDevices{}}}
	chat := &deviceGroupTestStore{&privateCallTestStore{fakeChat: newFakeChat(), group: netproto.Conversation{
		ID: testGroupID, Name: "Group", Owner: "user-uid", Revision: 1, Epoch: 1,
		Members: []netproto.ConversationMember{{UniqueID: "user-uid", JoinedEpoch: 1}, {UniqueID: "admin-uid", JoinedEpoch: 1}},
	}}}
	env := startTestEnvDeps(t, nil, func(c *config.Config) { c.Web = config.WebConfig{Enabled: true, PublicURL: "https://chat.example"} }, func(d *Deps) { d.Devices = devices; d.Chat = chat })
	t.Cleanup(env.stop)
	e := dmTestEnv{env: env, devices: devices}
	userKeys := newLinkTestKeys(t)
	_, desktopID := e.desktopAs(t, "user-uid", userKeys)
	_, _ = e.desktopAs(t, "admin-uid", newLinkTestKeys(t))
	phoneKeys, phoneID := e.approvedPhone(t, "user-uid", 2, desktopID, userKeys)
	phone, auth := keyLogin(t, env.addr, phoneKeys)
	t.Cleanup(func() { _ = phone.Close() })
	if !auth.OK {
		t.Fatal("phone login failed")
	}
	send(t, phone, netproto.MsgConversationRequest, netproto.ConversationRequest{Action: "send", ID: testGroupID, Revision: 1, Message: &netproto.ConversationSend{
		Epoch: 1, Reference: "review-single-phone", Envelopes: map[string]string{"user-uid": "sealed-account", "admin-uid": "sealed-peer"},
		DeviceEnvelopes: map[string]string{netproto.DeviceEnvelopeKey("user-uid", phoneID): "sealed-self"},
	}})
	for {
		frame, err := netproto.ReadFrame(phone)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Type == uint16(netproto.MsgError) {
			var got netproto.Error
			_ = netproto.Decode(frame, &got)
			t.Fatalf("valid mobile group send rejected: %+v", got)
		}
		if frame.Type == uint16(netproto.MsgConversationResult) {
			return
		}
	}
}
