package server

import (
	"context"
	"maps"
	"net"
	"slices"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/webrtc"
)

// nativeVoice is a fakeVoice that also implements nativePeerBackend.
type nativeVoice struct {
	fakeVoice
	nativeOffers    []map[string]string
	nativeCandidate func(candidate, sdpMid string, mlineIndex uint16)
	nativeClosed    int
	icePeers        []string
}

func (v *nativeVoice) HandleNativeOffer(_, _ string, slots map[string]string, onCandidate func(candidate, sdpMid string, mlineIndex uint16)) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.nativeOffers = append(v.nativeOffers, slots)
	v.nativeCandidate = onCandidate
	return "native-answer", nil
}

func (v *nativeVoice) CloseNativePeer(string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.nativeClosed++
	return nil
}

func (v *nativeVoice) AddICECandidate(peerID, _, _ string, _ uint16) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.icePeers = append(v.icePeers, peerID)
	return nil
}

func startNativeEnv(t *testing.T, extra ...authorization.Capability) (*testEnv, *nativeVoice) {
	t.Helper()
	backend := serverRoleFixture()
	backend.policy.OwnerID = 1
	backend.policy.Roles[0].Permissions = append([]authorization.Capability{authorization.ViewChannel, authorization.Connect}, extra...)
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	voice := &nativeVoice{}
	voice.answerSDP = "browser-answer"
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Voice = voice })
	t.Cleanup(env.stop)
	return env, voice
}

func dialNative(t *testing.T, env *testEnv) (net.Conn, netproto.AuthResponse) {
	t.Helper()
	conn := dialRetry(t, env.addr)
	t.Cleanup(func() { _ = conn.Close() })
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw"})
	var resp netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &resp); err != nil || !resp.OK {
		t.Fatalf("login: %v %+v", err, resp)
	}
	readOfType(t, conn, netproto.MsgSnapshot)
	placeVoiceTestClients(t, env, 1, resp.ClientID)
	return conn, resp
}

func TestNativePeerSignaling(t *testing.T) {
	env, voice := startNativeEnv(t, authorization.ShareScreen)
	conn, resp := dialNative(t, env)
	if !slices.Contains(resp.Capabilities, netproto.CapabilityNativeMediaPeer) {
		t.Fatalf("capability not advertised: %v", resp.Capabilities)
	}

	// An old client's offer still reaches the browser peer, unchanged.
	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "browser-offer"})
	var answer netproto.WebRTCAnswer
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgWebRTCAnswer), &answer); err != nil || answer.SDP != "browser-answer" || answer.Peer != "" {
		t.Fatalf("browser answer = %+v, %v", answer, err)
	}

	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "native-offer", Peer: netproto.PeerNative,
		Tracks: []netproto.TrackSlot{{TrackID: "s", Slot: webrtc.SlotScreen}, {TrackID: "a", Slot: webrtc.SlotScreenAudio}}})
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgWebRTCAnswer), &answer); err != nil || answer.SDP != "native-answer" || answer.Peer != netproto.PeerNative {
		t.Fatalf("native answer = %+v, %v", answer, err)
	}
	voice.mu.Lock()
	browserOffers, nativeOffers, candidate := len(voice.offers), slices.Clone(voice.nativeOffers), voice.nativeCandidate
	voice.mu.Unlock()
	if browserOffers != 1 || len(nativeOffers) != 1 || !maps.Equal(nativeOffers[0], map[string]string{"s": webrtc.SlotScreen, "a": webrtc.SlotScreenAudio}) {
		t.Fatalf("browser offers %d, native offers %v", browserOffers, nativeOffers)
	}

	candidate("candidate:1 udp 1 127.0.0.1 9 typ host", "0", 0)
	var ice netproto.ICECandidate
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgICECandidate), &ice); err != nil || ice.Peer != netproto.PeerNative {
		t.Fatalf("server candidate = %+v, %v", ice, err)
	}
	send(t, conn, netproto.MsgICECandidate, netproto.ICECandidate{Candidate: "c", Peer: netproto.PeerNative})
	send(t, conn, netproto.MsgICECandidate, netproto.ICECandidate{Candidate: "c"})
	waitFor(t, "candidates routed", func() bool {
		voice.mu.Lock()
		defer voice.mu.Unlock()
		return slices.Equal(voice.icePeers, []string{webrtc.NativePeerID(resp.ClientID), resp.ClientID})
	})

	for _, unknown := range []func(){
		func() { send(t, conn, netproto.MsgICECandidate, netproto.ICECandidate{Candidate: "c", Peer: "other"}) },
		func() { send(t, conn, netproto.MsgWebRTCAnswer, netproto.WebRTCAnswer{SDP: "a", Peer: netproto.PeerNative}) },
		func() { send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "o", Peer: "other"}) },
	} {
		unknown()
		if e := readError(t, conn); e.Code != errCodeMalformed {
			t.Fatalf("unknown peer error = %+v", e)
		}
	}

	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{Peer: netproto.PeerNative})
	waitFor(t, "native close", func() bool {
		voice.mu.Lock()
		defer voice.mu.Unlock()
		return voice.nativeClosed == 1
	})
}

func TestNativePeerRequiresScreenSharePermission(t *testing.T) {
	env, voice := startNativeEnv(t)
	conn, _ := dialNative(t, env)
	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "native-offer", Peer: netproto.PeerNative,
		Tracks: []netproto.TrackSlot{{TrackID: "s", Slot: webrtc.SlotScreen}}})
	readError(t, conn)
	voice.mu.Lock()
	defer voice.mu.Unlock()
	if len(voice.nativeOffers) != 0 {
		t.Fatal("native offer reached the backend without screen share permission")
	}
}

func TestNativePeerCapabilityNeedsBackend(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	_, resp := dialResumable(t, env.addr, netproto.Authenticate{Username: "user-uid", Password: "pw"})
	if slices.Contains(resp.Capabilities, netproto.CapabilityNativeMediaPeer) {
		t.Fatal("capability advertised without a native peer backend")
	}
}
