package server

// media_e2ee.go: mandatory channel media end-to-end encryption (Task 2).
//
// The server never sees media keys: clients encrypt every encoded frame
// before it reaches the SFU and distribute per-slot epoch keys only as
// authenticated ratchet controls (purpose "control"). The server's part is
// to refuse anything that would carry channel media without that
// protection, with an explicit, understandable reason:
//
//   - WebRTC offers must declare netproto.MediaE2EEVersion; old clients,
//     keyless guests and sessions without a published secure device are
//     refused before the voice backend sees the offer;
//   - server recording, whisper (cross-scope audio without keys) and
//     plaintext stream previews are refused;
//   - the SFU negotiates only Opus and VP8, the encrypted codecs whose
//     packetization and dimension admission are proven (webrtc.MediaPolicy).
//
// Metadata (membership, packet sizes and timing, the VP8 keyframe header
// dimensions) stays visible to the server by design.

import (
	"strings"

	"noxa/internal/netproto"
)

const eventMediaDeviceChanged = "media_device_changed"

// mediaDeviceEvent announces the device of a session that now publishes and
// receives encrypted media. Role delivery replaces it with a per-recipient
// snapshot, so visibility rules apply unchanged.
type mediaDeviceEvent struct {
	ClientID    string `json:"client_id"`
	MediaDevice string `json:"media_device"`
}

// mediaE2EERequired reports whether channel media must be end-to-end
// encrypted on this server.
func (s *TCPServer) mediaE2EERequired() bool { return s.mediaE2EE }

// mediaE2EEAvailable reports whether clients can distribute media keys: the
// signed device directory and ratchet admission must both be on.
func (s *TCPServer) mediaE2EEAvailable() bool {
	if !s.mediaE2EE || !s.olmAdmissionEnabled {
		return false
	}
	_, _, ok := s.olmDirectory()
	return ok
}

func mediaE2EEError(reason, explanation string) string { return reason + ": " + explanation }

// mediaE2EEOfferRejection returns the refusal message for a client WebRTC
// offer, or "" when the offer may proceed.
func (s *TCPServer) mediaE2EEOfferRejection(client *Client, msg netproto.WebRTCOffer) string {
	if !s.mediaE2EERequired() {
		return ""
	}
	if !s.mediaE2EEAvailable() {
		return mediaE2EEError(netproto.MediaE2EEReasonUnavailable, "channel voice is end-to-end encrypted, but this server's secure device directory is disabled")
	}
	if msg.MediaE2EE != netproto.MediaE2EEVersion {
		return mediaE2EEError(netproto.MediaE2EEReasonClientOutdated, "channel voice is end-to-end encrypted; update noXa to join voice")
	}
	if s.deps != nil && s.deps.State != nil {
		if c, ok := s.deps.State.GetClient(client.ID); ok && c.Status == "invisible" {
			return mediaE2EEError(netproto.MediaE2EEReasonInvisible, "encrypted voice is unavailable while you are invisible; become visible to join voice")
		}
	}
	if s.voiceRecipients(client) >= MediaE2EEMaxVoice {
		return mediaE2EEError(netproto.MediaE2EEReasonChannelFull, "this encrypted voice channel is full")
	}
	if client.currentDeviceID() == "" {
		if strings.HasPrefix(client.uniqueID(), "guest:") {
			return mediaE2EEError(netproto.MediaE2EEReasonKeylessGuest, "channel voice is end-to-end encrypted; guest sessions without a key identity cannot join voice")
		}
		return mediaE2EEError(netproto.MediaE2EEReasonUnavailable, "channel voice is end-to-end encrypted; this session has no secure device yet")
	}
	return ""
}

// announceMediaDevice records and announces the session's media device once
// its first encrypted offer was accepted.
func (s *TCPServer) announceMediaDevice(client *Client) {
	if !s.mediaE2EERequired() || s.deps == nil || s.deps.State == nil {
		return
	}
	device := client.currentDeviceID()
	if device == "" || !s.deps.State.SetMediaDevice(client.ID, device) {
		return
	}
	s.broadcastEvent(eventMediaDeviceChanged, mediaDeviceEvent{ClientID: client.ID, MediaDevice: device})
}

// mediaE2EECodecs keeps only codecs that are proven end-to-end encrypted
// through the SFU (VP8); H.264/HEVC are not offered while media is encrypted.
func (s *TCPServer) mediaE2EECodecs(msg *netproto.VideoStreamControl) {
	if !s.mediaE2EERequired() {
		return
	}
	kept := msg.Codecs[:0]
	for _, codec := range msg.Codecs {
		if strings.EqualFold(codec, "vp8") {
			kept = append(kept, codec)
		}
	}
	msg.Codecs = kept
	msg.HEVCLevelID = 0
}

// mediaE2EEStreamRejection refuses stream controls that would bypass
// encryption (the plaintext JPEG preview upload/cache).
func (s *TCPServer) mediaE2EEStreamRejection(msg netproto.VideoStreamControl) string {
	if !s.mediaE2EERequired() {
		return ""
	}
	switch msg.Action {
	case "preview_upload", "preview":
		return mediaE2EEError(netproto.MediaE2EEReasonPreview, "stream previews are created locally on each device while media is end-to-end encrypted")
	}
	return ""
}
