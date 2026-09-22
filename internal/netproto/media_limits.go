package netproto

const (
	MsgMediaLimitsChanged MessageType = 163
	MsgMediaLimitsSet     MessageType = 164
	MsgMediaLimitsSaved   MessageType = 165
)

// MediaLimitsChanged is the complete effective configuration after a live
// update. Revision is positive and increases for this server process; a new
// authenticated connection establishes a new baseline. It is encoded as a
// decimal string so JavaScript bridges do not lose uint64 precision.
type MediaLimitsChanged struct {
	Revision uint64 `json:"revision,string"`
	MediaLimits
}

// MediaLimitsSet uses pointers so a management request can distinguish an
// explicit zero (unlimited) from an omitted field. All fields are required.
type MediaLimitsSet struct {
	VideoMaxBitrate *int `json:"video_max_bitrate"`
	VideoMaxWidth   *int `json:"video_max_width"`
	VideoMaxHeight  *int `json:"video_max_height"`
}

func (m MediaLimitsSet) Limits() (MediaLimits, bool) {
	if m.VideoMaxBitrate == nil || m.VideoMaxWidth == nil || m.VideoMaxHeight == nil {
		return MediaLimits{}, false
	}
	limits := MediaLimits{VideoMaxBitrate: *m.VideoMaxBitrate, VideoMaxWidth: *m.VideoMaxWidth, VideoMaxHeight: *m.VideoMaxHeight}
	return limits, limits.Valid()
}

// MediaLimitsSaved acknowledges the exact committed values and process-local
// revision. Revision may be zero when an initial unlimited save is a no-op.
type MediaLimitsSaved struct {
	Revision uint64 `json:"revision,string"`
	MediaLimits
}

// MediaLimits advertises independent server resource ceilings to publishers.
// Zero values preserve unlimited/legacy behavior; dimensions must be paired.
type MediaLimits struct {
	VideoMaxBitrate int `json:"video_max_bitrate"`
	VideoMaxWidth   int `json:"video_max_width"`
	VideoMaxHeight  int `json:"video_max_height"`
}

func (m MediaLimits) Valid() bool {
	return m.VideoMaxBitrate >= 0 && m.VideoMaxBitrate <= 100_000_000 &&
		((m.VideoMaxWidth == 0 && m.VideoMaxHeight == 0) ||
			(m.VideoMaxWidth >= 1 && m.VideoMaxWidth <= 16383 && m.VideoMaxHeight >= 1 && m.VideoMaxHeight <= 16383))
}
