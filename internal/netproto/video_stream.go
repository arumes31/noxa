package netproto

const (
	MsgVideoStreamControl         MessageType = 166
	MsgVideoStreamResult          MessageType = 167
	CapabilityStreamSourceQuality             = "stream_source_quality_v1"
)

// VideoStreamControl has mandatory acknowledgement. Publication generations
// travel as decimal strings so WebView numbers cannot truncate their identity.
type VideoStreamControl struct {
	Action      string `json:"action"`
	PublisherID string `json:"publisher_id"`
	Slot        string `json:"slot"`
	Generation  uint64 `json:"generation,string"`
	Revision    uint64 `json:"revision,string"`
	Session     uint64 `json:"session,string"`
	Active      bool   `json:"active"`
	JPEG        []byte `json:"jpeg,omitempty"`
	QualityMode string `json:"quality_mode,omitempty"`
}

type VideoStream struct {
	WatchRevision uint64 `json:"watch_revision,string"`
	ViewerCount   *int   `json:"viewer_count,omitempty"`
	PublisherID   string `json:"publisher_id"`
	Slot          string `json:"slot"`
	Generation    uint64 `json:"generation,string"`
	PreviewAt     int64  `json:"preview_at"`
	QualityMode   string `json:"quality_mode,omitempty"`
	// Only the publisher receives upload demand. Nil means unknown/legacy;
	// false is authoritative no-demand, including server recorder consumers.
	UploadActive *bool `json:"upload_active,omitempty"`
}

type VideoStreamResult struct {
	Action       string                  `json:"action"`
	PublisherID  string                  `json:"publisher_id"`
	Slot         string                  `json:"slot"`
	Generation   uint64                  `json:"generation,string"`
	Revision     uint64                  `json:"revision,string"`
	Session      uint64                  `json:"session,string"`
	Active       bool                    `json:"active"`
	Streams      []VideoStream           `json:"streams"`
	JPEG         []byte                  `json:"jpeg,omitempty"`
	PreviewAt    int64                   `json:"preview_at"`
	Diagnostics  *VideoStreamDiagnostics `json:"diagnostics,omitempty"`
	QualityMode  string                  `json:"quality_mode,omitempty"`
	UploadActive *bool                   `json:"upload_active,omitempty"`
}
