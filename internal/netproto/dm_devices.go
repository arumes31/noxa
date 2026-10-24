package netproto

// Direct messages to every device (dm_devices_v1).
//
// An account's devices are listed by the key directory (KeyRequest with
// Devices). Clients trust a device when its keys chain to the account's
// pinned identity: the first device's Ed25519 key is the signer of the
// account's noxa-dm-key-v1 record, and every other device carries an
// approval signature (DeviceApprovalTranscript) by a trusted device.
//
// A sender encrypts the message once per trusted device of the recipient
// and of its own account (except itself) and sends the copies in one
// ChatSend. The server routes each copy to that device's live session or
// spools it for that device. It refuses a send that misses one of the
// recipient's devices, so the sender refreshes the list and retries.

// CapabilityDMDevices advertises per-device direct messages.
const CapabilityDMDevices = "dm_devices_v1"

// MaxDMCopies bounds the copies of one direct message.
const MaxDMCopies = 24

// DMDevice is one device of an account as the key directory lists it.
type DMDevice struct {
	DeviceID      string `json:"device_id"`
	Kind          string `json:"kind"`
	Ed25519Public string `json:"ed25519_public_key"`
	X25519Public  string `json:"x25519_public_key"`
	FirstDevice   bool   `json:"first_device,omitempty"`
	// ApprovedBy and ApprovalSignature chain the device to the approving
	// device; both are empty for the first device.
	ApprovedBy        string `json:"approved_by,omitempty"`
	ApprovalSignature []byte `json:"approval_signature,omitempty"`
}

// DMCopy is a direct message sealed for one device: nacl/box from the
// sending device's X25519 key to the device's X25519 key, base64 of
// nonce||box.
type DMCopy struct {
	DeviceID string `json:"device_id"`
	Text     string `json:"text"`
}

// Direct-message edits and deletions are per-device copies too, so they
// reach every device like the original, including offline ones through the
// spool. DMAction names the change and RefClientMsgID the original
// message; for an edit the sealed text is the new body, for a deletion it
// is empty. Receivers apply a change only from the original's sender.
const (
	DMActionEdit   = "edit"
	DMActionDelete = "delete"
)

// ValidDMAction checks a direct-message change.
func ValidDMAction(action, ref string) bool {
	switch action {
	case "":
		return ref == ""
	case DMActionEdit, DMActionDelete:
		return ref != "" && len(ref) <= 128
	}
	return false
}

// DeviceActionEndorse lets a trusted device sign a device that was linked
// with a passkey, so the account's contacts accept it for direct messages.
// DeviceID names the device; Signature is over DeviceApprovalTranscript.
const DeviceActionEndorse = "endorse"

// History copy: after approving a device, a trusted device re-encrypts its
// direct-message history to the new device's X25519 key and uploads it in
// chunks (history_put, DeviceID = the new device). The new device downloads
// them (history_get, from Seq) and acknowledges (history_ack, through Seq);
// the server deletes acknowledged chunks and any chunk after 24 hours.
const (
	DeviceActionHistoryPut = "history_put"
	DeviceActionHistoryGet = "history_get"
	DeviceActionHistoryAck = "history_ack"

	// DeviceEventHistoryReady tells the new device that chunks are waiting.
	DeviceEventHistoryReady = "history_ready"
)

// History copy limits.
const (
	MaxHistoryChunk  = 256 << 10
	MaxHistoryChunks = 256
	MaxHistoryBytes  = 50 << 20
	// MaxHistoryMessages bounds the messages a device copies.
	MaxHistoryMessages = 10000
)

// HistoryChunk is one sealed part of a history copy. Data is nacl/box from
// the sending device (FromDeviceID) to the new device: nonce||box of a JSON
// array of HistoryMessage.
type HistoryChunk struct {
	Seq          int    `json:"seq"`
	Total        int    `json:"total"`
	FromDeviceID string `json:"from_device_id,omitempty"`
	Data         []byte `json:"data,omitempty"`
}

// HistoryMessage is one copied direct message, as the plaintext inside a
// chunk. Peer is the other account; Self marks messages this account sent.
type HistoryMessage struct {
	Peer         string `json:"peer"`
	PeerNickname string `json:"peer_nickname,omitempty"`
	From         string `json:"from"`
	FromNickname string `json:"from_nickname,omitempty"`
	Body         string `json:"body"`
	SentAt       int64  `json:"sent_at"`
	Self         bool   `json:"self,omitempty"`
	ClientMsgID  string `json:"client_msg_id,omitempty"`
}
