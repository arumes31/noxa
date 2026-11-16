package netproto

// Native media peer (desktop native screen capture).
//
// A server that advertises CapabilityNativeMediaPeer accepts a SECOND WebRTC
// peer per session, negotiated over the same WebRTCOffer / WebRTCAnswer /
// ICECandidate messages with Peer == PeerNative. It is publish-only and may
// carry only the "screen" and "screenaudio" slots; its media is attributed to
// the owning session exactly as if the browser peer had published it. An
// empty Peer addresses the existing browser peer, unchanged. A WebRTCOffer
// with Peer == PeerNative and an empty SDP closes the native peer.
const (
	CapabilityNativeMediaPeer = "native-media-peer-v1"
	PeerNative                = "native"
)
