package webrtc

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
	"go.uber.org/zap"
)

// ErrNativeSlot rejects a native media peer offer that would send anything
// other than declared screen and screenaudio tracks.
var ErrNativeSlot = errors.New("webrtc: the native media peer may only send declared screen and screenaudio tracks")

// NativePeerID is the engine key of clientID's native media peer.
func NativePeerID(clientID string) string { return clientID + slotSep + "native" }

// HandleNativeOffer applies an offer of clientID's native media peer: a
// second, publish-only peer whose screen and screenaudio tracks are routed as
// clientID's own (see Router.AttachNativePeer). slots maps the offer's MSID
// track IDs to their slot. The native peer lives only while the browser peer
// does: ClosePeer, a browser peer rebuild, a channel leave or move and screen
// revocation close it. It never receives media or server offers.
func (v *Voice) HandleNativeOffer(clientID, offerSDP string, slots map[string]string, onLocalCandidate func(candidate, sdpMid string, mlineIndex uint16)) (string, error) {
	if err := checkNativeOffer(offerSDP, slots); err != nil {
		return "", err
	}
	fingerprint, err := remoteFingerprint(offerSDP)
	if err != nil {
		return "", err
	}
	owner := v.engine.PeerConnection(clientID)
	if owner == nil {
		return "", ErrNoPeer
	}
	key := NativePeerID(clientID)
	if existing := v.engine.PeerConnection(key); existing != nil {
		if remote := existing.pc.RemoteDescription(); remote != nil {
			if previous, err := remoteFingerprint(remote.SDP); err == nil && previous == fingerprint {
				// Waking the readers also resumes a track that lost its slot.
				v.router.SetNativeTrackSlots(clientID, existing, slots)
				v.engine.mu.RLock()
				bounds := v.engine.videoBounds
				v.engine.mu.RUnlock()
				return existing.handleOffer(offerSDP, &bounds)
			}
		}
	}
	_ = v.CloseNativePeer(clientID)
	wrapper, err := v.engine.NewPeerConnection(key)
	if err != nil {
		return "", err
	}
	v.router.AttachNativePeer(clientID, wrapper, owner)
	// ClosePeer and rebuilds replace the browser peer before closing the
	// native one, so this recheck cannot leave a native peer behind its owner.
	if v.engine.PeerConnection(clientID) != owner {
		v.closeNative(clientID, wrapper)
		return "", ErrNoPeer
	}
	v.router.SetNativeTrackSlots(clientID, wrapper, slots)
	answer, err := wrapper.HandleOffer(offerSDP)
	if err != nil {
		v.closeNative(clientID, wrapper)
		return "", err
	}
	forwardLocalCandidates(wrapper, onLocalCandidate)
	v.logger.Info("native media peer established", zap.String("client_id", clientID))
	return answer, nil
}

// CloseNativePeer closes clientID's native media peer, if any. Its tracks
// stop; the client's publications are left to MsgVideoStreamControl.
func (v *Voice) CloseNativePeer(clientID string) error {
	if wrapper := v.engine.PeerConnection(NativePeerID(clientID)); wrapper != nil {
		v.closeNative(clientID, wrapper)
	}
	return nil
}

// closeNativeOwnedBy closes clientID's native media peer only if it was
// created for the browser peer owner, sparing a replacement session's peer.
func (v *Voice) closeNativeOwnedBy(clientID string, owner *PeerConnectionWrapper) {
	if native := v.router.nativePeerOf(clientID); native.pc != nil && native.owner == owner {
		v.closeNative(clientID, native.pc)
	}
}

// closeNative closes exactly wrapper; a stale wrapper leaves its
// replacement's registration and declarations alone.
func (v *Voice) closeNative(clientID string, wrapper *PeerConnectionWrapper) {
	v.router.DetachNativePeer(clientID, wrapper)
	if !v.engine.closePeerIf(NativePeerID(clientID), wrapper) {
		_ = wrapper.Close()
	}
}

// checkNativeOffer admits only screen and screenaudio declarations, and only
// sending media sections whose MSID track is declared with a matching kind.
func checkNativeOffer(offerSDP string, slots map[string]string) error {
	for _, slot := range slots {
		if !nativeSlot(slot) {
			return ErrNativeSlot
		}
	}
	var description sdp.SessionDescription
	if err := description.Unmarshal([]byte(offerSDP)); err != nil {
		return fmt.Errorf("parsing remote SDP: %w", err)
	}
	for _, media := range description.MediaDescriptions {
		kind := webrtc.NewRTPCodecType(media.MediaName.Media)
		if kind == 0 || media.MediaName.Port.Value == 0 {
			continue // data channel or rejected section
		}
		sending := true
		for _, attribute := range media.Attributes {
			if attribute.Key == "recvonly" || attribute.Key == "inactive" {
				sending = false
			}
		}
		if !sending {
			continue
		}
		msid, _ := media.Attribute("msid")
		fields := strings.Fields(msid)
		if len(fields) != 2 {
			return ErrNativeSlot
		}
		if slot := slots[fields[1]]; slotKinds[slot] != kind {
			return ErrNativeSlot
		}
	}
	return nil
}
