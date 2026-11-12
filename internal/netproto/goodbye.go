package netproto

// MsgGoodbye is sent by an updated client right before it closes its control
// connection on purpose (disconnect, quit, sign-out). The payload is empty.
// When a client that announced ClientFeatureGoodbye closes without it, the
// server reports the departure as a lost connection instead of a leave.
const MsgGoodbye MessageType = 205

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
)
