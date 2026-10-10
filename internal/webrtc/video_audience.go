package webrtc

// A registered recording output consumes video even without a person watching.
// Match its slot selection without exposing its identity or counting it as a
// viewer. Recording authorization independently gates every eventual write.
// Caller holds Router.mu for reading.
func (r *Router) videoRecordingConsumerLocked(key publicationKey) bool {
	tap := r.senderVideoTapID(key.publisher)
	if tap == "" || !r.echoPublisherAllowedLocked(tap, key.publisher) {
		return false
	}
	output := r.videoOutputs[tap]
	if output == nil {
		return false
	}
	_, scoped := output.(ScopedTrackWriter)
	return scoped || key.slot == r.recordingVideoSlotLocked(key.publisher)
}
