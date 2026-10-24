package netproto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"encoding/hex"
	"strings"
)

const (
	MsgDeviceRequest MessageType = 195 // client -> server: device management and linking
	MsgDeviceResult  MessageType = 196 // server -> client: reply to a DeviceRequest
	MsgDeviceEvent   MessageType = 197 // server -> client: device change of the own account
)

// Device actions. link_claim and link_status are the only device actions
// accepted before authentication.
const (
	DeviceActionList       = "list"
	DeviceActionRename     = "rename"
	DeviceActionRevoke     = "revoke"
	DeviceActionLinkCreate = "link_create"
	DeviceActionLinkCancel = "link_cancel"
	DeviceActionLinkDecide = "link_decide"
	DeviceActionLinkClaim  = "link_claim"
	DeviceActionLinkStatus = "link_status"

	// Passkeys. Registration needs a signed-in linked device; login runs
	// before authentication and links the calling device to the account.
	DeviceActionPasskeyRegisterBegin  = "passkey_register_begin"
	DeviceActionPasskeyRegisterFinish = "passkey_register_finish"
	DeviceActionPasskeyLoginBegin     = "passkey_login_begin"
	DeviceActionPasskeyLoginFinish    = "passkey_login_finish"
	DeviceActionPasskeyDelete         = "passkey_delete"

	// Web Push for web devices: the server's VAPID key and this device's
	// subscription and preferences.
	DeviceActionPushConfig      = "push_config"
	DeviceActionPushSubscribe   = "push_subscribe"
	DeviceActionPushUnsubscribe = "push_unsubscribe"
	DeviceActionPushPreferences = "push_preferences"
	// Activity reports input on a desktop (throttled by the client) or the
	// visibility of a web app; it delays or skips pushes. No reply is sent.
	DeviceActionActivity = "activity"
	// Read announces that this device read a conversation, so the account's
	// other devices clear badges and notifications. No reply is sent.
	DeviceActionRead = "read"
)

// Read scopes.
const (
	ReadScopeDM           = "dm"           // ID: the peer's unique ID
	ReadScopeConversation = "conversation" // ID: the group conversation ID
	ReadScopeChannel      = "channel"      // ID: the channel ID
)

// ReadMarker names a conversation read up to MessageID (optional).
type ReadMarker struct {
	Scope     string `json:"scope"`
	ID        string `json:"id"`
	MessageID string `json:"message_id,omitempty"`
}

// Valid checks the marker shape.
func (m ReadMarker) Valid() bool {
	switch m.Scope {
	case ReadScopeDM, ReadScopeConversation, ReadScopeChannel:
	default:
		return false
	}
	return m.ID != "" && len(m.ID) <= 128 && len(m.MessageID) <= 128
}

// PushSubscriptionKeys is a browser PushSubscription (keys base64url).
type PushSubscriptionKeys struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

// PushPreferences are a web device's notification settings.
type PushPreferences struct {
	Subscribed bool `json:"subscribed"`
	// HideDetails sends "New message" and "Incoming call" without names.
	HideDetails bool `json:"hide_details"`
	// AlwaysNotify skips the delay while the account is active on a desktop.
	AlwaysNotify bool `json:"always_notify"`
}

// MaxWebAuthnPayload bounds WebAuthn options and responses in device frames.
const MaxWebAuthnPayload = 16 << 10

// Link states reported by link_status and link events.
const (
	LinkStatePending  = "pending"  // created, not yet claimed
	LinkStateClaimed  = "claimed"  // a device claimed it, awaiting approval
	LinkStateApproved = "approved" // the device may sign in with its key
	LinkStateDenied   = "denied"
	LinkStateExpired  = "expired"
)

// Device events.
const (
	DeviceEventLinkClaimed   = "link_claimed"
	DeviceEventLinked        = "linked"
	DeviceEventRevoked       = "revoked"
	DeviceEventRenamed       = "renamed"
	DeviceEventExpiryWarning = "expiry_warning"
	DeviceEventRead          = "read"
)

// DeviceRequest carries one device action. Fields not used by the action
// must be empty.
type DeviceRequest struct {
	Action   string `json:"action"`
	DeviceID string `json:"device_id,omitempty"`
	Name     string `json:"name,omitempty"`

	// Linking.
	LinkID  string `json:"link_id,omitempty"`
	Code    string `json:"code,omitempty"`  // hidden code typed instead of scanning
	Proof   []byte `json:"proof,omitempty"` // HMAC-SHA256(secret, LinkClaimTranscript)
	Approve bool   `json:"approve,omitempty"`
	// Signature is the approving device's Ed25519 signature over
	// DeviceApprovalTranscript.
	Signature []byte `json:"signature,omitempty"`

	// Claiming device.
	Ed25519PublicKey string `json:"ed25519_public_key,omitempty"`
	X25519PublicKey  string `json:"x25519_public_key,omitempty"`
	Platform         string `json:"platform,omitempty"`

	// Passkeys: the browser's WebAuthn response, and the passkey to delete
	// (base64url credential ID).
	WebAuthn  json.RawMessage `json:"webauthn,omitempty"`
	PasskeyID string          `json:"passkey_id,omitempty"`

	// Push and activity.
	Subscription *PushSubscriptionKeys `json:"subscription,omitempty"`
	Preferences  *PushPreferences      `json:"preferences,omitempty"`
	Active       bool                  `json:"active,omitempty"`
	Read         *ReadMarker           `json:"read,omitempty"`
}

// Valid checks the action-specific shape; handlers check authorization.
func (r DeviceRequest) Valid() bool {
	if len(r.Name) > 256 || len(r.Platform) > 128 || len(r.Code) > 16 || len(r.LinkID) > 64 ||
		len(r.DeviceID) > 64 || len(r.Proof) > 64 || len(r.Signature) > 128 ||
		len(r.Ed25519PublicKey) > 512 || len(r.X25519PublicKey) > 128 ||
		len(r.WebAuthn) > MaxWebAuthnPayload || len(r.PasskeyID) > 1400 {
		return false
	}
	if r.Subscription != nil && (len(r.Subscription.Endpoint) > 1024 || len(r.Subscription.P256dh) > 128 || len(r.Subscription.Auth) > 64) {
		return false
	}
	switch r.Action {
	case DeviceActionPushConfig, DeviceActionPushUnsubscribe, DeviceActionActivity:
		return r.Subscription == nil && r.Read == nil
	case DeviceActionPushSubscribe:
		return r.Subscription != nil && r.Subscription.Endpoint != ""
	case DeviceActionPushPreferences:
		return r.Preferences != nil
	case DeviceActionRead:
		return r.Read != nil && r.Read.Valid()
	case DeviceActionPasskeyRegisterBegin, DeviceActionPasskeyLoginBegin:
		return len(r.WebAuthn) == 0 && r.DeviceID == "" && r.LinkID == ""
	case DeviceActionPasskeyRegisterFinish:
		return len(r.WebAuthn) > 0
	case DeviceActionPasskeyLoginFinish:
		return len(r.WebAuthn) > 0 && r.Ed25519PublicKey != "" && r.X25519PublicKey != ""
	case DeviceActionPasskeyDelete:
		return r.PasskeyID != ""
	case DeviceActionList, DeviceActionLinkCreate:
		return r.DeviceID == "" && r.LinkID == ""
	case DeviceActionRename:
		return r.DeviceID != "" && strings.TrimSpace(r.Name) != ""
	case DeviceActionRevoke:
		return r.DeviceID != ""
	case DeviceActionLinkCancel:
		return r.LinkID != ""
	case DeviceActionLinkDecide:
		return r.LinkID != "" && (!r.Approve || len(r.Signature) > 0)
	case DeviceActionLinkClaim:
		hasQR := r.LinkID != "" && len(r.Proof) > 0 && r.Code == ""
		hasCode := r.Code != "" && r.LinkID == "" && len(r.Proof) == 0
		return (hasQR || hasCode) && r.Ed25519PublicKey != "" && r.X25519PublicKey != ""
	case DeviceActionLinkStatus:
		return r.LinkID != "" && r.Ed25519PublicKey != ""
	}
	return false
}

// DeviceInfo describes one linked device.
type DeviceInfo struct {
	DeviceID       string `json:"device_id"`
	Kind           string `json:"kind"`
	Name           string `json:"name"`
	Fingerprint    string `json:"fingerprint"`
	X25519Public   string `json:"x25519_public_key,omitempty"`
	FirstDevice    bool   `json:"first_device,omitempty"`
	ApprovedBy     string `json:"approved_by,omitempty"`
	Current        bool   `json:"current,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	LastSeenAt     int64  `json:"last_seen_at"`
	ExpiryWarnedAt int64  `json:"expiry_warned_at,omitempty"`
	RevokedAt      int64  `json:"revoked_at,omitempty"`
}

// PasskeyInfo describes one of the account's passkeys.
type PasskeyInfo struct {
	PasskeyID  string `json:"passkey_id"` // base64url credential ID
	Name       string `json:"name"`
	DeviceID   string `json:"device_id"` // the device that registered it
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at,omitempty"`
}

// DeviceResult answers a DeviceRequest.
type DeviceResult struct {
	Action   string        `json:"action"`
	Devices  []DeviceInfo  `json:"devices,omitempty"`
	Device   *DeviceInfo   `json:"device,omitempty"`
	Passkeys []PasskeyInfo `json:"passkeys,omitempty"`
	// WebAuthn carries creation or request options for navigator.credentials.
	WebAuthn json.RawMessage `json:"webauthn,omitempty"`

	// Linking.
	LinkID    string `json:"link_id,omitempty"`
	Secret    []byte `json:"secret,omitempty"` // only to the creating device
	Code      string `json:"code,omitempty"`   // only to the creating device
	URL       string `json:"url,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	State     string `json:"state,omitempty"`
	// ApproverFingerprint lets the claiming device compare the approver.
	ApproverFingerprint string `json:"approver_fingerprint,omitempty"`

	// Push: the server's VAPID public key (base64url) and this device's
	// preferences. An empty key means the server does not send pushes.
	VAPIDPublicKey string           `json:"vapid_public_key,omitempty"`
	Preferences    *PushPreferences `json:"preferences,omitempty"`
}

// DeviceEvent notifies every session of the account about device changes.
type DeviceEvent struct {
	Event  string      `json:"event"`
	Device *DeviceInfo `json:"device,omitempty"`

	// link_claimed: the claiming device awaiting the creator's decision.
	LinkID           string `json:"link_id,omitempty"`
	Platform         string `json:"platform,omitempty"`
	Ed25519PublicKey string `json:"ed25519_public_key,omitempty"`
	X25519PublicKey  string `json:"x25519_public_key,omitempty"`
	// Via is "qr" or "code": which link key the claim used, so the creator
	// can compute the same LinkSAS as the claiming device.
	Via string `json:"via,omitempty"`
	// Reason accompanies revocations: user, admin_reset, idle, integration.
	Reason string `json:"reason,omitempty"`
	// ExpiresAt is the revocation time announced by an expiry warning.
	ExpiresAt int64 `json:"expires_at,omitempty"`
	// Read: another device of the account read a conversation.
	Read *ReadMarker `json:"read,omitempty"`
}

// KeyFingerprint is the comparison fingerprint of an identity key: the first
// 16 hex characters of SHA-256 over the key's canonical text, in groups of 4.
func KeyFingerprint(publicKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(publicKey)))
	h := hex.EncodeToString(sum[:8])
	return h[0:4] + " " + h[4:8] + " " + h[8:12] + " " + h[12:16]
}

// LinkClaimTranscript is the message a claiming device MACs with the link
// secret (QR) or the hidden code (typed).
func LinkClaimTranscript(linkID, ed25519Public, x25519Public string) []byte {
	return []byte("noxa-device-link-claim-v1\n" + linkID + "\n" + ed25519Public + "\n" + x25519Public)
}

// DeviceApprovalTranscript is the message the approving device signs. The
// signature travels with the new device so other clients can verify who
// approved it.
func DeviceApprovalTranscript(principal, approverDeviceID, ed25519Public, x25519Public string) []byte {
	return []byte("noxa-device-approval-v1\n" + principal + "\n" + approverDeviceID + "\n" + ed25519Public + "\n" + x25519Public)
}

// LinkSAS is the two-digit code both linking screens show. Each side derives
// it locally from the link key it holds (QR secret or hidden code), so the
// server cannot choose it.
func LinkSAS(linkKey, transcript []byte) string {
	mac := hmac.New(sha256.New, linkKey)
	mac.Write([]byte("noxa-device-link-sas-v1\n"))
	mac.Write(transcript)
	sum := mac.Sum(nil)
	n := (uint16(sum[0])<<8 | uint16(sum[1])) % 100
	return string([]byte{byte('0' + n/10), byte('0' + n%10)})
}

// LinkProof is the claim MAC over LinkClaimTranscript with the link key.
func LinkProof(linkKey, transcript []byte) []byte {
	mac := hmac.New(sha256.New, linkKey)
	mac.Write(transcript)
	return mac.Sum(nil)
}

// CapabilityDevices advertises device management and device linking.
const CapabilityDevices = "devices_v1"

// IntegrationDeviceRequest is the ServerQuery and gRPC form of device
// administration for one account.
type IntegrationDeviceRequest struct {
	Action   string `json:"action"` // list, revoke, reset
	UniqueID string `json:"unique_id"`
	DeviceID string `json:"device_id,omitempty"`
	// Keep is the device a reset leaves active; empty revokes every device.
	Keep string `json:"keep,omitempty"`
}

// Valid checks the request shape.
func (r IntegrationDeviceRequest) Valid() bool {
	if r.UniqueID == "" || len(r.UniqueID) > 256 || len(r.DeviceID) > 64 || len(r.Keep) > 64 {
		return false
	}
	switch r.Action {
	case "list":
		return r.DeviceID == "" && r.Keep == ""
	case "revoke":
		return r.DeviceID != "" && r.Keep == ""
	case "reset":
		return r.DeviceID == ""
	}
	return false
}

// IntegrationDeviceResult lists the account's devices after the action and
// the devices it revoked.
type IntegrationDeviceResult struct {
	Action   string       `json:"action"`
	UniqueID string       `json:"unique_id"`
	Devices  []DeviceInfo `json:"devices"`
	Revoked  []DeviceInfo `json:"revoked,omitempty"`
}
