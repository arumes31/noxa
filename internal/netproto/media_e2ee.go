package netproto

import "errors"

// Mandatory channel media end-to-end encryption (Task 2).
//
// A server that requires encrypted channel media advertises
// CapabilityMediaE2EE. Every WebRTCOffer of a channel voice session must then
// declare WebRTCOffer.MediaE2EE == MediaE2EEVersion: the client attaches the
// encoded-frame transform to every sender and receiver before negotiating
// and distributes per-slot epoch keys only as authenticated ratchet
// controls. The server never receives media keys. Old clients, keyless
// guests and servers without the device directory get an explicit error
// whose message starts with one of the MediaE2EEReason* codes.
const (
	CapabilityMediaE2EE = "noxa-media-e2ee-v1"
	MediaE2EEVersion    = CapabilityMediaE2EE
)

// Error message prefixes (the rest of the message is an English
// explanation for clients that do not know the code).
const (
	MediaE2EEReasonClientOutdated   = "media_e2ee_client_outdated"
	MediaE2EEReasonKeylessGuest     = "media_e2ee_keyless_guest"
	MediaE2EEReasonUnavailable      = "media_e2ee_unavailable"
	MediaE2EEReasonRecording        = "server_recording_disabled_e2ee"
	MediaE2EEReasonWhisper          = "whisper_unavailable_e2ee"
	MediaE2EEReasonPreview          = "stream_preview_disabled_e2ee"
	MediaE2EEReasonCodecUnsupported = "media_e2ee_codec_unsupported"
	// An invisible session is not in the other members' roster, so nobody
	// would give it keys and its own keys would reveal it: refused.
	MediaE2EEReasonInvisible = "media_e2ee_invisible"
	// The encrypted voice channel is at its supported size.
	MediaE2EEReasonChannelFull = "media_e2ee_channel_full"
)

// ErrMediaE2EEChannelFull refuses a join or move of encrypted voice into a
// channel that already has the supported number of members. It is not
// retryable until a member leaves; transports map it to their "resource
// exhausted" status and keep the reason code in the message.
var ErrMediaE2EEChannelFull = errors.New(MediaE2EEReasonChannelFull + ": this encrypted voice channel is full")
