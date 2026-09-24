package netproto

const MsgPokeAccepted MessageType = 155

// PokeAccepted confirms that the target's outgoing queue accepted the poke.
// Delivery can still be interrupted or revoked before the target receives it.
type PokeAccepted struct {
	ClientID string `json:"client_id"`
}
