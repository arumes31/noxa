package webrtc

import "noxa/internal/netproto"

type mediaTerminalDropReason uint8

const (
	mediaDropUnclassified mediaTerminalDropReason = iota
	mediaDropMissingBinding
	mediaDropInvalidTicket
	mediaDropMissingTicket
	mediaDropInactiveStream
	mediaDropExpiredTicket
	mediaDropPolicyBusy
	mediaDropPolicyRevision
	mediaDropScopeDeclined
	mediaDropGuardDeclined
	mediaDropReasonCount
)

type mediaPacketKind uint8

const (
	mediaPacketPrimary mediaPacketKind = iota
	mediaPacketRTX
	mediaPacketPadding
	mediaPacketKindCount
)

// Fixed-size counters retain no packet, ticket, payload, address or source ID.
// Their lifetime is the recipient pacer, just like dropped_scope. A publication
// or watch change does not reset them or reattribute old queued repairs.
type mediaTerminalDropCounters [mediaPacketKindCount][mediaDropReasonCount]uint64

func (d mediaTerminalDropCounters) snapshot() *netproto.VideoTerminalDropDiagnostics {
	convert := func(kind mediaPacketKind) netproto.VideoTerminalDropReasons {
		c := d[kind]
		return netproto.VideoTerminalDropReasons{
			Unclassified: c[mediaDropUnclassified], MissingBinding: c[mediaDropMissingBinding],
			InvalidTicket: c[mediaDropInvalidTicket], MissingTicket: c[mediaDropMissingTicket],
			InactiveStream: c[mediaDropInactiveStream], ExpiredTicket: c[mediaDropExpiredTicket],
			PolicyBusy: c[mediaDropPolicyBusy], PolicyRevision: c[mediaDropPolicyRevision],
			ScopeDeclined: c[mediaDropScopeDeclined], GuardDeclined: c[mediaDropGuardDeclined],
		}
	}
	return &netproto.VideoTerminalDropDiagnostics{
		Primary: convert(mediaPacketPrimary), RTX: convert(mediaPacketRTX), Padding: convert(mediaPacketPadding),
	}
}
