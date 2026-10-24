package server

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/store"
	"noxa/internal/webpush"
)

// fakePushes adds an in-memory PushStore to fakeDevices.
type fakePushes struct {
	*fakeDevices
	pmu  sync.Mutex
	subs map[string]store.PushSubscription // by device ID
}

func (f *fakePushes) SavePushSubscription(_ context.Context, p store.PushSubscription) error {
	f.pmu.Lock()
	defer f.pmu.Unlock()
	if f.subs == nil {
		f.subs = map[string]store.PushSubscription{}
	}
	f.subs[p.DeviceID] = p
	return nil
}

func (f *fakePushes) SetPushPreferences(_ context.Context, principal, deviceID string, hide, always bool) error {
	f.pmu.Lock()
	defer f.pmu.Unlock()
	p, ok := f.subs[deviceID]
	if !ok || p.Principal != principal {
		return store.ErrDeviceNotFound
	}
	p.HideDetails, p.AlwaysNotify = hide, always
	f.subs[deviceID] = p
	return nil
}

func (f *fakePushes) PushSubscriptionForDevice(_ context.Context, principal, deviceID string) (store.PushSubscription, error) {
	f.pmu.Lock()
	defer f.pmu.Unlock()
	p, ok := f.subs[deviceID]
	if !ok || p.Principal != principal {
		return store.PushSubscription{}, store.ErrDeviceNotFound
	}
	return p, nil
}

func (f *fakePushes) PushSubscriptions(_ context.Context, principal string) ([]store.PushSubscription, error) {
	f.pmu.Lock()
	defer f.pmu.Unlock()
	var out []store.PushSubscription
	for _, p := range f.subs {
		if p.Principal == principal {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakePushes) DeletePushSubscription(_ context.Context, _ string, deviceID string) error {
	f.pmu.Lock()
	defer f.pmu.Unlock()
	delete(f.subs, deviceID)
	return nil
}

func (f *fakePushes) DeletePushEndpoint(_ context.Context, endpoint string) error {
	f.pmu.Lock()
	defer f.pmu.Unlock()
	for id, p := range f.subs {
		if p.Endpoint == endpoint {
			delete(f.subs, id)
		}
	}
	return nil
}

func (f *fakePushes) RecordPushResult(context.Context, string, bool, int) error { return nil }

type sentPush struct {
	endpoint string
	payload  pushPayload
	msg      webpush.Message
}

// fakeSender records pushes; gone endpoints answer ErrGone.
type fakeSender struct {
	sent chan sentPush
	gone map[string]bool
}

func (f *fakeSender) PublicKey() string { return "vapid-test-key" }

func (f *fakeSender) Send(_ context.Context, sub webpush.Subscription, msg webpush.Message) error {
	if f.gone[sub.Endpoint] {
		return webpush.ErrGone
	}
	var p pushPayload
	_ = json.Unmarshal(msg.Payload, &p)
	f.sent <- sentPush{endpoint: sub.Endpoint, payload: p, msg: msg}
	return nil
}

func (f *fakeSender) next(t *testing.T) sentPush {
	t.Helper()
	select {
	case p := <-f.sent:
		return p
	case <-time.After(waitDeadline):
		t.Fatal("no push sent")
		return sentPush{}
	}
}

func (f *fakeSender) none(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case p := <-f.sent:
		t.Fatalf("unexpected push %+v", p.payload)
	case <-time.After(within):
	}
}

type pushTestEnv struct {
	env    *testEnv
	pushes *fakePushes
	sender *fakeSender
}

func startPushTestEnv(t *testing.T) pushTestEnv {
	t.Helper()
	pushes := &fakePushes{fakeDevices: &fakeDevices{}}
	sender := &fakeSender{sent: make(chan sentPush, 16), gone: map[string]bool{}}
	env := startTestEnvDeps(t, nil, func(cfg *config.Config) {
		cfg.Web = config.WebConfig{Enabled: true, PublicURL: "https://chat.example", PushEnabled: true}
	}, func(deps *Deps) {
		deps.Devices = pushes
		deps.Push = sender
	})
	t.Cleanup(env.stop)
	return pushTestEnv{env: env, pushes: pushes, sender: sender}
}

// phoneSession links a web device of user-uid and signs it in over the web
// transport.
func (p pushTestEnv) phoneSession(t *testing.T, name string) (net.Conn, *Client) {
	t.Helper()
	keys := newLinkTestKeys(t)
	if _, err := p.pushes.AddDevice(t.Context(), store.NewDevice{Principal: "user-uid", UserID: 2, Kind: store.DeviceKindWeb, Name: name, Ed25519Public: keys.public, X25519Public: keys.x25519}, 10); err != nil {
		t.Fatal(err)
	}
	conn, resp := keyLogin(t, p.env.addr, keys)
	t.Cleanup(func() { _ = conn.Close() })
	if !resp.OK {
		t.Fatalf("phone login = %+v", resp)
	}
	session, ok := p.env.srv.clientByID(resp.ClientID)
	if !ok {
		t.Fatal("phone session not found")
	}
	session.webTransport.Store(true)
	return conn, session
}

func browserSubscription(t *testing.T, endpoint string) *netproto.PushSubscriptionKeys {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return &netproto.PushSubscriptionKeys{
		Endpoint: endpoint,
		P256dh:   base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(auth),
	}
}

func TestPushSubscriptionAndPreferences(t *testing.T) {
	p := startPushTestEnv(t)
	phone, _ := p.phoneSession(t, "Pixel")

	config := deviceCall(t, phone, netproto.DeviceRequest{Action: netproto.DeviceActionPushConfig})
	if config.VAPIDPublicKey != "vapid-test-key" || config.Preferences == nil || config.Preferences.Subscribed {
		t.Fatalf("push config = %+v", config)
	}
	bad := browserSubscription(t, "http://push.example/1")
	if e := deviceCallError(t, phone, netproto.DeviceRequest{Action: netproto.DeviceActionPushSubscribe, Subscription: bad}); e.Code != errCodeMalformed {
		t.Fatal("plain http endpoint accepted")
	}
	subscribed := deviceCall(t, phone, netproto.DeviceRequest{
		Action: netproto.DeviceActionPushSubscribe, Subscription: browserSubscription(t, "https://push.example/1"),
		Preferences: &netproto.PushPreferences{HideDetails: true},
	})
	if !subscribed.Preferences.Subscribed || !subscribed.Preferences.HideDetails || subscribed.Preferences.AlwaysNotify {
		t.Fatalf("subscribe = %+v", subscribed.Preferences)
	}
	changed := deviceCall(t, phone, netproto.DeviceRequest{Action: netproto.DeviceActionPushPreferences, Preferences: &netproto.PushPreferences{AlwaysNotify: true}})
	if !changed.Preferences.Subscribed || changed.Preferences.HideDetails || !changed.Preferences.AlwaysNotify {
		t.Fatalf("preferences = %+v", changed.Preferences)
	}
	off := deviceCall(t, phone, netproto.DeviceRequest{Action: netproto.DeviceActionPushUnsubscribe})
	if off.Preferences.Subscribed {
		t.Fatalf("unsubscribe = %+v", off.Preferences)
	}

	// Desktops have no push subscription.
	desktop := desktopLogin(t, p.env.addr, newLinkTestKeys(t))
	defer func() { _ = desktop.Close() }()
	if e := deviceCallError(t, desktop, netproto.DeviceRequest{Action: netproto.DeviceActionPushConfig}); e.Code != errCodePermissionDenied {
		t.Fatalf("desktop push config = %+v", e)
	}
}

func TestPushSkipsForegroundPhoneAndHidesDetails(t *testing.T) {
	p := startPushTestEnv(t)
	phone, session := p.phoneSession(t, "Pixel")
	deviceCall(t, phone, netproto.DeviceRequest{Action: netproto.DeviceActionPushSubscribe, Subscription: browserSubscription(t, "https://push.example/1")})

	// The app is open: the event arrives live, no push.
	p.env.srv.pushNotify("user-uid", pushNotice{Kind: pushKindDM, Tag: "dm:ivy", From: "Ivy"})
	p.sender.none(t, 200*time.Millisecond)

	send(t, phone, netproto.MsgDeviceRequest, netproto.DeviceRequest{Action: netproto.DeviceActionActivity})
	waitFor(t, "background report", func() bool {
		session.mu.RLock()
		defer session.mu.RUnlock()
		return session.webHidden
	})
	p.env.srv.pushNotify("user-uid", pushNotice{Kind: pushKindDM, Tag: "dm:ivy", From: "Ivy"})
	got := p.sender.next(t)
	if got.payload.Kind != pushKindDM || got.payload.From != "Ivy" || got.payload.Tag != "dm:ivy" || got.msg.Topic != pushTopic("dm:ivy") {
		t.Fatalf("push = %+v", got)
	}

	deviceCall(t, phone, netproto.DeviceRequest{Action: netproto.DeviceActionPushPreferences, Preferences: &netproto.PushPreferences{HideDetails: true}})
	p.env.srv.pushNotify("user-uid", pushNotice{Kind: pushKindMention, Tag: "channel:4", From: "Ivy", Place: "design"})
	got = p.sender.next(t)
	if got.payload.From != "" || got.payload.Place != "" || got.payload.Kind != pushKindMention {
		t.Fatalf("hidden-details push = %+v", got.payload)
	}
	raw, _ := json.Marshal(got.payload)
	if string(raw) != `{"kind":"mention","tag":"channel:4","at":`+jsonInt(got.payload.At)+`}` {
		t.Fatalf("hidden-details payload carries more than kind and tag: %s", raw)
	}
}

func jsonInt(v int64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func TestPushWaitsWhileDesktopIsActiveAndReadCancels(t *testing.T) {
	previous := pushDesktopDelay
	pushDesktopDelay = 300 * time.Millisecond
	defer func() { pushDesktopDelay = previous }()

	p := startPushTestEnv(t)
	phone, session := p.phoneSession(t, "Pixel")
	deviceCall(t, phone, netproto.DeviceRequest{Action: netproto.DeviceActionPushSubscribe, Subscription: browserSubscription(t, "https://push.example/1")})
	session.mu.Lock()
	session.webHidden = true
	session.mu.Unlock()

	desktop := desktopLogin(t, p.env.addr, newLinkTestKeys(t))
	defer func() { _ = desktop.Close() }()
	send(t, desktop, netproto.MsgDeviceRequest, netproto.DeviceRequest{Action: netproto.DeviceActionActivity, Active: true})
	waitFor(t, "desktop activity", func() bool { return p.env.srv.desktopActive("user-uid", time.Now()) })

	// Read on the desktop within the delay: the push is never sent, and the
	// phone's live session hears about the read.
	p.env.srv.pushNotify("user-uid", pushNotice{Kind: pushKindDM, Tag: "dm:ivy", From: "Ivy"})
	waitFor(t, "delayed push", func() bool {
		p.env.srv.push.mu.Lock()
		defer p.env.srv.push.mu.Unlock()
		return len(p.env.srv.push.pending) == 1
	})
	send(t, desktop, netproto.MsgDeviceRequest, netproto.DeviceRequest{Action: netproto.DeviceActionRead, Read: &netproto.ReadMarker{Scope: netproto.ReadScopeDM, ID: "ivy"}})
	var event netproto.DeviceEvent
	if err := netproto.Decode(readOfType(t, phone, netproto.MsgDeviceEvent), &event); err != nil {
		t.Fatal(err)
	}
	if event.Event != netproto.DeviceEventRead || event.Read == nil || event.Read.ID != "ivy" {
		t.Fatalf("read event = %+v", event)
	}
	p.sender.none(t, 2*pushDesktopDelay)

	// Unread after the delay: sent.
	p.env.srv.pushNotify("user-uid", pushNotice{Kind: pushKindDM, Tag: "dm:rowan", From: "Rowan"})
	p.sender.none(t, pushDesktopDelay/2)
	if got := p.sender.next(t); got.payload.Tag != "dm:rowan" {
		t.Fatalf("delayed push = %+v", got.payload)
	}

	// Calls never wait.
	p.env.srv.pushNotify("user-uid", pushNotice{Kind: pushKindCall, Tag: "call:1", From: "Rowan"})
	got := p.sender.next(t)
	if got.payload.Kind != pushKindCall || got.msg.Urgency != webpush.UrgencyHigh || got.msg.TTL != pushCallTTL {
		t.Fatalf("call push = %+v", got)
	}

	// Reading a conversation whose notification was shown clears it.
	send(t, desktop, netproto.MsgDeviceRequest, netproto.DeviceRequest{Action: netproto.DeviceActionRead, Read: &netproto.ReadMarker{Scope: netproto.ReadScopeDM, ID: "rowan"}})
	if got := p.sender.next(t); got.payload.Kind != pushKindClear || got.payload.Tag != "dm:rowan" || got.msg.Topic != pushTopic("dm:rowan") {
		t.Fatalf("clear push = %+v", got.payload)
	}

	// "Always notify this phone" skips the delay.
	deviceCall(t, phone, netproto.DeviceRequest{Action: netproto.DeviceActionPushPreferences, Preferences: &netproto.PushPreferences{AlwaysNotify: true}})
	p.env.srv.pushNotify("user-uid", pushNotice{Kind: pushKindDM, Tag: "dm:ivy", From: "Ivy"})
	select {
	case got := <-p.sender.sent:
		if got.payload.Tag != "dm:ivy" {
			t.Fatalf("always-notify push = %+v", got.payload)
		}
	case <-time.After(pushDesktopDelay / 2):
		t.Fatal("always-notify push waited for the desktop delay")
	}
}

func TestPushDropsGoneSubscriptions(t *testing.T) {
	p := startPushTestEnv(t)
	phone, session := p.phoneSession(t, "Pixel")
	deviceCall(t, phone, netproto.DeviceRequest{Action: netproto.DeviceActionPushSubscribe, Subscription: browserSubscription(t, "https://push.example/gone")})
	session.mu.Lock()
	session.webHidden = true
	session.mu.Unlock()
	p.sender.gone["https://push.example/gone"] = true
	p.env.srv.pushNotify("user-uid", pushNotice{Kind: pushKindDM, Tag: "dm:ivy"})
	waitFor(t, "subscription removal", func() bool {
		subs, _ := p.pushes.PushSubscriptions(t.Context(), "user-uid")
		return len(subs) == 0
	})
}

// webPipe is a connection the server treats as the mobile web transport.
type webPipe struct{ net.Conn }

func (webPipe) WebTransport() bool { return true }

func TestWebSessionsAreMarkedOnMobile(t *testing.T) {
	p := startPushTestEnv(t)
	keys := newLinkTestKeys(t)
	if _, err := p.pushes.AddDevice(t.Context(), store.NewDevice{Principal: "user-uid", UserID: 2, Kind: store.DeviceKindWeb, Name: "Pixel", Ed25519Public: keys.public, X25519Public: keys.x25519}, 10); err != nil {
		t.Fatal(err)
	}
	phone, serverSide := net.Pipe()
	t.Cleanup(func() { _ = phone.Close() })
	go func() { _ = p.env.srv.ServeExternalConn(context.Background(), webPipe{serverSide}) }()
	resp := keyLoginOn(t, phone, keys)
	if !resp.OK {
		t.Fatalf("phone login = %+v", resp)
	}
	// net.Pipe is unbuffered: keep reading so the server's writes complete.
	go func() { _, _ = io.Copy(io.Discard, phone) }()
	waitFor(t, "phone presence", func() bool {
		c, ok := p.env.srv.deps.State.GetClient(resp.ClientID)
		return ok && c.OnMobile
	})

	desktop := desktopLogin(t, p.env.addr, newLinkTestKeys(t))
	defer func() { _ = desktop.Close() }()
	var desktopID string
	for _, c := range p.env.srv.sessionsOf("user-uid") {
		if !c.isWebTransport() {
			desktopID = c.ID
		}
	}
	if c, ok := p.env.srv.deps.State.GetClient(desktopID); !ok || c.OnMobile {
		t.Fatalf("desktop session marked on mobile: %+v", c)
	}
}

func TestSilentPushRevokesOnlyApple(t *testing.T) {
	if !silentPushRevokes("https://web.push.apple.com/QGxyz") || silentPushRevokes("https://fcm.googleapis.com/fcm/send/x") ||
		silentPushRevokes("https://push.apple.com.evil.example/x") {
		t.Fatal("silent push classification is wrong")
	}
}

func TestReadMarkerValidation(t *testing.T) {
	for _, m := range []netproto.ReadMarker{{}, {Scope: "dm"}, {Scope: "inbox", ID: "x"}} {
		if m.Valid() {
			t.Fatalf("%+v accepted", m)
		}
	}
	if !(netproto.ReadMarker{Scope: netproto.ReadScopeChannel, ID: "4"}).Valid() {
		t.Fatal("channel marker rejected")
	}
	if (netproto.DeviceRequest{Action: netproto.DeviceActionRead}).Valid() {
		t.Fatal("read without marker accepted")
	}
}
