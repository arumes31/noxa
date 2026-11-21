package server

import (
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"noxa/internal/auth"
	"noxa/internal/e2ee"
	"noxa/internal/netproto"
	"noxa/internal/state"
	"noxa/internal/store"
)

func authReply(t *testing.T, conn net.Conn) netproto.AuthResponse {
	t.Helper()
	var resp netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// A password login that presents its key without a proof keeps the key for
// what the session signs (signed directory publish) and binds it unproven,
// but registers no device; a proven login does.
func TestUnprovenKeyPasswordLoginRegistersNoDevice(t *testing.T) {
	devices := &fakeDevices{}
	env := startTestEnvDeps(t, nil, nil, func(deps *Deps) { deps.Devices = devices })
	defer env.stop()
	keys := newLinkTestKeys(t)
	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", PublicKey: keys.public, AuthorizationModels: []string{netproto.AuthorizationModelRolesV1}})
	resp := authReply(t, conn)
	if !resp.OK {
		t.Fatalf("password login: %s", resp.Reason)
	}
	readOfType(t, conn, netproto.MsgSnapshot)
	env.auth.mu.Lock()
	bindings := append([][3]any(nil), env.auth.bindings...)
	env.auth.mu.Unlock()
	if len(bindings) != 1 || bindings[0][1] != keys.public || bindings[0][2] != false {
		t.Fatalf("bindings = %v, want one unproven binding", bindings)
	}
	pub, _ := testX25519(t)
	signer, _ := auth.LoadPrivateKey(keys.private)
	record, err := e2ee.SignKeyRecord("user-uid", pub, signer)
	if err != nil {
		t.Fatal(err)
	}
	send(t, conn, netproto.MsgKeyPublish, netproto.KeyPublish{PublicKey: record})
	readOfType(t, conn, netproto.MsgChannelKey)
	if got, _ := env.srv.senderDMKey(mustServerClient(t, env, resp.ClientID)); got != record {
		t.Fatal("signed directory publish refused for an unproven key")
	}
	if listed := deviceCall(t, conn, netproto.DeviceRequest{Action: netproto.DeviceActionList}); len(listed.Devices) != 0 {
		t.Fatalf("an unproven key registered a device: %+v", listed.Devices)
	}
	if _, err := devices.DeviceByKey(t.Context(), keys.public); !errors.Is(err, store.ErrDeviceNotFound) {
		t.Fatalf("device row for an unproven key: %v", err)
	}

	// A later proven login proves the binding and registers the device.
	proven := dialRetry(t, env.addr)
	defer func() { _ = proven.Close() }()
	sendProvenPasswordAuth(t, proven, netproto.Authenticate{Username: "user-uid", Password: "pw", PublicKey: keys.public, AuthorizationModels: []string{netproto.AuthorizationModelRolesV1}}, keys.private)
	if resp := authReply(t, proven); !resp.OK {
		t.Fatalf("proven login: %s", resp.Reason)
	}
	if listed := deviceCall(t, proven, netproto.DeviceRequest{Action: netproto.DeviceActionList}); len(listed.Devices) != 1 || !listed.Devices[0].Current {
		t.Fatalf("device registration after a proven login = %+v", listed.Devices)
	}
	if d, err := devices.DeviceByKey(t.Context(), keys.public); err != nil || d.KeyUnproven {
		t.Fatalf("registered device = %+v, %v; want a proven key", d, err)
	}
	env.auth.mu.Lock()
	last := env.auth.bindings[len(env.auth.bindings)-1]
	env.auth.mu.Unlock()
	if last[2] != true {
		t.Fatalf("proven login binding = %v, want proven", last)
	}
	if u, err := env.auth.LookupUserByPublicKey(t.Context(), keys.public); err != nil || u.UniqueID != "user-uid" {
		t.Fatalf("proven key does not route to its account: %+v, %v", u, err)
	}
}

// Someone else's key presented without a proof binds only as unproven and
// registers no device, so it never routes the key owner's guest login into
// the account.
func TestUnprovenKeyNeverRoutesKeyLogin(t *testing.T) {
	devices := &fakeDevices{}
	env := startTestEnvDeps(t, nil, nil, func(deps *Deps) { deps.Devices = devices })
	defer env.stop()
	victim := newLinkTestKeys(t)
	attacker := newLinkTestKeys(t)

	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", PublicKey: victim.public, AuthorizationModels: []string{netproto.AuthorizationModelRolesV1}})
	if resp := authReply(t, conn); !resp.OK {
		t.Fatalf("password login: %s", resp.Reason)
	}
	readOfType(t, conn, netproto.MsgSnapshot)
	deviceCall(t, conn, netproto.DeviceRequest{Action: netproto.DeviceActionList})
	if _, err := devices.DeviceByKey(t.Context(), victim.public); !errors.Is(err, store.ErrDeviceNotFound) {
		t.Fatalf("a key named without proof registered a device: %v", err)
	}

	// A proof made with another key is not a proof.
	forged := dialRetry(t, env.addr)
	defer func() { _ = forged.Close() }()
	sendProvenPasswordAuth(t, forged, netproto.Authenticate{Username: "user-uid", Password: "pw", PublicKey: victim.public}, attacker.private)
	if resp := authReply(t, forged); !resp.OK {
		t.Fatalf("password login with a bad proof: %s", resp.Reason)
	}
	env.auth.mu.Lock()
	for _, b := range env.auth.bindings {
		if b[2] != false {
			t.Errorf("binding %v recorded as proven", b)
		}
	}
	env.auth.mu.Unlock()

	_, guest := keyLogin(t, env.addr, victim)
	if !guest.OK || !guest.Guest || guest.UniqueID != victim.uniqueID {
		t.Fatalf("victim key login = %+v, want its own guest identity", guest)
	}
}

// Before authentication only IP bans are revealed; an account ban is applied
// once the credentials are proven.
func TestAccountBanIsNotRevealedBeforeAuthentication(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	victim := newLinkTestKeys(t)
	env.auth.setBan("user-uid", &auth.Ban{ID: 1, Reason: "spam"})
	env.auth.setBan(victim.uniqueID, &auth.Ban{ID: 2, Reason: "spam"})

	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "wrong"})
	if resp := authReply(t, conn); resp.Reason != "invalid credentials" {
		t.Fatalf("wrong password for a banned account = %q, want invalid credentials", resp.Reason)
	}
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid"})
	readOfType(t, conn, netproto.MsgAuthChallenge) // no ban answer before a signature
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw"})
	if resp := authReply(t, conn); resp.OK || resp.Reason != "banned: spam" {
		t.Fatalf("banned account after authentication = %+v", resp)
	}

	// Guest signature path: a bad signature learns nothing, a good one is refused.
	guest := dialRetry(t, env.addr)
	defer func() { _ = guest.Close() }()
	send(t, guest, netproto.MsgAuthenticate, netproto.Authenticate{Username: victim.uniqueID, Anonymous: true})
	readOfType(t, guest, netproto.MsgAuthChallenge)
	send(t, guest, netproto.MsgAuthSignature, netproto.AuthSignature{PublicKey: victim.public, Signature: make([]byte, 64)})
	if resp := authReply(t, guest); resp.Reason != "invalid credentials" {
		t.Fatalf("bad signature for a banned key = %q, want invalid credentials", resp.Reason)
	}
	if resp := keyLoginOn(t, guest, victim); resp.OK || resp.Reason != "banned: spam" {
		t.Fatalf("banned key after its signature = %+v", resp)
	}

	// IP bans still apply before any credential check.
	env.auth.setBan("127.0.0.1", &auth.Ban{ID: 3})
	ipConn := dialRetry(t, env.addr)
	defer func() { _ = ipConn.Close() }()
	send(t, ipConn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "admin-uid", Password: "wrong"})
	if resp := authReply(t, ipConn); resp.Reason != "banned" {
		t.Fatalf("IP-banned login = %q, want banned", resp.Reason)
	}
}

// Signature logins do no KDF work, so saturated KDF slots must not block them.
func TestKeyLoginIgnoresSaturatedKDFSlots(t *testing.T) {
	limiter := auth.NewLoginFailureLimiter(auth.LoginFailureLimiterConfig{MaxConcurrentKDF: 1})
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.LoginLimiter = limiter })
	defer env.stop()
	held, ok := limiter.Reserve(auth.LoginFailureScope("tcp", "busy"))
	if !ok {
		t.Fatal("reservation rejected")
	}
	defer held.Cancel()
	conn, resp := keyLogin(t, env.addr, newLinkTestKeys(t))
	defer func() { _ = conn.Close() }()
	if !resp.OK {
		t.Fatalf("key login with saturated KDF slots: %s", resp.Reason)
	}
}

// Saturated KDF slots refuse a password login as busy, not as a lockout.
func TestPasswordLoginWithSaturatedKDFSlotsIsBusy(t *testing.T) {
	limiter := auth.NewLoginFailureLimiter(auth.LoginFailureLimiterConfig{MaxConcurrentKDF: 1})
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.LoginLimiter = limiter })
	defer env.stop()
	held, ok := limiter.Reserve(auth.LoginFailureScope("tcp", "busy"))
	if !ok {
		t.Fatal("reservation rejected")
	}
	defer held.Cancel()
	// Clients without the busy feature get the lockout text they already retry.
	for _, tc := range []struct {
		features []string
		want     string
	}{
		{[]string{netproto.ClientFeatureBusy}, netproto.ReasonServerBusy},
		{nil, "too many failed logins, try again later"},
	} {
		features, want := tc.features, tc.want
		conn := dialRetry(t, env.addr)
		send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", ClientFeatures: features})
		var resp netproto.AuthResponse
		if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &resp); err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
		if resp.OK || resp.Reason != want {
			t.Fatalf("features %v: response = %+v, want %q", features, resp, want)
		}
	}
}

// A server password that already verified costs no further KDF.
func TestKnownServerPasswordSkipsKDF(t *testing.T) {
	var verifications atomic.Int32
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
		d.ServerPasswordHash = "server-hash"
		d.VerifyServerPassword = func(password, _ string) error {
			verifications.Add(1)
			if password != "door" {
				return auth.ErrUserNotFound
			}
			return nil
		}
	})
	defer env.stop()
	for i := 0; i < 3; i++ {
		conn := dialRetry(t, env.addr)
		send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", ServerPassword: "door"})
		if resp := authReply(t, conn); !resp.OK {
			t.Fatalf("login %d: %s", i, resp.Reason)
		}
		_ = conn.Close()
	}
	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", ServerPassword: "wrong"})
	if resp := authReply(t, conn); resp.OK {
		t.Fatal("a wrong server password was accepted")
	}
	if got := verifications.Load(); got != 2 {
		t.Fatalf("server password KDF runs = %d, want 2 (first success, wrong password)", got)
	}
}

// Rejoining a password channel with a password that already verified takes
// no KDF slot, so members holding both slots cannot lock others out.
func TestKnownChannelPasswordSkipsKDF(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	hash, err := auth.HashPassword("door")
	if err != nil {
		t.Fatal(err)
	}
	locked := testChannel(1)
	locked.PasswordHash = hash
	env.state.AddChannel(locked)
	env.state.AddChannel(testChannel(2))
	conn, id := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1, Password: "door"})
	waitFor(t, "user in channel 1", func() bool { c, ok := env.state.GetClient(id); return ok && c.ChannelID == 1 })
	send(t, conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 2})
	waitFor(t, "user in channel 2", func() bool { c, ok := env.state.GetClient(id); return ok && c.ChannelID == 2 })

	for _, holder := range []string{"a", "b"} {
		held, ok := env.srv.channelPasswordLimiter.Reserve(auth.LoginFailureScope("channel", holder))
		if !ok {
			t.Fatal("reservation rejected")
		}
		defer held.Cancel()
	}
	send(t, conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1, Password: "door"})
	waitFor(t, "user back in channel 1", func() bool { c, ok := env.state.GetClient(id); return ok && c.ChannelID == 1 })
}

// An invisible admin stays hidden across a resume; a session that is no
// longer allowed to be invisible comes back online with its message.
func TestSessionResumeKeepsInvisibleOnlyForAdmins(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	resume := func(uid, status string) *state.Client {
		t.Helper()
		login := netproto.Authenticate{Username: uid, Password: "pw"}
		conn, first := dialResumable(t, env.addr, login)
		env.state.SetStatus(first.ClientID, status, "brb")
		_ = conn.Close()
		waitFor(t, "session parked", func() bool { _, ok := env.state.GetClient(first.ClientID); return !ok })
		login.ResumeToken = first.ResumeToken
		back, second := dialResumable(t, env.addr, login)
		t.Cleanup(func() { _ = back.Close() })
		if !second.Resumed {
			t.Fatal("session was not resumed")
		}
		c, _ := env.state.GetClient(second.ClientID)
		if c == nil {
			t.Fatal("resumed session missing")
		}
		return c
	}
	if c := resume("admin-uid", "invisible"); c.Status != "invisible" || c.StatusMessage != "brb" {
		t.Fatalf("resumed admin status = %q %q, want invisible brb", c.Status, c.StatusMessage)
	}
	if c := resume("user-uid", "invisible"); c.Status != "" || c.StatusMessage != "brb" {
		t.Fatalf("resumed non-admin status = %q %q, want online brb", c.Status, c.StatusMessage)
	}
}

// A session carries a linked device only if its login signed for that
// device's key. A password login that merely names the key signs in without
// a device, so it stays behind every device-scoped check.
func TestPasswordLoginNamingADeviceKeyNeedsProofToAttach(t *testing.T) {
	env, devices := startDeviceTestEnv(t)
	owner := newLinkTestKeys(t)
	desktop := desktopLogin(t, env.addr, owner)
	defer func() { _ = desktop.Close() }()
	listed := deviceCall(t, desktop, netproto.DeviceRequest{Action: netproto.DeviceActionList})
	if len(listed.Devices) != 1 || !listed.Devices[0].FirstDevice || !listed.Devices[0].Current {
		t.Fatalf("setup: devices = %+v", listed.Devices)
	}
	first := listed.Devices[0]

	named := dialRetry(t, env.addr)
	defer func() { _ = named.Close() }()
	send(t, named, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", PublicKey: owner.public})
	resp := authReply(t, named)
	if !resp.OK {
		t.Fatalf("password login naming a key: %s", resp.Reason)
	}
	if session := mustServerClient(t, env, resp.ClientID); resp.DeviceID != "" || session.currentDeviceID() != "" {
		t.Fatalf("unproven key attached device %q (session %q)", resp.DeviceID, session.currentDeviceID())
	}
	// The device list still works and marks no device as this session's.
	for _, d := range deviceCall(t, named, netproto.DeviceRequest{Action: netproto.DeviceActionList}).Devices {
		if d.Current {
			t.Fatalf("unproven session listed %s as its own device", d.DeviceID)
		}
	}
	refused := deviceCallError(t, named, netproto.DeviceRequest{Action: netproto.DeviceActionRevoke, DeviceID: first.DeviceID})
	if !strings.Contains(refused.Message, "first device cannot") {
		t.Fatalf("revoking the first device from an unproven session = %+v", refused)
	}
	if d, err := devices.DeviceByKey(t.Context(), owner.public); err != nil || !d.Active() {
		t.Fatalf("first device after the refused revoke = %+v, %v", d, err)
	}

	// A device row stored unproven is no exception; a proven login attaches
	// and upgrades it.
	legacy := newLinkTestKeys(t)
	row, err := devices.AddDevice(t.Context(), store.NewDevice{Principal: "user-uid", UserID: 2, Kind: store.DeviceKindDesktop, Name: "Desktop", Ed25519Public: legacy.public, KeyUnproven: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	unproven := dialRetry(t, env.addr)
	defer func() { _ = unproven.Close() }()
	send(t, unproven, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", PublicKey: legacy.public})
	if resp := authReply(t, unproven); !resp.OK || resp.DeviceID != "" {
		t.Fatalf("unproven login for an unproven row = %+v, want signed in without a device", resp)
	}
	proven := dialRetry(t, env.addr)
	defer func() { _ = proven.Close() }()
	sendProvenPasswordAuth(t, proven, netproto.Authenticate{Username: "user-uid", Password: "pw", PublicKey: legacy.public}, legacy.private)
	if resp := authReply(t, proven); !resp.OK || resp.DeviceID != row.DeviceID {
		t.Fatalf("proven login = %+v, want device %s", resp, row.DeviceID)
	}
	waitFor(t, "device key proven", func() bool {
		d, err := devices.DeviceByKey(t.Context(), legacy.public)
		return err == nil && !d.KeyUnproven
	})
}
