package netproto

const MaxVideoOperatorPublications = 32
const MaxVideoOperatorViewers = 32

// VideoOperatorDiagnostics is for the authenticated/local operator surface,
// never the viewer protocol: it reveals the identities of current watchers.
type VideoOperatorDiagnostics struct {
	SampledAt    int64                      `json:"sampled_at"`
	Publications []VideoOperatorPublication `json:"publications"`
	Truncated    bool                       `json:"truncated"`
}

type VideoOperatorPublication struct {
	PublisherID       string                        `json:"publisher_id"`
	PublisherNickname string                        `json:"publisher_nickname"`
	PublisherVersion  string                        `json:"publisher_version"`
	Slot              string                        `json:"slot"`
	Generation        uint64                        `json:"generation,string"`
	ChannelID         int64                         `json:"channel_id"`
	Layers            []VideoStreamLayerDiagnostics `json:"layers"`
	SenderReport      *VideoSenderReport            `json:"sender_report"`
	Viewers           []VideoOperatorViewer         `json:"viewers"`
	ViewersTruncated  bool                          `json:"viewers_truncated"`
}

type VideoOperatorViewer struct {
	ClientID        string                         `json:"client_id"`
	Nickname        string                         `json:"nickname"`
	ClientVersion   string                         `json:"client_version"`
	ConnectionState string                         `json:"connection_state"`
	Session         uint64                         `json:"session,string"`
	WatchRevision   uint64                         `json:"watch_revision,string"`
	WatchEpoch      uint64                         `json:"watch_epoch,string"`
	Watching        bool                           `json:"watching"`
	OutputExists    bool                           `json:"output_exists"`
	OutputActive    bool                           `json:"output_active"`
	OutputSSRC      uint32                         `json:"output_ssrc"`
	Forwarding      *VideoStreamForwardDiagnostics `json:"forwarding"`
	Pacer           *VideoPacerDiagnostics         `json:"pacer"`
}

// Pacer counters are cumulative for the recipient peer, across its video
// streams, including retransmissions. They are not source-specific losses.
// A nil feedback age means no bounded TWCC feedback report has arrived;
// feedback counters precede the estimator's semantic validation. Lifecycle
// queue purges on unbind/close are not network losses and are not counted.
type VideoPacerDiagnostics struct {
	TargetBitrateBPS   int    `json:"target_bitrate_bps"`
	QueuedPackets      int    `json:"queued_packets"`
	QueuedBytes        int    `json:"queued_bytes"`
	OldestAgeMS        int64  `json:"oldest_age_ms"`
	DroppedQueueFull   uint64 `json:"dropped_queue_full"`
	DroppedExpired     uint64 `json:"dropped_expired"`
	DroppedRetired     uint64 `json:"dropped_retired"`
	DroppedScope       uint64 `json:"dropped_scope"`
	SentPackets        uint64 `json:"sent_packets"`
	SentBytes          uint64 `json:"sent_bytes"`
	FinalWriteFailures uint64 `json:"final_write_failures"`
	FeedbackReports    uint64 `json:"feedback_reports"`
	FeedbackAgeMS      *int64 `json:"feedback_age_ms"`
}
