package webrtc

const (
	videoCodecVP8 uint8 = 1 << iota
	videoCodecH264
)

// "h264" means the relay's baseline profile 42001f, packetization-mode=1.
// VP8 is the compatibility floor; absent capabilities mean a legacy client.
func videoCodecCapabilities(codecs []string) (uint8, bool) {
	if len(codecs) == 0 {
		return videoCodecVP8, true
	}
	if len(codecs) > 2 {
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
	if r.videoPolicy.bounds != (VideoBounds{}) || r.publicationCodecCapabilities[key]&videoCodecH264 == 0 || r.videoRecordingConsumerLocked(key) {
		return "vp8"
	}
	for recipient, watch := range r.watches {
		if recipient.publisher == key.publisher && recipient.slot == key.slot && watch.active &&
			watch.publication == generation && watch.session == r.watchSessions[recipient.subscriber] &&
			r.clientChan[recipient.subscriber] == r.clientChan[key.publisher] && r.publisherAllowedLocked(recipient.subscriber, key.publisher) &&
			watch.codecs&videoCodecH264 == 0 {
			return "vp8"
		}
	}
	return "h264"
}

func (v *Voice) PublishVideoWithCodecs(publisher, slot string, generation uint64, active bool, mode string, codecs []string) (uint64, error) {
	return v.router.PublishVideoWithCodecs(publisher, slot, generation, active, mode, codecs)
}

func (v *Voice) WatchVideoWithCodecs(subscriber, publisher, slot string, generation, revision, session uint64, active bool, codecs []string) (bool, error) {
	return v.router.WatchVideoWithCodecs(subscriber, publisher, slot, generation, revision, session, active, codecs)
}
