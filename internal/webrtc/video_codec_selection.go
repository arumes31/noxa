package webrtc

const (
	videoCodecVP8 uint8 = 1 << iota
	videoCodecH264
	videoCodecH265
)

// "h264" means the relay's baseline profile 42001f, packetization-mode=1.
// VP8 is the compatibility floor; absent capabilities mean a legacy client.
func videoCodecCapabilities(codecs []string) (uint8, bool) {
	if len(codecs) == 0 {
		return videoCodecVP8, true
	}
	if len(codecs) > 3 {
		return 0, false
	}
	var mask uint8
	for _, codec := range codecs {
		var bit uint8
		switch codec {
		case "vp8":
			bit = videoCodecVP8
		case "h264":
			bit = videoCodecH264
		case "h265":
			bit = videoCodecH265
		default:
			return 0, false
		}
		if mask&bit != 0 {
			return 0, false
		}
		mask |= bit
	}
	return mask, mask&videoCodecVP8 != 0
}

// Caller holds Router.mu and watchMu. Only an aggregate choice is disclosed;
// viewers (including invisible members) and their capabilities remain private.
func (r *Router) publicationCodecLocked(key publicationKey, generation uint64) string {
	if r.videoPolicy.bounds != (VideoBounds{}) || r.videoRecordingConsumerLocked(key) {
		return "vp8"
	}
	common := r.publicationCodecCapabilities[key]
	level := r.publicationHEVCLevels[key]
	if !validHEVCLevel(level) {
		common &^= videoCodecH265
	}
	for recipient, watch := range r.watches {
		if recipient.publisher == key.publisher && recipient.slot == key.slot && watch.active &&
			watch.publication == generation && watch.session == r.watchSessions[recipient.subscriber] &&
			r.clientChan[recipient.subscriber] == r.clientChan[key.publisher] && r.publisherAllowedLocked(recipient.subscriber, key.publisher) {
			common &= watch.codecs
			if watch.hevcLevel < level {
				common &^= videoCodecH265
			}
		}
	}
	if common&videoCodecH265 != 0 {
		return "h265"
	}
	if common&videoCodecH264 != 0 {
		return "h264"
	}
	return "vp8"
}

func videoFormatCapabilities(codecs []string, level uint8) (uint8, bool) {
	mask, ok := videoCodecCapabilities(codecs)
	if !ok || (mask&videoCodecH265 != 0 && !validHEVCLevel(level)) || (mask&videoCodecH265 == 0 && level != 0) {
		return 0, false
	}
	return mask, true
}

func (v *Voice) PublishVideoWithFormats(publisher, slot string, generation uint64, active bool, mode string, codecs []string, level uint8, revision ...uint64) (uint64, error) {
	return v.router.PublishVideoWithFormats(publisher, slot, generation, active, mode, codecs, level, revision...)
}
func (v *Voice) WatchVideoWithFormats(subscriber, publisher, slot string, generation, revision, session uint64, active bool, codecs []string, level uint8) (bool, error) {
	return v.router.WatchVideoWithFormats(subscriber, publisher, slot, generation, revision, session, active, codecs, level)
}

func (v *Voice) PublishVideoWithCodecs(publisher, slot string, generation uint64, active bool, mode string, codecs []string) (uint64, error) {
	return v.router.PublishVideoWithCodecs(publisher, slot, generation, active, mode, codecs)
}

func (v *Voice) WatchVideoWithCodecs(subscriber, publisher, slot string, generation, revision, session uint64, active bool, codecs []string) (bool, error) {
	return v.router.WatchVideoWithCodecs(subscriber, publisher, slot, generation, revision, session, active, codecs)
}
