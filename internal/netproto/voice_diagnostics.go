package netproto

import (
	"math"
	"strings"
)

const (
	MsgVoiceTelemetry        MessageType = 188
	CapabilityVoiceTelemetry             = "voice-telemetry-v1"
	MaxVoiceDiagnosticTracks             = 64
	MaxVoiceTelemetryBytes               = 32 * 1024
)

// VoiceTelemetry contains allowlisted media measurements. The authenticated socket
// supplies the reporter identity; no audio, device names or addresses are sent.
type VoiceTelemetry struct {
	SessionID       string                     `json:"session_id,omitempty"`
	Truncated       bool                       `json:"truncated,omitempty"`
	ChannelID       int64                      `json:"channel_id"`
	ClientVersion   string                     `json:"client_version"`
	ConnectionState string                     `json:"connection_state"`
	OutputState     string                     `json:"output_state"`
	OutputLatencyMS *float64                   `json:"output_latency_ms"`
	RTTMS           *float64                   `json:"rtt_ms"`
	Muted           bool                       `json:"muted"`
	Deafened        bool                       `json:"deafened"`
	Volume          int                        `json:"volume"`
	VoiceLimiter    bool                       `json:"voice_limiter"`
	GainNormalize   bool                       `json:"gain_normalize"`
	Tracks          []VoiceReceiverDiagnostics `json:"tracks"`
	Transport       *VoiceICETransport         `json:"transport,omitempty"`
	Senders         []VoiceSenderDiagnostics   `json:"senders,omitempty"`
	VideoSenders    []VideoSenderDiagnostics   `json:"video_senders,omitempty"`
}

type VoiceICETransport struct {
	Protocol        string `json:"protocol"`
	LocalCandidate  string `json:"local_candidate"`
	RemoteCandidate string `json:"remote_candidate"`
	RelayProtocol   string `json:"relay_protocol"`
}

type VoiceSenderDiagnostics struct {
	SSRC             uint32   `json:"ssrc"`
	SampleMS         *float64 `json:"sample_ms"`
	PacketsSent      *float64 `json:"packets_sent"`
	BytesSent        *float64 `json:"bytes_sent"`
	PacketsPerSecond *float64 `json:"packets_per_second"`
	BitrateBPS       *float64 `json:"bitrate_bps"`
}

// Percentages and buffer delays describe SampleMS, not the entire call.
// Missing, initial or reset measurements are null, never a false healthy zero.
type VoiceReceiverDiagnostics struct {
	SSRC            *uint32  `json:"ssrc,omitempty"`
	TrackID         string   `json:"track_id"`
	PublisherID     string   `json:"publisher_id"`
	Codec           string   `json:"codec"`
	SampleMS        *float64 `json:"sample_ms"`
	PacketsReceived *float64 `json:"packets_received"`
	PacketsLost     *float64 `json:"packets_lost"`
	// Discarded packets were received but rejected by the jitter buffer, which
	// network loss alone cannot reveal. The percentage uses received packets.
	PacketsDiscarded  *float64 `json:"packets_discarded,omitempty"`
	DiscardPercent    *float64 `json:"discard_percent,omitempty"`
	BytesReceived     *float64 `json:"bytes_received"`
	TotalSamples      *float64 `json:"total_samples"`
	ConcealedSamples  *float64 `json:"concealed_samples"`
	ConcealmentEvents *float64 `json:"concealment_events"`
	// Optional cumulative counters distinguish silence concealment and adaptive
	// playback from lost speech. Older clients leave them nil.
	SilentConcealedSamples *float64 `json:"silent_concealed_samples,omitempty"`
	AcceleratedSamples     *float64 `json:"accelerated_samples,omitempty"`
	DeceleratedSamples     *float64 `json:"decelerated_samples,omitempty"`
	JitterMS               *float64 `json:"jitter_ms"`
	LossPercent            *float64 `json:"loss_percent"`
	ConcealmentPercent     *float64 `json:"concealment_percent"`
	// These interval percentages all use delta(TotalSamples) as denominator.
	SilentConcealmentPercent    *float64 `json:"silent_concealment_percent,omitempty"`
	NonSilentConcealmentPercent *float64 `json:"non_silent_concealment_percent,omitempty"`
	AccelerationPercent         *float64 `json:"acceleration_percent,omitempty"`
	DecelerationPercent         *float64 `json:"deceleration_percent,omitempty"`
	BufferMS                    *float64 `json:"buffer_ms"`
	BufferTargetMS              *float64 `json:"buffer_target_ms"`
	// Minimum delay excludes external playout constraints. All buffer values are
	// interval averages, not instantaneous or end-to-end latency.
	BufferMinimumMS *float64 `json:"buffer_minimum_ms,omitempty"`
	AudioLevel      *float64 `json:"audio_level"`
	// AudioActive describes audible activity over SampleMS, not the instantaneous
	// level. Nil means unavailable; false permits a neutral idle playback badge.
	AudioActive *bool `json:"audio_active,omitempty"`
}

func diagnosticText(s string, maximum int) bool {
	return len(s) <= maximum && !strings.ContainsAny(s, "\x00\r\n\t")
}

func ValidClientVersion(s string) bool {
	if len(s) > 100 {
		return false
	}
	for _, c := range s {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}

func diagnosticNumber(n *float64, low, high float64) bool {
	return n == nil || (!math.IsNaN(*n) && !math.IsInf(*n, 0) && *n >= low && *n <= high)
}

func (m VoiceTelemetry) Valid() bool {
	if len(m.SessionID) > 64 {
		return false
	}
	for _, c := range m.SessionID {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	if m.ChannelID < 1 || m.Volume < 0 || m.Volume > 200 || len(m.Tracks) > MaxVoiceDiagnosticTracks || !diagnosticText(m.ClientVersion, 100) {
		return false
	}
	switch m.ConnectionState {
	case "new", "connecting", "connected", "disconnected", "failed", "closed":
	default:
		return false
	}
	switch m.OutputState {
	case "unavailable", "running", "suspended", "interrupted", "closed":
	default:
		return false
	}
	if !diagnosticNumber(m.RTTMS, 0, 60000) || !diagnosticNumber(m.OutputLatencyMS, 0, 60000) {
		return false
	}
	if m.Transport != nil && !m.Transport.Valid() {
		return false
	}
	if len(m.Senders) > 8 || len(m.VideoSenders) > 8 {
		return false
	}
	senders := map[uint32]bool{}
	for _, sender := range m.Senders {
		if sender.SSRC == 0 || senders[sender.SSRC] || !diagnosticNumber(sender.SampleMS, 0, 60000) ||
			!diagnosticNumber(sender.PacketsSent, 0, 9007199254740991) || !diagnosticNumber(sender.BytesSent, 0, 9007199254740991) ||
			!diagnosticNumber(sender.PacketsPerSecond, 0, 1000000) || !diagnosticNumber(sender.BitrateBPS, 0, 1000000000) {
			return false
		}
		senders[sender.SSRC] = true
	}
	videoSenders := map[uint32]bool{}
	for _, sender := range m.VideoSenders {
		if !sender.Valid() || videoSenders[sender.SSRC] {
			return false
		}
		videoSenders[sender.SSRC] = true
	}
	seen := map[string]bool{}
	for _, t := range m.Tracks {
		if t.SSRC != nil && *t.SSRC == 0 {
			return false
		}
		if t.TrackID == "" || !diagnosticText(t.TrackID, 160) || !diagnosticText(t.PublisherID, 80) || !diagnosticText(t.Codec, 40) || seen[t.TrackID] {
			return false
		}
		seen[t.TrackID] = true
		for _, n := range []*float64{t.PacketsReceived, t.PacketsDiscarded, t.BytesReceived, t.TotalSamples, t.ConcealedSamples, t.ConcealmentEvents, t.SilentConcealedSamples, t.AcceleratedSamples, t.DeceleratedSamples} {
			if !diagnosticNumber(n, 0, 9007199254740991) {
				return false
			}
		}
		if !diagnosticNumber(t.PacketsLost, -9007199254740991, 9007199254740991) {
			return false
		}
		for _, n := range []*float64{t.SampleMS, t.JitterMS, t.BufferMS, t.BufferTargetMS, t.BufferMinimumMS} {
			if !diagnosticNumber(n, 0, 60000) {
				return false
			}
		}
		for _, n := range []*float64{t.LossPercent, t.DiscardPercent, t.ConcealmentPercent, t.SilentConcealmentPercent, t.NonSilentConcealmentPercent, t.AccelerationPercent, t.DecelerationPercent} {
			if !diagnosticNumber(n, 0, 100) {
				return false
			}
		}
		if !diagnosticNumber(t.AudioLevel, 0, 1) {
			return false
		}
		if t.SilentConcealedSamples != nil && t.ConcealedSamples != nil && *t.SilentConcealedSamples > *t.ConcealedSamples {
			return false
		}
		if t.PacketsDiscarded != nil && t.PacketsReceived != nil && *t.PacketsDiscarded > *t.PacketsReceived {
			return false
		}
	}
	return true
}

func (t VoiceICETransport) Valid() bool {
	protocol := func(s string, relay bool) bool {
		return s == "unknown" || s == "udp" || s == "tcp" || (relay && s == "tls")
	}
	candidate := func(s string) bool {
		return s == "unknown" || s == "host" || s == "srflx" || s == "prflx" || s == "relay"
	}
	return protocol(t.Protocol, false) && protocol(t.RelayProtocol, true) && candidate(t.LocalCandidate) && candidate(t.RemoteCandidate)
}

type VoiceClientReport struct {
	ReceivedAt int64          `json:"received_at"`
	AgeMS      int64          `json:"age_ms"`
	Stale      bool           `json:"stale"`
	Report     VoiceTelemetry `json:"report"`
}

type VoiceServerTrack struct {
	SSRC         uint32  `json:"ssrc"`
	PublisherID  string  `json:"publisher_id"`
	Slot         string  `json:"slot"`
	ReceivedAt   int64   `json:"received_at"`
	AgeMS        int64   `json:"age_ms"`
	Stale        bool    `json:"stale"`
	PacketsLost  int64   `json:"packets_lost"`
	FractionLost float64 `json:"fraction_lost"`
	JitterMS     float64 `json:"jitter_ms"`
}

type VoiceTransportDiagnostics struct {
	ConnectionState string             `json:"connection_state"`
	ReceiverReports []VoiceServerTrack `json:"receiver_reports"`
	Paths           []VoiceMediaPath   `json:"paths,omitempty"`
}

// History is a compact worst-track summary, not recorded media or raw RTCStats.
// All timestamps are server receipt times; they do not imply synchronized clocks.
type VoiceHistoryPoint struct {
	ObservedAt         int64    `json:"observed_at"`
	TrackCount         int      `json:"track_count"`
	LossPercent        *float64 `json:"loss_percent"`
	DiscardPercent     *float64 `json:"discard_percent"`
	ConcealmentPercent *float64 `json:"concealment_percent"`
	BufferMS           *float64 `json:"buffer_ms"`
	RTTMS              *float64 `json:"rtt_ms"`
	OutputState        string   `json:"output_state"`
}

type VoiceIngressDiagnostics struct {
	Publication      string   `json:"publication"`
	SSRC             uint32   `json:"ssrc"`
	StartedAt        int64    `json:"started_at"`
	AgeMS            int64    `json:"age_ms"`
	Stale            bool     `json:"stale"`
	Packets          uint64   `json:"packets"`
	Bytes            uint64   `json:"bytes"`
	SampleMS         float64  `json:"sample_ms"`
	PacketsPerSecond *float64 `json:"packets_per_second"`
	JitterMS         float64  `json:"jitter_ms"`
	MaxGapMS         float64  `json:"max_gap_ms"`
	BurstPackets     uint64   `json:"burst_packets"`
}

type VoiceMediaPath struct {
	PublisherID string                   `json:"publisher_id"`
	Slot        string                   `json:"slot"`
	OutputSSRC  uint32                   `json:"output_ssrc"`
	Ingress     *VoiceIngressDiagnostics `json:"ingress"`
}

type VoiceCorrelatedPath struct {
	PublisherID string                    `json:"publisher_id"`
	Slot        string                    `json:"slot"`
	OutputSSRC  uint32                    `json:"output_ssrc"`
	Ingress     *VoiceIngressDiagnostics  `json:"ingress"`
	Sender      *VoiceSenderDiagnostics   `json:"sender"`
	SenderAgeMS *int64                    `json:"sender_age_ms"`
	Receiver    *VoiceReceiverDiagnostics `json:"receiver"`
	Feedback    *VoiceServerTrack         `json:"feedback"`
}

// VoiceDiagnostics is a read-only snapshot for the current server owner,
// administrator or local operator. Client reports are untrusted evidence.
type VoiceDiagnostics struct {
	ClientVersion string                     `json:"client_version"`
	ServerVersion string                     `json:"server_version"`
	ObservedAt    int64                      `json:"observed_at"`
	ClientID      string                     `json:"client_id"`
	Nickname      string                     `json:"nickname"`
	ChannelID     int64                      `json:"channel_id"`
	PingMS        int64                      `json:"ping_ms"`
	ClientReport  *VoiceClientReport         `json:"client_report"`
	Transport     *VoiceTransportDiagnostics `json:"transport"`
	History       []VoiceHistoryPoint        `json:"history,omitempty"`
	Paths         []VoiceCorrelatedPath      `json:"paths,omitempty"`
}
