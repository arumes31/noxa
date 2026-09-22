// Package netproto: codec.go defines the message type registry and a small
// set of message structs used by the control channel. Encoding is JSON for
// now (codegen is deferred); the framing layer is type-agnostic so the wire
// format can be swapped later without changing the transport.
package netproto

import (
	"encoding/json"
	"fmt"
	"time"

	"noxa/internal/authorization"
)

// MessageType is the numeric identifier carried in the Frame.Type field.
type MessageType uint16

// Control-channel message type constants. Keep these stable; clients and
// servers rely on them for dispatch.
const (
	MsgAuthenticate  MessageType = 1 // client -> server: authenticate request
	MsgAuthResponse  MessageType = 2 // server -> client: authentication result
	MsgChatSend      MessageType = 5 // client -> server: send a chat message
	MsgChatBroadcast MessageType = 6 // server -> client: broadcast a chat message
	MsgError         MessageType = 7 // server -> client: error report
	MsgPing          MessageType = 8 // client -> server: liveness probe
	MsgPong          MessageType = 9 // server -> client: liveness reply

	MsgJoinChannel   MessageType = 10 // client -> server: join a channel
	MsgMoveClient    MessageType = 12 // client -> server: move another client into a channel
	MsgKickClient    MessageType = 13 // client -> server: kick (or ban) a client
	MsgSnapshot      MessageType = 14 // server -> client: full channel-tree snapshot (payload is a broadcast.TreeSnapshot JSON document)
	MsgEvent         MessageType = 15 // server -> client: asynchronous event envelope {"type": ..., "data": ...}
	MsgAuthChallenge MessageType = 16 // server -> client: challenge for challenge-response auth
	MsgAuthSignature MessageType = 17 // client -> server: signed auth challenge

	MsgWebRTCOffer    MessageType = 18 // client -> server: SDP offer (server -> client: renegotiation offer)
	MsgWebRTCAnswer   MessageType = 19 // server -> client: SDP answer (client -> server: renegotiation answer)
	MsgICECandidate   MessageType = 20 // both directions: trickle ICE candidate
	MsgWhisperSet     MessageType = 21 // client -> server: configure whisper targets/mode
	MsgPositionUpdate MessageType = 22 // client -> server: 3D position publish

	MsgVideoQuality     MessageType = 23 // client -> server: request simulcast layer (high/mid/low)
	MsgRecordingControl MessageType = 24 // client -> server: start/stop channel recording

	MsgFileTransferInit         MessageType = 25 // client -> server: request a transfer token
	MsgFileTransferInitResponse MessageType = 26 // server -> client: issued transfer token
	MsgFileList                 MessageType = 27 // client -> server: list a channel's files
	MsgFileListResponse         MessageType = 28 // server -> client: channel file listing

	MsgAvatarSet      MessageType = 29 // client -> server: set own avatar (base64 image)
	MsgAvatarGet      MessageType = 30 // client -> server: request a user's avatar
	MsgAvatarData     MessageType = 31 // server -> client: avatar image data
	MsgChannelIconSet MessageType = 32 // client -> server: set a channel icon
	MsgComplaint      MessageType = 34 // client -> server: file a complaint
	MsgScreenShare    MessageType = 35 // client -> server: declare screen-share state

	MsgClientInfoQuery    MessageType = 38 // client -> server: request a client's connection info
	MsgClientInfoResponse MessageType = 39 // server -> client: client connection info

	MsgPrioritySpeaker MessageType = 41 // client -> server: toggle own priority-speaker flag

	MsgKeyPublish  MessageType = 42 // client -> server: publish own X25519 public key (E2EE directory)
	MsgKeyRequest  MessageType = 43 // client -> server: request a user's X25519 public key
	MsgKeyResponse MessageType = 44 // server -> client: a user's X25519 public key (empty = none)
	MsgChannelKey  MessageType = 45 // server -> client: sealed channel/global chat key (key rotation)

	MsgChatHistory         MessageType = 46 // client -> server: request paged chat history
	MsgChatHistoryResponse MessageType = 47 // server -> client: chat history page
	MsgChatEdit            MessageType = 48 // client -> server: edit own message
	MsgChatDelete          MessageType = 49 // client -> server: delete message (own or b_chat_delete_any)
	MsgChatPin             MessageType = 50 // client -> server: pin/unpin a message
	MsgChatPins            MessageType = 51 // client -> server: list channel pins
	MsgChatPinsResponse    MessageType = 52 // server -> client: channel pins
	MsgTyping              MessageType = 53 // both directions: typing indicator (relayed, not stored)
	MsgChatDelivered       MessageType = 54 // client -> server: DM delivery ack (relayed to sender)
	MsgChatRead            MessageType = 55 // client -> server: DM read receipt (relayed to sender)
	MsgEmojiUpload         MessageType = 56 // client -> server: upload custom emoji
	MsgEmojiList           MessageType = 57 // client -> server: list custom emojis
	MsgEmojiListResponse   MessageType = 58 // server -> client: custom emoji list
	MsgChatReact           MessageType = 59 // client -> server: toggle a reaction on a message
	MsgEmojiGet            MessageType = 60 // client -> server: fetch one custom emoji image
	MsgEmojiData           MessageType = 61 // server -> client: custom emoji image data
	MsgAuditLog            MessageType = 74 // client -> server: request audit log page
	MsgAuditLogResponse    MessageType = 75 // server -> client: audit log page
	MsgBanList             MessageType = 81 // client -> server: list bans
	MsgBanListResponse     MessageType = 82 // server -> client: ban list

	MsgFileDelete           MessageType = 86 // client -> server: delete a channel file
	MsgFileRename           MessageType = 87 // client -> server: rename/move a channel file
	MsgFileVersions         MessageType = 88 // client -> server: list a file's old versions
	MsgFileVersionsResponse MessageType = 89 // server -> client: version list
	MsgFileLink             MessageType = 90 // client -> server: create an expiring download link
	MsgFileLinkResponse     MessageType = 91 // server -> client: the link URL + expiry
	MsgServerIconSet        MessageType = 92 // client -> server: upload the server icon (admin)
	MsgServerIconGet        MessageType = 93 // client -> server: fetch the server icon
	MsgServerIconData       MessageType = 94 // server -> client: server icon payload

	MsgSetStatus          MessageType = 95 // client -> server: set own presence status
	MsgPoke               MessageType = 96 // client -> server: poke a client
	MsgServerInfoQuery    MessageType = 97 // client -> server: request public server info
	MsgServerInfoResponse MessageType = 98 // server -> client: public server info

	MsgChatKeyRequest MessageType = 99  // client -> server: request specific scope key generations
	MsgChatKeyBundle  MessageType = 100 // server -> client: sealed generations (+ refusals)

	MsgChatFilterGet      MessageType = 101 // client -> server: read the runtime chat moderation lists
	MsgChatFilterSet      MessageType = 102 // client -> server: replace the runtime chat moderation lists
	MsgChatFilterResponse MessageType = 103 // server -> client: the moderation lists in force
	MsgComplaintList      MessageType = 107 // client -> server: list complaints
	MsgComplaints         MessageType = 108 // server -> client: complaint list
	MsgComplaintClear     MessageType = 109 // client -> server: delete complaints against a target

	MsgChannelIconGet  MessageType = 114 // client -> server: fetch a channel icon
	MsgChannelIconData MessageType = 115 // server -> client: channel icon payload
	MsgServerBannerSet MessageType = 116 // client -> server: upload the server banner (admin)
	MsgServerBannerGet MessageType = 117 // client -> server: fetch the server banner
	MsgServerBannerDat MessageType = 118 // server -> client: server banner payload
	MsgEmojiDelete     MessageType = 119 // client -> server: delete a custom emoji
	MsgEmojiRename     MessageType = 120 // client -> server: rename a custom emoji

	MsgServerRules          MessageType = 121 // server -> client: rules text awaiting acceptance
	MsgServerRulesAccept    MessageType = 122 // client -> server: accept the rules by hash
	MsgChannelSubscribe     MessageType = 123 // client -> server: (un)subscribe to channels
	MsgSubscriptionState    MessageType = 124 // server -> client: the authoritative subscription set
	MsgServerConfigQuery    MessageType = 125 // admin -> server: get runtime voice/client settings
	MsgServerConfigSet      MessageType = 126 // admin -> server: update runtime voice/client settings
	MsgServerConfigResponse MessageType = 127 // server -> admin: effective settings
	MsgPreKeyPublish        MessageType = 128 // client -> server: publish X3DH bundle and one-time keys
	MsgPreKeyQuery          MessageType = 129 // client -> server: consume target's X3DH bundle
	MsgPreKeyBundle         MessageType = 130 // server -> client: signed bundle plus optional one-time key
)

// String returns a human-readable name for the message type.
func (m MessageType) String() string {
	switch m {
	case MsgChatMutationSaved:
		return "ChatMutationSaved"
	case MsgChatAccepted:
		return "ChatAccepted"
	case MsgPokeAccepted:
		return "PokeAccepted"
	case MsgClientMoved:
		return "ClientMoved"
	case MsgClientRemoved:
		return "ClientRemoved"
	case MsgChannelJoined:
		return "ChannelJoined"
	case MsgAssetMutationSaved:
		return "AssetMutationSaved"
	case MsgFileMutationSaved:
		return "FileMutationSaved"
	case MsgStatusSaved:
		return "StatusSaved"
	case MsgMediaControlSaved:
		return "MediaControlSaved"
	case MsgMediaLimitsChanged:
		return "MediaLimitsChanged"
	case MsgMediaLimitsSet:
		return "MediaLimitsSet"
	case MsgMediaLimitsSaved:
		return "MediaLimitsSaved"
	case MsgRoleBanRemove:
		return "RoleBanRemove"
	case MsgRoleBanRemoved:
		return "RoleBanRemoved"
	case MsgRoleChannelIconSet:
		return "RoleChannelIconSet"
	case MsgRoleChannelIconSaved:
		return "RoleChannelIconSaved"
	case MsgRoleChannelQuery:
		return "RoleChannelQuery"
	case MsgRoleChannelState:
		return "RoleChannelState"
	case MsgRoleChannelChange:
		return "RoleChannelChange"
	case MsgRoleChannelResult:
		return "RoleChannelResult"
	case MsgRoleQuery:
		return "RoleQuery"
	case MsgRoleState:
		return "RoleState"
	case MsgRoleChange:
		return "RoleChange"
	case MsgRoleChangeResult:
		return "RoleChangeResult"
	case MsgAccessCheck:
		return "AccessCheck"
	case MsgChannelAccessPreview:
		return "ChannelAccessPreview"
	case MsgChannelAccessImpact:
		return "ChannelAccessImpact"
	case MsgAccessCheckResult:
		return "AccessCheckResult"
	case MsgRoleMemberQuery:
		return "RoleMemberQuery"
	case MsgRoleMembers:
		return "RoleMembers"
	case MsgMemberVoiceSet:
		return "MemberVoiceSet"
	case MsgMemberVoiceState:
		return "MemberVoiceState"
	case MsgAuthenticate:
		return "Authenticate"
	case MsgAuthResponse:
		return "AuthResponse"
	case MsgChatSend:
		return "ChatSend"
	case MsgChatBroadcast:
		return "ChatBroadcast"
	case MsgError:
		return "Error"
	case MsgPing:
		return "Ping"
	case MsgPong:
		return "Pong"
	case MsgServerConfigQuery:
		return "ServerConfigQuery"
	case MsgServerConfigSet:
		return "ServerConfigSet"
	case MsgServerConfigResponse:
		return "ServerConfigResponse"
	case MsgPreKeyPublish:
		return "PreKeyPublish"
	case MsgPreKeyQuery:
		return "PreKeyQuery"
	case MsgPreKeyBundle:
		return "PreKeyBundle"
	case MsgJoinChannel:
		return "JoinChannel"
	case MsgMoveClient:
		return "MoveClient"
	case MsgKickClient:
		return "KickClient"
	case MsgSnapshot:
		return "Snapshot"
	case MsgEvent:
		return "Event"
	case MsgAuthChallenge:
		return "AuthChallenge"
	case MsgAuthSignature:
		return "AuthSignature"
	case MsgWebRTCOffer:
		return "WebRTCOffer"
	case MsgWebRTCAnswer:
		return "WebRTCAnswer"
	case MsgICECandidate:
		return "ICECandidate"
	case MsgWhisperSet:
		return "WhisperSet"
	case MsgPositionUpdate:
		return "PositionUpdate"
	case MsgVideoQuality:
		return "VideoQuality"
	case MsgRecordingControl:
		return "RecordingControl"
	case MsgFileTransferInit:
		return "FileTransferInit"
	case MsgFileTransferInitResponse:
		return "FileTransferInitResponse"
	case MsgFileList:
		return "FileList"
	case MsgFileListResponse:
		return "FileListResponse"
	case MsgAvatarSet:
		return "AvatarSet"
	case MsgAvatarGet:
		return "AvatarGet"
	case MsgAvatarData:
		return "AvatarData"
	case MsgChannelIconSet:
		return "ChannelIconSet"
	case MsgComplaint:
		return "Complaint"
	case MsgScreenShare:
		return "ScreenShare"
	case MsgClientInfoQuery:
		return "ClientInfoQuery"
	case MsgClientInfoResponse:
		return "ClientInfoResponse"
	case MsgPrioritySpeaker:
		return "PrioritySpeaker"
	case MsgKeyPublish:
		return "KeyPublish"
	case MsgKeyRequest:
		return "KeyRequest"
	case MsgKeyResponse:
		return "KeyResponse"
	case MsgChannelKey:
		return "ChannelKey"
	case MsgChatHistory:
		return "ChatHistory"
	case MsgChatHistoryResponse:
		return "ChatHistoryResponse"
	case MsgChatEdit:
		return "ChatEdit"
	case MsgChatDelete:
		return "ChatDelete"
	case MsgChatPin:
		return "ChatPin"
	case MsgChatPins:
		return "ChatPins"
	case MsgChatPinsResponse:
		return "ChatPinsResponse"
	case MsgTyping:
		return "Typing"
	case MsgChatDelivered:
		return "ChatDelivered"
	case MsgChatRead:
		return "ChatRead"
	case MsgEmojiUpload:
		return "EmojiUpload"
	case MsgEmojiList:
		return "EmojiList"
	case MsgEmojiListResponse:
		return "EmojiListResponse"
	case MsgChatReact:
		return "ChatReact"
	case MsgEmojiGet:
		return "EmojiGet"
	case MsgEmojiData:
		return "EmojiData"
	case MsgAuditLog:
		return "AuditLog"
	case MsgAuditLogResponse:
		return "AuditLogResponse"
	case MsgBanList:
		return "BanList"
	case MsgBanListResponse:
		return "BanListResponse"
	case MsgFileDelete:
		return "FileDelete"
	case MsgFileRename:
		return "FileRename"
	case MsgFileVersions:
		return "FileVersions"
	case MsgFileVersionsResponse:
		return "FileVersionsResponse"
	case MsgFileLink:
		return "FileLink"
	case MsgFileLinkResponse:
		return "FileLinkResponse"
	case MsgServerIconSet:
		return "ServerIconSet"
	case MsgServerIconGet:
		return "ServerIconGet"
	case MsgServerIconData:
		return "ServerIconData"
	case MsgSetStatus:
		return "SetStatus"
	case MsgPoke:
		return "Poke"
	case MsgServerInfoQuery:
		return "ServerInfoQuery"
	case MsgServerInfoResponse:
		return "ServerInfoResponse"
	case MsgChatKeyRequest:
		return "ChatKeyRequest"
	case MsgChatKeyBundle:
		return "ChatKeyBundle"
	case MsgChatFilterGet:
		return "ChatFilterGet"
	case MsgChatFilterSet:
		return "ChatFilterSet"
	case MsgChatFilterResponse:
		return "ChatFilterResponse"
	case MsgComplaintList:
		return "ComplaintList"
	case MsgComplaints:
		return "Complaints"
	case MsgComplaintClear:
		return "ComplaintClear"
	case MsgChannelIconGet:
		return "ChannelIconGet"
	case MsgChannelIconData:
		return "ChannelIconData"
	case MsgServerBannerSet:
		return "ServerBannerSet"
	case MsgServerBannerGet:
		return "ServerBannerGet"
	case MsgServerBannerDat:
		return "ServerBannerData"
	case MsgEmojiDelete:
		return "EmojiDelete"
	case MsgEmojiRename:
		return "EmojiRename"
	case MsgServerRules:
		return "ServerRules"
	case MsgServerRulesAccept:
		return "ServerRulesAccept"
	case MsgChannelSubscribe:
		return "ChannelSubscribe"
	case MsgSubscriptionState:
		return "SubscriptionState"
	default:
		return fmt.Sprintf("Unknown(%d)", uint16(m))
	}
}

// Authenticate is sent by a client to authenticate. Username carries the
// user's TS3-style unique ID — or, for password login, a nickname (the
// server tries unique ID first, then nickname). When Password is non-empty
// it is verified against the stored Argon2id hash; when Password is empty
// the server starts a challenge-response handshake and replies with an
// AuthChallenge. ServerPassword is required when the server has a global
// password set. PublicKey is the client's Ed25519 identity key (PEM); on a
// successful nickname login the server binds it to the account so future
// challenge logins work with the same key.
//
// Anonymous guest login (TS3-style): with Anonymous set and no Username or
// Password, the server authenticates the client immediately as a guest with
// an ephemeral guest: unique ID and the given Nickname. With Anonymous set
// and a client-derived Username (unique ID of the client's own Ed25519
// identity), the challenge handshake runs as usual; presenting the matching
// PublicKey in AuthSignature then authenticates the client as a guest with a
// stable, key-derived unique ID even when no users row exists.
type Authenticate struct {
	// AuthorizationModels lists the replacement policy models this client supports.
	AuthorizationModels []string `json:"authorization_models,omitempty"`
	Username            string   `json:"username"`
	Password            string   `json:"password,omitempty"`
	ServerPassword      string   `json:"server_password,omitempty"`
	Nickname            string   `json:"nickname,omitempty"`
	Anonymous           bool     `json:"anonymous,omitempty"`
	PublicKey           string   `json:"public_key,omitempty"`
	Token               string   `json:"token,omitempty"`
	// X25519PublicKey is the client's ENCRYPTION key (base64, 32 bytes) — the
	// same value MsgKeyPublish carries. PublicKey above is the Ed25519
	// identity key and cannot be sealed to, so this is supplied at auth time
	// so the server can seal the global scope key and the MOTD into
	// AuthResponse (133).
	X25519PublicKey string `json:"x25519_public_key,omitempty"`
}

// CapabilityGroupAssignAck advertises support for GroupAssign.AckRequested.
// #nosec G101 -- this is a protocol capability identifier, not a credential.
const CapabilityGroupAssignAck = "group_assign_ack"

// AuthorizationModelRolesV1 identifies the role and channel-override contract.
const AuthorizationModelRolesV1 = "roles-v1"

// AuthResponse is the server's reply to an Authenticate message.
type AuthResponse struct {
	// AuthorizationModel is the selected model, or the required model on rejection.
	// Omission identifies a legacy server.
	AuthorizationModel string `json:"authorization_model,omitempty"`
	// Capabilities advertises optional protocol features. Missing means legacy.
	Capabilities []string     `json:"capabilities,omitempty"`
	MediaLimits  *MediaLimits `json:"media_limits,omitempty"`
	// Zero/absent is the startup/legacy baseline; subsequent updates are positive.
	MediaLimitsRevision uint64 `json:"media_limits_revision,string,omitempty"`

	OK       bool   `json:"ok"`
	ClientID string `json:"client_id,omitempty"`
	UniqueID string `json:"unique_id,omitempty"`
	Nickname string `json:"nickname,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// ICEServers carries the ICE servers the client should use for WebRTC
	// (STUN defaults plus TURN entries with time-limited credentials when the
	// server has TURN configured). Empty means "client defaults".
	ICEServers []ICEServer `json:"ice_servers,omitempty"`
	// TLSFingerprint is the SHA-256 fingerprint of the server's control
	// channel certificate (empty when TLS is disabled). Clients use it for
	// TOFU verification and display.
	TLSFingerprint string `json:"tls_fingerprint,omitempty"`
	// MOTD is the server's message of the day (empty when unset). When
	// MOTDEnc is set it is SEALED under the global scope generation
	// MOTDKeyID, and the client opens it with a key from ChatKeys below.
	MOTD      string `json:"motd,omitempty"`
	MOTDEnc   bool   `json:"motd_enc,omitempty"`
	MOTDKeyID uint32 `json:"motd_key_id,omitempty"`
	// ChatKeys carries the global scope generation (and nothing else) sealed
	// to Authenticate.X25519PublicKey, so the client can open MOTD before
	// Connect() returns — no new frame, no ordering rule, no race. Channel
	// keys still arrive via MsgChannelKey after key publish.
	ChatKeys []ChannelKey `json:"chat_keys,omitempty"`
}

// ICEServer describes one ICE server for a WebRTC RTCPeerConnection,
// mirroring the browser's RTCIceServer dictionary.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// AuthChallenge is the server's challenge in the challenge-response (Ed25519)
// authentication handshake. The client must sign Challenge with its identity
// private key and reply with an AuthSignature.
type AuthChallenge struct {
	Challenge []byte `json:"challenge"`
}

// AuthSignature is the client's reply to an AuthChallenge, completing the
// challenge-response handshake. PublicKey (PEM) is optional: registered
// users omit it (the server uses the stored key). Guests present it so the
// server can verify the signature and derive their key-based unique ID.
type AuthSignature struct {
	UniqueID  string `json:"unique_id"`
	PublicKey string `json:"public_key,omitempty"`
	Signature []byte `json:"signature"`
	// X25519PublicKey mirrors Authenticate.X25519PublicKey. The guest/challenge
	// path leaves Authenticate.PublicKey empty and supplies its identity key
	// here, so the encryption key has to be carried here too (133).
	X25519PublicKey string `json:"x25519_public_key,omitempty"`
}

// PrioritySpeaker toggles the calling client's priority-speaker flag
// (TS3-style channel commander). Gated by b_client_priority_speaker.
type PrioritySpeaker struct {
	Active       bool `json:"active"`
	AckRequested bool `json:"ack_requested,omitempty"`
}

// ChatSend is a chat message from a client to the server. ChannelID set means
// channel chat; ToUniqueID set means a direct message to a user (spooled when
// offline); ToClientID set means a direct message to an online connection;
// none set means a global (server-wide) message.
//
// Encryption (wave 4b): when Enc is true, Text is base64 ciphertext —
// nacl/box (nonce-prepended) for direct messages (KeyID 0), secretbox for
// channel/global (KeyID identifies the scope key). The server validates the
// KeyID against its current scope key but cannot read the body.
type ChatSend struct {
	AckRequested bool   `json:"ack_requested,omitempty"`
	ChannelID    string `json:"channel_id,omitempty"`
	ToClientID   string `json:"to_client_id,omitempty"`
	ToUniqueID   string `json:"to_unique_id,omitempty"`
	Text         string `json:"text"`
	Enc          bool   `json:"enc,omitempty"`
	KeyID        uint32 `json:"key_id,omitempty"`
	// ReplyToID references a stored message in the same channel/global scope.
	// It is zero for a normal message and is never used for direct messages.
	ReplyToID int64 `json:"reply_to_id,omitempty"`
	// ClientMsgID is a client-generated reference echoed in the broadcast so
	// DM delivery/read receipts (wave 5a) can reference the message without
	// a database id (DMs are true E2EE and not stored server-side).
	ClientMsgID string `json:"client_msg_id,omitempty"`
}

// ChatBroadcast is a chat message the server fans out to interested clients.
// Offline marks messages delivered from the offline-message spool after login.
// FromUniqueID identifies the sender for E2EE direct messages (the recipient
// fetches the sender's public key from the directory). Enc/KeyID mirror
// ChatSend; E2E marks true end-to-end (box) messages for UI display.
type ChatBroadcast struct {
	ChannelID string `json:"channel_id,omitempty"`
	// Direct and ToUniqueID preserve routing independently of crypto flags,
	// including the recipient needed to decrypt and place the sender's echo.
	Direct     bool   `json:"direct,omitempty"`
	ToUniqueID string `json:"to_unique_id,omitempty"`
	// EncVerified is set only by the receiving native client after opening.
	EncVerified  bool   `json:"enc_verified,omitempty"`
	FromClientID string `json:"from_client_id,omitempty"`
	FromUniqueID string `json:"from_unique_id,omitempty"`
	From         string `json:"from"`
	Text         string `json:"text"`
	Offline      bool   `json:"offline,omitempty"`
	Enc          bool   `json:"enc,omitempty"`
	KeyID        uint32 `json:"key_id,omitempty"`
	E2E          bool   `json:"e2e,omitempty"`
	// ID is the server-side message id (channel/global history, wave 5a;
	// 0 for DMs). Mentions lists mentioned users' unique IDs. ClientMsgID
	// echoes the sender's reference for receipts.
	ID          int64    `json:"id,omitempty"`
	ReplyToID   int64    `json:"reply_to_id,omitempty"`
	Version     uint64   `json:"version,omitempty"`
	Mentions    []string `json:"mentions,omitempty"`
	ClientMsgID string   `json:"client_msg_id,omitempty"`
}

// Error carries a server-side error to the client.
type Error struct {
	Code    uint16 `json:"code"`
	Message string `json:"message"`
	// OriginType optionally identifies the client request frame that caused
	// this error. It is additive: older peers omit it and still decode, while
	// newer clients can distinguish a command failure from an unrelated
	// fire-and-forget server error.
	OriginType uint16 `json:"origin_type,omitempty"`
}

// Ping is a liveness probe. Payload is ignored.
type Ping struct{}

// Pong is the reply to a Ping.
type Pong struct{}

// JoinChannel requests that the calling client joins (moves into) a channel.
type JoinChannel struct {
	AckRequested bool   `json:"ack_requested,omitempty"`
	ChannelID    int64  `json:"channel_id"`
	Password     string `json:"password,omitempty"`
}

// MoveClient requests moving another client into a channel.
type MoveClient struct {
	AckRequested bool   `json:"ack_requested,omitempty"`
	ClientID     string `json:"client_id"`
	ChannelID    int64  `json:"channel_id"`
}

// KickClient requests kicking a client from its channel or from the server.
// Ban additionally records a unique-ID ban before removing the client; a ban
// always implies FromServer.
type KickClient struct {
	AckRequested      bool   `json:"ack_requested,omitempty"`
	ExpectedChannelID int64  `json:"expected_channel_id,omitempty"`
	ClientID          string `json:"client_id"`
	FromServer        bool   `json:"from_server,omitempty"`
	Ban               bool   `json:"ban,omitempty"`
	Reason            string `json:"reason,omitempty"`
	// DurationSeconds > 0 makes the ban temporary (0 = permanent). Only
	// meaningful with Ban.
	DurationSeconds int64 `json:"duration_seconds,omitempty"`
}

// WebRTCOffer carries an SDP offer. From a client it starts a WebRTC session;
// from the server it requests renegotiation.
type WebRTCOffer struct {
	SDP string `json:"sdp"`
	// Tracks labels the offer's outbound tracks with the media slot each one
	// occupies. It replaces the previous declaration on every offer; an
	// omitted track takes its kind's default slot, so a client that never
	// sends this field publishes exactly one microphone and one video track
	// (70). Server -> client renegotiation offers never set it.
	Tracks []TrackSlot `json:"tracks,omitempty"`
}

// TrackSlot names the media slot one outbound track of a WebRTC offer
// occupies, so the server can route a publisher's SECOND audio or video track
// separately instead of muxing it into the first (70).
//
// TrackID is the MediaStreamTrack.id as it appears in the offer's a=msid
// line. Slot is one of "mic" (default audio: microphone), "cam" (default
// video: camera or primary shared surface), "screenaudio" (system audio
// captured with a screen share), or "screen" (a second shared surface).
// A client publishing more than one track of a kind MUST declare them: an
// unknown or missing slot puts the track in its kind's default slot, and a
// slot carries exactly one source, so whichever track arrives first wins and
// the other is dropped rather than muxed into it.
type TrackSlot struct {
	TrackID string `json:"track_id"`
	Slot    string `json:"slot"`
}

// WebRTCAnswer carries an SDP answer to a previously received offer.
type WebRTCAnswer struct {
	SDP string `json:"sdp"`
}

// ICECandidate carries a single trickle ICE candidate in either direction.
type ICECandidate struct {
	Candidate     string `json:"candidate"`
	SDPMid        string `json:"sdp_mid,omitempty"`
	SDPMLineIndex uint16 `json:"sdp_mline_index,omitempty"`
}

// WhisperSet configures the calling client's whisper list and mode. UniqueIDs
// are target users (resolved to online connections at set time); ChannelIDs
// are target channels whose members receive the audio. While Active is true,
// the client's outgoing audio is routed to the whisper targets instead of
// their channel.
type WhisperSet struct {
	AckRequested bool     `json:"ack_requested,omitempty"`
	UniqueIDs    []string `json:"unique_ids,omitempty"`
	ChannelIDs   []int64  `json:"channel_ids,omitempty"`
	Active       bool     `json:"active"`
}

// PositionUpdate publishes the client's 3D position for positional audio. It
// is relayed to the other members of the client's channel as a position event.
type PositionUpdate struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// VideoQuality requests a simulcast layer for the video the client receives.
// Quality is "high", "mid", or "low" (mapped to RID f/h/q server-side, with
// fallback to the closest published layer).
type VideoQuality struct {
	Quality      string `json:"quality"`
	AckRequested bool   `json:"ack_requested,omitempty"`
}

// RecordingControl starts or stops a server-side recording of a channel.
// Action is "start" or "stop".
type RecordingControl struct {
	ChannelID int64  `json:"channel_id"`
	Action    string `json:"action"`
}

// FileTransferInit requests a transfer token for a file upload or download.
// Direction is "upload" or "download"; Size is the declared file size in
// bytes (uploads only; the server enforces its per-transfer cap).
type FileTransferInit struct {
	ChannelID int64  `json:"channel_id"`
	Direction string `json:"direction"`
	Name      string `json:"name"`
	Size      int64  `json:"size,omitempty"`
	// Folder is the virtual folder within the channel ('' = root, wave 7).
	Folder string `json:"folder,omitempty"`
}

// FileTransferInitResponse carries an issued single-use transfer token and
// the port of the file-transfer server the client should connect to.
type FileTransferInitResponse struct {
	TransferID string `json:"transfer_id"`
	Token      string `json:"token"`
	Port       int    `json:"port"`
	// TLS reports that the file-transfer port requires a TLS handshake, and
	// TLSFingerprint is the SHA-256 of its certificate — the SAME certificate
	// as the control channel, so the client re-uses the pin it already holds.
	// Without these an un-upgraded client hangs in a handshake instead of
	// failing legibly.
	TLS            bool   `json:"tls,omitempty"`
	TLSFingerprint string `json:"tls_fingerprint,omitempty"`
}

// FileList requests the file listing of a channel. Folder selects a virtual
// folder ("" = root, wave 7); Path is the legacy alias ("" or "/" only).
type FileList struct {
	ChannelID int64  `json:"channel_id"`
	Path      string `json:"path,omitempty"`
	Folder    string `json:"folder,omitempty"`
}

// FileEntry describes one file in a FileListResponse.
type FileEntry struct {
	Name       string    `json:"name"`
	Folder     string    `json:"folder,omitempty"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256"`
	Uploader   string    `json:"uploader,omitempty"`
	UploadedAt time.Time `json:"uploaded_at"`
	// Encrypted marks a client-sealed chat attachment (91-135). The browser
	// infers it from the ".vcx" suffix today, which only works because the
	// client also chooses the name — this makes it explicit.
	Encrypted bool `json:"encrypted,omitempty"`
}

// FileListResponse carries a channel's file listing plus the quota state
// (265): UsedBytes counts all files in the channel; QuotaBytes 0 = unlimited.
type FileListResponse struct {
	Entries    []FileEntry `json:"entries"`
	Folders    []string    `json:"folders,omitempty"`
	UsedBytes  int64       `json:"used_bytes"`
	QuotaBytes int64       `json:"quota_bytes"`
}

// FileDelete deletes one channel file (263). Allowed for the uploader and
// holders of b_ft_delete (admins bypass).
type FileDelete struct {
	AckRequested bool   `json:"ack_requested,omitempty"`
	ChannelID    int64  `json:"channel_id"`
	Folder       string `json:"folder,omitempty"`
	Name         string `json:"name"`
}

// FileRename renames or moves a channel file (262); same gate as FileDelete.
type FileRename struct {
	AckRequested bool   `json:"ack_requested,omitempty"`
	ChannelID    int64  `json:"channel_id"`
	Folder       string `json:"folder,omitempty"`
	Name         string `json:"name"`
	NewName      string `json:"new_name"`
	NewFolder    string `json:"new_folder,omitempty"`
	// NewChannelID moves the file to another channel (262). 0 keeps it where
	// it is; a move is permission-checked against BOTH channels.
	NewChannelID int64 `json:"new_channel_id,omitempty"`
}

// ServerRules carries the operator's rules text awaiting acceptance (215).
// Hash identifies the exact text: editing the rules changes it, which re-asks
// everyone and makes a stale acceptance refusable.
type ServerRules struct {
	Text string `json:"text"`
	Hash string `json:"hash"`
}

// ServerRulesAccept records that the caller accepted the rules they were
// shown (215). A hash that is no longer current is refused, so the client
// re-displays rather than silently accepting text the user never read.
type ServerRulesAccept struct {
	Hash string `json:"hash"`
}

// ChannelSubscribe (un)subscribes the caller from channels it is not in
// (312), so their chat and presence still arrive. Gated by the subscriber's
// i_channel_subscribe_power against each target's
// i_channel_needed_subscribe_power.
type ChannelSubscribe struct {
	ChannelIDs []int64 `json:"channel_ids"`
	Subscribe  bool    `json:"subscribe"`
}

// SubscriptionState is the AUTHORITATIVE set after any change (312). The
// server always sends the whole set rather than a delta, so a client cannot
// accumulate drift against it. The caller's own channel is always included
// and cannot be unsubscribed.
type SubscriptionState struct {
	ChannelIDs []int64 `json:"channel_ids"`
}

// ChannelIconGet fetches a channel's icon (271). Without it the uploaded
// image was write-only: nothing could ever read one back.
type ChannelIconGet struct {
	ChannelID int64 `json:"channel_id"`
}

// ChannelIconData carries a channel icon ("" data = no icon set).
type ChannelIconData struct {
	ChannelID   int64  `json:"channel_id"`
	DataBase64  string `json:"data_base64"`
	ContentType string `json:"content_type,omitempty"`
}

// ServerBannerSet uploads the server banner (270, admin only).
type ServerBannerSet struct {
	AckRequested bool   `json:"ack_requested,omitempty"`
	DataBase64   string `json:"data_base64"`
}

// ServerBannerGet requests the server banner.
type ServerBannerGet struct{}

// ServerBannerData carries the server banner ("" data = none set).
type ServerBannerData struct {
	DataBase64  string `json:"data_base64"`
	ContentType string `json:"content_type,omitempty"`
}

// EmojiDelete removes a custom server emoji (272). Gated like upload.
type EmojiDelete struct {
	AckRequested bool   `json:"ack_requested,omitempty"`
	Name         string `json:"name"`
}

// EmojiRename renames a custom server emoji (272). Messages already sent
// keep the old shortcode, so a rename does not rewrite history.
type EmojiRename struct {
	AckRequested bool   `json:"ack_requested,omitempty"`
	Name         string `json:"name"`
	NewName      string `json:"new_name"`
}

// FileVersions lists the rotated old versions of a file (264).
type FileVersions struct {
	ChannelID int64  `json:"channel_id"`
	Folder    string `json:"folder,omitempty"`
	Name      string `json:"name"`
}

// FileVersionsResponse carries the version list (newest first).
type FileVersionsResponse struct {
	Entries []FileEntry `json:"entries"`
}

// FileLink requests an expiring download link for a file (267). Gated by
// admin or uploader.
type FileLink struct {
	ChannelID int64  `json:"channel_id"`
	Folder    string `json:"folder,omitempty"`
	Name      string `json:"name"`
}

// FileLinkResponse carries the link path, scheme, and expiry. The client
// builds the full URL from its own control host plus HealthPort (the server
// cannot know its published address behind Docker/NAT). Scheme is optional
// for compatibility with old servers and must never be inferred from the
// TLS control connection.
type FileLinkResponse struct {
	// SessionBound links also expire when the issuing session ends or loses access.
	SessionBound bool   `json:"session_bound,omitempty"`
	Path         string `json:"path"`
	Scheme       string `json:"scheme,omitempty"`
	HealthPort   int    `json:"health_port"`
	ExpiresAt    int64  `json:"expires_at"`
}

// ServerIconSet uploads the server icon (admin only; same validation as
// avatars).
type ServerIconSet struct {
	AckRequested bool   `json:"ack_requested,omitempty"`
	DataBase64   string `json:"data_base64"`
}

// ServerIconGet fetches the server icon.
type ServerIconGet struct{}

// ServerIconData carries the server icon (empty = none set).
type ServerIconData struct {
	DataBase64  string `json:"data_base64,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// SetStatus sets the caller's presence (307-309): Status is "online",
// "away", or "busy"; Message is a free-form status line ("" clears).
type SetStatus struct {
	AckRequested bool   `json:"ack_requested,omitempty"`
	Status       string `json:"status"`
	Message      string `json:"message,omitempty"`
}

// Poke pokes a client (321/322): a short attention message relayed as a
// "poke" event. Gated by b_client_poke (or poke power) with a per-target
// cooldown.
type Poke struct {
	AckRequested bool   `json:"ack_requested,omitempty"`
	ClientID     string `json:"client_id"`
	Message      string `json:"message,omitempty"`
}

// ServerInfoQuery requests the server's public information (313).
type ServerInfoQuery struct{}

// ServerInfoResponse carries the server's public information.
type ServerInfoResponse struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	Platform       string `json:"platform,omitempty"` // Optional for compatibility with older servers.
	UptimeSeconds  int64  `json:"uptime_seconds"`
	ClientsOnline  int    `json:"clients_online"`
	ChannelsOnline int    `json:"channels_online"`
	MaxClients     int    `json:"max_clients"`
	MOTD           string `json:"motd,omitempty"`
}

type ServerConfigQuery struct{}

type ServerConfig struct {
	MaxClients           int  `json:"max_clients"`
	ClientTimeoutSeconds int  `json:"client_timeout_seconds"`
	OpusBitrate          int  `json:"opus_bitrate"`
	OpusFEC              bool `json:"opus_fec"`
	OpusDTX              bool `json:"opus_dtx"`
	OpusStereo           bool `json:"opus_stereo"`
	// Response capability; ignored when clients submit the six settings.
	MediaLimitsManagement bool `json:"media_limits_management,omitempty"`
}

// ValidLimits reports whether this complete configuration fits runtime limits.
func (c ServerConfig) ValidLimits() bool {
	return c.MaxClients >= 0 && c.MaxClients <= 100_000 &&
		c.ClientTimeoutSeconds >= 30 && c.ClientTimeoutSeconds <= 86_400 &&
		c.OpusBitrate >= 6_000 && c.OpusBitrate <= 510_000
}

type OneTimePreKey struct {
	KeyID     uint32 `json:"key_id"`
	PublicKey []byte `json:"public_key"`
}

type PreKeyPublish struct {
	IdentityDH     []byte          `json:"identity_dh"`
	SigningPublic  []byte          `json:"signing_public"`
	SignedPreKeyID uint32          `json:"signed_prekey_id"`
	SignedPreKey   []byte          `json:"signed_prekey"`
	Signature      []byte          `json:"signature"`
	OneTimePreKeys []OneTimePreKey `json:"one_time_prekeys,omitempty"`
}

type PreKeyQuery struct {
	UniqueID string `json:"unique_id"`
}

type PreKeyBundle struct {
	UniqueID       string `json:"unique_id"`
	IdentityDH     []byte `json:"identity_dh"`
	SigningPublic  []byte `json:"signing_public"`
	SignedPreKeyID uint32 `json:"signed_prekey_id"`
	SignedPreKey   []byte `json:"signed_prekey"`
	Signature      []byte `json:"signature"`
	OneTimeKeyID   uint32 `json:"one_time_key_id,omitempty"`
	OneTimePreKey  []byte `json:"one_time_prekey,omitempty"`
}

// AvatarSet uploads the client's avatar image (base64). Accepted image
// types: PNG, JPEG, GIF, WebP; max 256 KiB after decoding.
type AvatarSet struct {
	AckRequested bool   `json:"ack_requested,omitempty"`
	DataBase64   string `json:"data_base64"`
}

// AvatarGet requests another user's avatar.
type AvatarGet struct {
	UniqueID string `json:"unique_id"`
}

// AvatarData is the server's reply to AvatarGet.
type AvatarData struct {
	UniqueID    string `json:"unique_id"`
	DataBase64  string `json:"data_base64"`
	ContentType string `json:"content_type"`
}

// ChannelIconSet uploads a channel icon (same validation as avatars).
type ChannelIconSet struct {
	ChannelID  int64  `json:"channel_id"`
	DataBase64 string `json:"data_base64"`
	// CopyFromChannelID, when non-zero (and DataBase64 is empty), reuses the
	// icon already stored for another channel (271 icon library).
	CopyFromChannelID int64 `json:"copy_from_channel_id,omitempty"`
}

// Complaint files a complaint against a user.
type Complaint struct {
	TargetUniqueID string `json:"target_unique_id"`
	Reason         string `json:"reason"`
}

// ScreenShare declares whether the client's video track is a screen share
// (relayed to channel members as a screenshare_changed event).
type ScreenShare struct {
	AckRequested bool `json:"ack_requested,omitempty"`
	Active       bool `json:"active"`
	MaxHeight    int  `json:"max_height,omitempty"`
}

// KeyPublish publishes the client's X25519 public key (base64, 32 bytes) to
// the server directory. Registered users persist it; guests keep it in
// server memory for their session.
type KeyPublish struct {
	PublicKey string `json:"public_key"`
}

// KeyRequest requests a user's X25519 public key by unique ID.
type KeyRequest struct {
	UniqueID string `json:"unique_id"`
}

// KeyResponse is the server's reply to KeyRequest. PublicKey is empty when
// the user has not published a key (old client).
type KeyResponse struct {
	UniqueID  string `json:"unique_id"`
	PublicKey string `json:"public_key,omitempty"`
}

// ChannelKey delivers a scope chat key to a member, sealed with nacl/box
// (SealAnonymous) to the member's X25519 public key. ChannelID 0 is the
// global (server-wide) scope. KeyID identifies the key generation — it bumps
// on rotation (member left), and members must use the latest KeyID when
// sending.
type ChannelKey struct {
	ChannelID int64  `json:"channel_id"`
	KeyID     uint32 `json:"key_id"`
	SealedKey string `json:"sealed_key"`
}

// --- Chat infrastructure (wave 5a) ------------------------------------------

// ChatHistory requests a page of channel/global history. ChannelID 0 is the
// global scope. BeforeID pages backwards (0 = latest).
type ChatHistory struct {
	ChannelID int64 `json:"channel_id"`
	BeforeID  int64 `json:"before_id,omitempty"`
	Limit     int   `json:"limit,omitempty"`
}

// ChatHistoryEntry is one stored message in a history page. Reactions maps
// emoji -> count.
type ChatHistoryEntry struct {
	ID           int64  `json:"id"`
	FromUniqueID string `json:"from_unique_id"`
	FromNickname string `json:"from_nickname"`
	ReplyToID    int64  `json:"reply_to_id,omitempty"`
	Version      uint64 `json:"version"`

	// BodyEnc is the stored ciphertext:
	// base64(nonce[24] || secretbox(plain, scopeKey[KeyID])).
	// The SERVER populates ONLY BodyEnc/KeyID and NEVER Body — including when
	// chat_allow_plaintext is set. There is no server-controlled switch that
	// makes history plaintext on the wire. Both are empty for tombstoned
	// (deleted) messages.
	BodyEnc string `json:"body_enc,omitempty"`
	KeyID   uint32 `json:"key_id,omitempty"`

	// Body is the DECRYPTED text. It is ALWAYS empty on the wire. The Wails
	// Go layer fills it after unsealing, before the webview sees the entry. A
	// non-empty Body arriving from a server is a protocol violation and the
	// client replaces the entry with a refusal string.
	Body string `json:"body,omitempty"`

	// EncVerified is set by the CLIENT after it successfully opens BodyEnc.
	// It never appears on the wire from a server. It exists so the renderer
	// can draw the shield on a message replayed from history — without it a
	// history message is visually indistinguishable from one the server
	// handed over in the clear.
	EncVerified bool `json:"enc_verified,omitempty"`

	SentAt    int64          `json:"sent_at"` // unix seconds
	EditedAt  int64          `json:"edited_at,omitempty"`
	Deleted   bool           `json:"deleted,omitempty"`
	Reactions map[string]int `json:"reactions,omitempty"`
}

// ChatHistoryResponse carries one history page (newest first).
type ChatHistoryResponse struct {
	ChannelID int64              `json:"channel_id"`
	Messages  []ChatHistoryEntry `json:"messages"`
	// Keys carries exactly the distinct generations this page references,
	// sealed for the caller, so a page costs no extra round trips. Refused
	// and Truncated mean what they mean in ChatKeyBundle and MUST NOT be
	// conflated.
	Keys      []ChannelKey `json:"keys,omitempty"`
	Refused   []uint32     `json:"refused,omitempty"`
	Truncated bool         `json:"truncated,omitempty"`
}

// ChatEdit edits the caller's own message. NewText is encrypted like a
// normal message (the server decrypts before storing).
type ChatEdit struct {
	AckRequested    bool   `json:"ack_requested,omitempty"`
	MessageID       int64  `json:"message_id"`
	NewText         string `json:"new_text"`
	Enc             bool   `json:"enc,omitempty"`
	KeyID           uint32 `json:"key_id,omitempty"`
	ExpectedVersion uint64 `json:"expected_version,omitempty"`
}

// ChatDelete deletes a message (own, or any with b_chat_delete_any).
type ChatDelete struct {
	AckRequested bool  `json:"ack_requested,omitempty"`
	MessageID    int64 `json:"message_id"`
}

// ChatPin pins or unpins a message in a channel.
type ChatPin struct {
	AckRequested bool  `json:"ack_requested,omitempty"`
	ChannelID    int64 `json:"channel_id"`
	MessageID    int64 `json:"message_id"`
	Pinned       bool  `json:"pinned"`
}

// ChatPins requests a channel's pins.
type ChatPins struct {
	ChannelID int64 `json:"channel_id"`
}

// ChatPinEntry describes one pinned message.
type ChatPinEntry struct {
	MessageID int64             `json:"message_id"`
	PinnedBy  string            `json:"pinned_by"`
	PinnedAt  int64             `json:"pinned_at"`
	Message   *ChatHistoryEntry `json:"message,omitempty"`
}

// ChatPinsResponse carries a channel's pins. ChatPinEntry embeds
// *ChatHistoryEntry, so pinned bodies are ciphertext on the wire for the same
// reason history bodies are; Keys/Refused/Truncated behave identically.
type ChatPinsResponse struct {
	ChannelID int64          `json:"channel_id"`
	Pins      []ChatPinEntry `json:"pins"`
	Keys      []ChannelKey   `json:"keys,omitempty"`
	Refused   []uint32       `json:"refused,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
}

// ChatKeyRequest asks for specific scope chat key generations the client is
// missing: a live broadcast or chat_edited event referencing a generation it
// never received. History and pins do NOT use this — they carry their keys
// inline (ChatHistoryResponse.Keys), because a nested request/response would
// serialise behind the client's process-global request mutex. KeyIDs is
// capped at 64 per request; an empty list is rejected.
type ChatKeyRequest struct {
	ChannelID int64    `json:"channel_id"`
	KeyIDs    []uint32 `json:"key_ids"`
}

// ChatKeyBundle answers a ChatKeyRequest. Keys holds the generations the
// caller is entitled to, each sealed with box.SealAnonymous to the caller's
// published X25519 key — the same envelope as ChannelKey.
//
// The three outcomes are distinct and MUST NOT be conflated by clients:
//   - present in Keys      -> usable
//   - present in Refused   -> permanently withheld (not a member, or unknown
//     generation). Render "you do not have access".
//   - absent from both, with Truncated set -> the server capped the response.
//     Render "key unavailable" and re-request. Treating truncation as refusal
//     turns a transient cap into a permanent "[missing key]".
type ChatKeyBundle struct {
	ChannelID int64        `json:"channel_id"`
	Keys      []ChannelKey `json:"keys"`
	Refused   []uint32     `json:"refused,omitempty"`
	Truncated bool         `json:"truncated,omitempty"`
}

// ChatFilterGet requests the chat moderation lists currently in force
// (117/118). Gated by b_chat_filter_manage; admins bypass.
type ChatFilterGet struct{}

// ChatFilterSet replaces the runtime chat moderation lists. A nil field is
// left unchanged; a non-nil pointer to "" clears that list. Every set writes
// all three lists, so the first one snapshots the config.yaml defaults into
// the database and config stops being consulted (117/118).
//
// Each list is comma-separated. WordFilter entries are case-insensitive
// SUBSTRINGS of the message; the link lists are HOSTS matched against the
// hostname of each http(s) URL in the message (exact or subdomain).
type ChatFilterSet struct {
	WordFilter    *string `json:"word_filter,omitempty"`
	LinkBlacklist *string `json:"link_blacklist,omitempty"`
	LinkWhitelist *string `json:"link_whitelist,omitempty"`
}

// ChatFilterResponse carries the moderation lists in force. It answers both
// ChatFilterGet and a successful ChatFilterSet.
type ChatFilterResponse struct {
	WordFilter    string `json:"word_filter"`
	LinkBlacklist string `json:"link_blacklist"`
	LinkWhitelist string `json:"link_whitelist"`
	// FromConfig reports that no runtime override is stored yet, so these are
	// the config.yaml values. It is false for every response to a set.
	FromConfig bool `json:"from_config,omitempty"`
}

// ComplaintList requests the complaint list (173). Gated by b_complain_list.
type ComplaintList struct{}

// ComplaintEntry is one filed complaint.
type ComplaintEntry struct {
	TargetUniqueID string `json:"target_unique_id"`
	TargetNickname string `json:"target_nickname,omitempty"`
	FromUniqueID   string `json:"from_unique_id"`
	FromNickname   string `json:"from_nickname,omitempty"`
	Reason         string `json:"reason"`
	CreatedAt      int64  `json:"created_at"` // unix seconds
}

// Complaints is the complaint list.
type Complaints struct {
	Entries []ComplaintEntry `json:"entries"`
}

// ComplaintClear deletes complaints against a target (173). FromUniqueID
// empty clears every complaint against the target.
type ComplaintClear struct {
	TargetUniqueID string `json:"target_unique_id"`
	FromUniqueID   string `json:"from_unique_id,omitempty"`
}

// Typing is a typing indicator. Exactly one of ChannelID / ToUniqueID is
// set (channel scope or DM); neither set means global. Relayed, not stored —
// there is no body, so it never enters the ciphertext-at-rest path (91).
//
// The relay emits a "typing" event whose data shape is fixed by the server
// (see typingEvent): client_id, unique_id, nickname, channel_id. A channel
// or global relay reaches the SENDER too, so receivers must drop their own
// unique_id. A ToUniqueID naming an offline user, or a ChannelID the sender
// is not currently in, is dropped without an error frame.
type Typing struct {
	ChannelID  int64  `json:"channel_id,omitempty"`
	ToUniqueID string `json:"to_unique_id,omitempty"`
}

// ChatDelivered is the recipient's ack that a DM arrived (client-side ref).
type ChatDelivered struct {
	ToUniqueID  string `json:"to_unique_id"` // the DM sender to notify
	ClientMsgID string `json:"client_msg_id"`
}

// ChatRead is the recipient's read receipt for a DM.
type ChatRead struct {
	ToUniqueID  string `json:"to_unique_id"` // the DM sender to notify
	ClientMsgID string `json:"client_msg_id"`
}

// EmojiUpload uploads a custom emoji image (png/gif/webp, max 256 KiB after
// decoding). Gated by b_emoji_manage.
type EmojiUpload struct {
	AckRequested bool   `json:"ack_requested,omitempty"`
	Name         string `json:"name"`
	DataBase64   string `json:"data_base64"`
}

// EmojiList requests the custom emoji list.
type EmojiList struct{}

// EmojiEntry describes one custom emoji.
type EmojiEntry struct {
	Name     string `json:"name"`
	FileName string `json:"file_name"`
}

// EmojiListResponse carries the custom emoji list.
type EmojiListResponse struct {
	Emojis []EmojiEntry `json:"emojis"`
}

// ChatReact toggles a reaction on a message (add if absent, remove if
// present; one reaction per emoji per user).
type ChatReact struct {
	AckRequested bool   `json:"ack_requested,omitempty"`
	MessageID    int64  `json:"message_id"`
	Emoji        string `json:"emoji"`
}

// EmojiGet requests a custom emoji's image data by name (96; the files live
// on the server, so clients fetch them over the control channel).
type EmojiGet struct {
	Name string `json:"name"`
}

// EmojiData is the server's reply to EmojiGet.
type EmojiData struct {
	Name        string `json:"name"`
	DataBase64  string `json:"data_base64"`
	ContentType string `json:"content_type"`
}

// AuditLog requests an audit page (before_id = 0 for latest).
type AuditLog struct {
	BeforeID int64 `json:"before_id,omitempty"`
	Limit    int   `json:"limit,omitempty"`
}

// AuditEntry is one audit log row on the wire.
type AuditEntry struct {
	ID         int64  `json:"id"`
	Actor      string `json:"actor"`
	Action     string `json:"action"`
	Target     string `json:"target"`
	Detail     string `json:"detail"`
	CreatedAt  int64  `json:"created_at"`
	Restricted bool   `json:"restricted,omitempty"`
	Structured bool   `json:"structured,omitempty"`
}

// AuditLogResponse carries an audit page (newest first).
type AuditLogResponse struct {
	Entries      []AuditEntry                   `json:"entries"`
	Capabilities []authorization.CapabilityInfo `json:"capabilities,omitempty"`
}

// BanList requests the ban list (gated by ban power / admin).
type BanList struct{}

// BanEntry describes one ban.
type BanEntry struct {
	ID        int64  `json:"id"`
	Type      int    `json:"type"` // 0=IP, 1=unique_id, 2=nickname
	Value     string `json:"value"`
	Reason    string `json:"reason,omitempty"`
	BannedBy  string `json:"banned_by,omitempty"` // unique ID, when known
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at,omitempty"` // unix; 0 = permanent
}

// BanListResponse carries the ban list (newest first).
type BanListResponse struct {
	Bans []BanEntry `json:"bans"`
}

// BanRemove lifts one ban by ID.
type BanRemove struct {
	BanID int64 `json:"ban_id"`
}

// ClientInfoQuery requests the connection info of an online client.
type ClientInfoQuery struct {
	ClientID string `json:"client_id"`
}

// ClientInfoResponse is the connection info of an online client (TS3-style
// Client Info dialog). PingMs is -1 when unknown (the client never answered
// a server Ping). IP and Port are empty/0 unless the requester is the
// target itself, an admin, or holds b_client_remoteaddress_view.
type ClientInfoResponse struct {
	ClientID    string `json:"client_id"`
	UniqueID    string `json:"unique_id"`
	Nickname    string `json:"nickname"`
	ChannelID   int64  `json:"channel_id"`
	ConnectedAt int64  `json:"connected_at"` // unix seconds
	IdleSeconds int64  `json:"idle_seconds"`
	PingMs      int64  `json:"ping_ms"`
	IP          string `json:"ip,omitempty"`
	Port        int    `json:"port,omitempty"`
	BytesIn     int64  `json:"bytes_in"`
	BytesOut    int64  `json:"bytes_out"`
}

// Encode marshals a message into a Frame with the given type.
func Encode(mt MessageType, msg any) (*Frame, error) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("netproto: encoding %s: %w", mt, err)
	}
	return &Frame{Type: uint16(mt), Payload: payload}, nil
}

// Decode unmarshals a Frame's payload into the provided message struct. The
// caller is responsible for selecting the right struct based on f.Type.
func Decode(f *Frame, msg any) error {
	if f == nil {
		return fmt.Errorf("netproto: nil frame")
	}
	if len(f.Payload) == 0 {
		// Allow empty payloads (e.g. Ping) to decode into zero-value structs.
		return nil
	}
	if err := json.Unmarshal(f.Payload, msg); err != nil {
		return fmt.Errorf("netproto: decoding %s: %w", MessageType(f.Type), err)
	}
	return nil
}
