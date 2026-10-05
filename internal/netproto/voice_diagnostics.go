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

// VoiceTelemetry contains receiver measurements only. The authenticated socket
// supplies the reporter identity; no audio, device names or addresses are sent.
type VoiceTelemetry struct {
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
}

// Percentages and buffer delays describe SampleMS, not the entire call.
// Missing, initial or reset measurements are null, never a false healthy zero.
type VoiceReceiverDiagnostics struct {
	TrackID            string   `json:"track_id"`
	PublisherID        string   `json:"publisher_id"`
	Codec              string   `json:"codec"`
	SampleMS           *float64 `json:"sample_ms"`
	PacketsReceived    *float64 `json:"packets_received"`
	PacketsLost        *float64 `json:"packets_lost"`
	BytesReceived      *float64 `json:"bytes_received"`
	TotalSamples       *float64 `json:"total_samples"`
	ConcealedSamples   *float64 `json:"concealed_samples"`
	ConcealmentEvents  *float64 `json:"concealment_events"`
	JitterMS           *float64 `json:"jitter_ms"`
	LossPercent        *float64 `json:"loss_percent"`
	ConcealmentPercent *float64 `json:"concealment_percent"`
	BufferMS           *float64 `json:"buffer_ms"`
	BufferTargetMS     *float64 `json:"buffer_target_ms"`
	AudioLevel         *float64 `json:"audio_level"`
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
	seen := map[string]bool{}
	for _, t := range m.Tracks {
		if t.TrackID == "" || !diagnosticText(t.TrackID, 160) || !diagnosticText(t.PublisherID, 80) || !diagnosticText(t.Codec, 40) || seen[t.TrackID] {
			return false
		}
		seen[t.TrackID] = true
		for _, n := range []*float64{t.PacketsReceived, t.BytesReceived, t.TotalSamples, t.ConcealedSamples, t.ConcealmentEvents} {
			if !diagnosticNumber(n, 0, 9007199254740991) {
				return false
			}
		}
		if !diagnosticNumber(t.PacketsLost, -9007199254740991, 9007199254740991) {
			return false
		}
		for _, n := range []*float64{t.SampleMS, t.JitterMS, t.BufferMS, t.BufferTargetMS} {
			if !diagnosticNumber(n, 0, 60000) {
				return false
			}
		}
		if !diagnosticNumber(t.LossPercent, 0, 100) || !diagnosticNumber(t.ConcealmentPercent, 0, 100) || !diagnosticNumber(t.AudioLevel, 0, 1) {
			return false
		}
	}
	return true
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
	PacketsLost  int32   `json:"packets_lost"`
	FractionLost float64 `json:"fraction_lost"`
	JitterMS     float64 `json:"jitter_ms"`
}

type VoiceTransportDiagnostics struct {
	ConnectionState string             `json:"connection_state"`
	ReceiverReports []VoiceServerTrack `json:"receiver_reports"`
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
}
