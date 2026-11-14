package netproto

// MsgGoodbye is sent by an updated client right before it closes its control
// connection on purpose (disconnect, quit, sign-out). The payload is empty or
// a Goodbye object; servers ignore fields they do not know.
// When a client that announced ClientFeatureGoodbye closes without it, the
// server reports the departure as a lost connection instead of a leave.
const MsgGoodbye MessageType = 205

// Goodbye is the optional MsgGoodbye payload.
type Goodbye struct {
	// Reason is GoodbyeReasonUpdate when the client quits to install an
	// update; empty for every other deliberate close.
	Reason string `json:"reason,omitempty"`
}

// GoodbyeReasonUpdate: the client restarts into an update and comes back soon.
const GoodbyeReasonUpdate = "update"

// ClientFeatureGoodbye announces that the client sends MsgGoodbye before a
// deliberate close. Clients without it keep the legacy reading of a clean
// close as a deliberate leave.
const ClientFeatureGoodbye = "goodbye-v1"

// Departure reasons carried by user_left events and role snapshots.
const (
	// DepartureConnectionLost: the connection dropped without a deliberate close.
	DepartureConnectionLost = "connection_lost"
	// DepartureReplaced: the same device already reconnected on a new session;
	// observers stay silent for the old one.
	DepartureReplaced = "replaced"
	// DepartureUpdating: the client quit to install an update. Its place is
	// kept for a short while; a return is quiet, no return becomes a leave.
	DepartureUpdating = "updating"
)
