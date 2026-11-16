package server

import (
	"context"
	"errors"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/webrtc"
)

// nativePeerBackend is the optional VoiceBackend capability behind the
// native media peer (netproto.CapabilityNativeMediaPeer).
type nativePeerBackend interface {
	HandleNativeOffer(clientID, offerSDP string, slots map[string]string, onLocalCandidate func(candidate, sdpMid string, mlineIndex uint16)) (string, error)
	CloseNativePeer(clientID string) error
}

var _ nativePeerBackend = (*webrtc.Voice)(nil)

// handleNativeOffer negotiates the session's publish-only native media peer.
// An empty SDP closes it. It needs the screen share capability in the current
// channel and, on an encrypted server, the same MediaE2EE declaration as the
// browser peer. It takes no media seat: it lives inside the browser peer's
// session, which already holds one.
func (s *TCPServer) handleNativeOffer(ctx context.Context, client *Client, msg netproto.WebRTCOffer) error {
	native, ok := s.deps.Voice.(nativePeerBackend)
	if !ok || msg.Peer != netproto.PeerNative {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "webrtc_offer for an unknown peer")
	}
	if msg.SDP == "" {
		return native.CloseNativePeer(client.ID)
	}
	if reason := s.mediaE2EEOfferRejection(client, msg); reason != "" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, reason)
	}
	return s.roleChannelControl(ctx, client, authorization.ShareScreen, true, func(ctx context.Context, _ int64) error {
		slots := make(map[string]string, len(msg.Tracks))
		for _, t := range msg.Tracks {
			if t.TrackID != "" {
				slots[t.TrackID] = t.Slot
			}
		}
		answer, err := native.HandleNativeOffer(client.ID, msg.SDP, slots, func(candidate, sdpMid string, mlineIndex uint16) {
			_ = s.writeVoiceSignal(client, netproto.MsgICECandidate, netproto.ICECandidate{
				Candidate: candidate, SDPMid: sdpMid, SDPMLineIndex: mlineIndex, Peer: netproto.PeerNative,
			})
		})
		switch {
		case err == nil:
			return s.writeMessage(client, netproto.MsgWebRTCAnswer, netproto.WebRTCAnswer{SDP: answer, Peer: netproto.PeerNative})
		case errors.Is(err, webrtc.ErrNativeSlot):
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, err.Error())
		case errors.Is(err, webrtc.ErrNoPeer):
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "the native media peer needs an established voice session")
		}
		if errors.Is(err, webrtc.ErrPeerReset) {
			// Unlike the browser peer, a stuck native peer does not end the session.
			_ = native.CloseNativePeer(client.ID)
		}
		s.logger.Warn("native webrtc offer failed", zap.String("client_id", client.ID), zap.Error(err))
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "native webrtc offer failed")
	})
}
