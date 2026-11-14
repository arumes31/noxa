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

// DepartureReconnecting: a resumable session lost its connection. Like an
// update, its place is kept for the resume grace window and a resume is quiet.
const DepartureReconnecting = "reconnecting"

// ClientFeatureResume announces that the client presents its resume token on
// reconnect. Only such sessions keep their place after a lost connection;
// others keep the legacy "lost connection" / "reconnected" announcements.
const ClientFeatureResume = "resume-v1"

// Pre-connect update handover (handover-v1). A desktop that installed an
// update starts the new process while the old one keeps the session. The new
// process logs in with the session's resume token and Authenticate.Handover;
// the server answers AuthResponse.Standby and keeps that connection invisible
// (no presence, no broadcasts, no channel) until MsgHandoverCommit, then
// moves the session to it exactly like a resume taking over a half-open
// connection: same client ID, channel and stream watches, no leave or join.
// A standby that does not commit within 20 seconds is dropped and
// the old session continues untouched.
const (
	ClientFeatureHandover = "handover-v1"
	// CapabilityHandover: the server accepts handover standby logins. A client
	// requests one only from a server that advertised it, since an older
	// server would treat the token as an immediate resume.
	CapabilityHandover = "handover_v1"
	// MsgHandoverCommit (client -> server, empty payload) commits a standby
	// login; the reply is the session's regular AuthResponse and snapshot.
	MsgHandoverCommit MessageType = 206
	// ReasonHandoverUnavailable refuses a standby login or commit: no live
	// session for this token, account and device, or another handover is
	// already pending. The client falls back to a restart with resume.
	ReasonHandoverUnavailable = "update handover unavailable"
)
