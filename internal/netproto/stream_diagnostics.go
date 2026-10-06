package netproto

const CapabilityStreamDiagnostics = "stream_diagnostics_v1"

// VideoSenderDiagnostics reports one current publication encoding. Measured
// rates and limitation durations use SampleMS; encoder targets/settings and
// transport estimates are snapshots. Nil means unavailable or an initial/reset
// sample. Transport capacity is shared, not a per-encoding bitrate allowance.
type VideoSenderDiagnostics struct {
	SSRC                        uint32   `json:"ssrc"`
	RID                         string   `json:"rid"`
	Slot                        string   `json:"slot"`
	Generation                  uint64   `json:"generation,string"`
	SampleMS                    *float64 `json:"sample_ms"`
	RequestedFPS                *float64 `json:"requested_fps"`
	SettingsFPS                 *float64 `json:"settings_fps"`
	CaptureFPS                  *float64 `json:"capture_fps"`
	EncodedFPS                  *float64 `json:"encoded_fps"`
	SentFPS                     *float64 `json:"sent_fps"`
	ReportedFPS                 *float64 `json:"reported_fps"`
	Width                       *float64 `json:"width"`
	Height                      *float64 `json:"height"`
	BitrateBPS                  *float64 `json:"bitrate_bps"`
	TargetBitrateBPS            *float64 `json:"target_bitrate_bps"`
	AvailableOutgoingBitrateBPS *float64 `json:"available_outgoing_bitrate_bps"`
	TransportRTTMS              *float64 `json:"transport_rtt_ms"`
	RemoteRTTMS                 *float64 `json:"remote_rtt_ms"`
	RemoteFractionLost          *float64 `json:"remote_fraction_lost"`
	EncodingMaxBitrateBPS       *float64 `json:"encoding_max_bitrate_bps"`
	EncodingActive              *bool    `json:"encoding_active"`
	BandwidthLimitedMS          *float64 `json:"bandwidth_limited_ms"`
	CPULimitedMS                *float64 `json:"cpu_limited_ms"`
	RetransmitBitrateBPS        *float64 `json:"retransmit_bitrate_bps"`
	RetransmitPercent           *float64 `json:"retransmit_percent"`
	FrameBytes                  *float64 `json:"frame_bytes"`
	EncodeMS                    *float64 `json:"encode_ms"`
	SendDelayMS                 *float64 `json:"send_delay_ms"`
	QualityReason               string   `json:"quality_reason"`
	Codec                       string   `json:"codec"`
	EncoderImplementation       string   `json:"encoder_implementation"`
	PowerEfficient              *bool    `json:"power_efficient"`
	FramesEncoded               *float64 `json:"frames_encoded"`
	FramesSent                  *float64 `json:"frames_sent"`
	KeyFrames                   *float64 `json:"key_frames"`
	KeyFramesDelta              *float64 `json:"key_frames_delta"`
	PacketsSent                 *float64 `json:"packets_sent"`
	BytesSent                   *float64 `json:"bytes_sent"`
	RetransmittedPackets        *float64 `json:"retransmitted_packets"`
	RetransmittedBytes          *float64 `json:"retransmitted_bytes"`
}

func (r VideoSenderDiagnostics) Valid() bool {
	if r.SSRC == 0 || r.Generation == 0 || (r.Slot != "screen" && r.Slot != "cam") {
		return false
	}
	switch r.RID {
	case "", "q", "h", "f":
	default:
		return false
	}
	switch r.QualityReason {
	case "", "unknown", "none", "cpu", "bandwidth", "other":
	default:
		return false
	}
	switch r.Codec {
	case "", "unknown", "video/vp8", "video/vp9", "video/h264", "video/av1":
	default:
		return false
	}
	switch r.EncoderImplementation {
	case "", "unknown", "libvpx", "libaom", "openh264", "ExternalEncoder", "MediaFoundationVideoEncoder", "VideoToolbox":
	default:
		return false
	}
	for _, n := range []*float64{r.RequestedFPS, r.SettingsFPS, r.CaptureFPS, r.EncodedFPS, r.SentFPS, r.ReportedFPS} {
		if !diagnosticNumber(n, 0, 240) {
			return false
		}
	}
	for _, n := range []*float64{r.SampleMS, r.EncodeMS, r.SendDelayMS, r.TransportRTTMS, r.RemoteRTTMS} {
		if !diagnosticNumber(n, 0, 60000) {
			return false
		}
	}
	for _, n := range []*float64{r.Width, r.Height} {
		if !diagnosticNumber(n, 0, 16384) {
			return false
		}
	}
	for _, n := range []*float64{r.FramesEncoded, r.FramesSent, r.KeyFrames, r.KeyFramesDelta, r.PacketsSent, r.BytesSent, r.RetransmittedPackets, r.RetransmittedBytes} {
		if !diagnosticNumber(n, 0, 9007199254740991) {
			return false
		}
	}
	for _, n := range []*float64{r.BitrateBPS, r.TargetBitrateBPS, r.AvailableOutgoingBitrateBPS, r.EncodingMaxBitrateBPS, r.RetransmitBitrateBPS, r.FrameBytes} {
		if !diagnosticNumber(n, 0, 1000000000) {
			return false
		}
	}
	if r.KeyFrames != nil && r.FramesEncoded != nil && *r.KeyFrames > *r.FramesEncoded {
		return false
	}
	if r.RetransmittedBytes != nil && r.BytesSent != nil && *r.RetransmittedBytes > *r.BytesSent {
		return false
	}
	if r.RetransmittedPackets != nil && r.PacketsSent != nil && *r.RetransmittedPackets > *r.PacketsSent {
		return false
	}
	var limitedMS float64
	for _, n := range []*float64{r.BandwidthLimitedMS, r.CPULimitedMS} {
		if n != nil {
			if r.SampleMS == nil || *r.SampleMS <= 0 || !diagnosticNumber(n, 0, *r.SampleMS) {
				return false
			}
			limitedMS += *n
		}
	}
	if r.SampleMS != nil && limitedMS > *r.SampleMS {
		return false
	}
	return diagnosticNumber(r.RetransmitPercent, 0, 100) && diagnosticNumber(r.RemoteFractionLost, 0, 1)
}

type VideoSenderReport struct {
	ReceivedAt int64                    `json:"received_at"`
	AgeMS      int64                    `json:"age_ms"`
	Stale      bool                     `json:"stale"`
	Rows       []VideoSenderDiagnostics `json:"rows"`
}

// Stage frames count unique non-padding RTP timestamps, not decoded frames.
// Bytes include RTP headers; forwarding bytes also include retransmissions.
type VideoStreamStageDiagnostics struct {
	Packets    uint64   `json:"packets"`
	Bytes      uint64   `json:"bytes"`
	Frames     uint64   `json:"frames"`
	Markers    uint64   `json:"markers"`
	SampleMS   int64    `json:"sample_ms"`
	AgeMS      int64    `json:"age_ms"`
	Stale      bool     `json:"stale"`
	FPS        *float64 `json:"fps"`
	BitrateBPS *float64 `json:"bitrate_bps"`
}

type VideoStreamLayerDiagnostics struct {
	RID       string                      `json:"rid"`
	SSRC      uint32                      `json:"ssrc"`
	StartedAt int64                       `json:"started_at"`
	Ingress   VideoStreamStageDiagnostics `json:"ingress"`
}

type VideoStreamForwardDiagnostics struct {
	SourceSSRC uint32                      `json:"source_ssrc"`
	RID        string                      `json:"rid"`
	OutputSSRC uint32                      `json:"output_ssrc"`
	Stage      VideoStreamStageDiagnostics `json:"stage"`
}

type VideoStreamDiagnostics struct {
	PublisherID  string                         `json:"publisher_id"`
	Slot         string                         `json:"slot"`
	Generation   uint64                         `json:"generation,string"`
	Session      uint64                         `json:"session,string"`
	SampledAt    int64                          `json:"sampled_at"`
	SenderReport *VideoSenderReport             `json:"sender_report"`
	Layers       []VideoStreamLayerDiagnostics  `json:"layers"`
	Forwarding   *VideoStreamForwardDiagnostics `json:"forwarding"`
}
