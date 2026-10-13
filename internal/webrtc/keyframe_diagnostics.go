package webrtc

import (
	"time"

	"noxa/internal/netproto"
)

type keyframeRequestReason uint8

const (
	keyframeExplicit keyframeRequestReason = iota
	keyframeConsumerStart
	keyframeRecordingSource
	keyframeWatchStart
	keyframeQualityChange
	keyframeVP8Bounds
	keyframeH264Assembly
	keyframeAwaitKeyframe
	keyframeViewerFeedback
)

type keyframeRequestOutcome uint8

const (
	keyframeRequested keyframeRequestOutcome = iota
	keyframeCoalesced
	keyframeWriteSucceeded
	keyframeWriteFailed
)

// The ingress pointer is captured with source selection and never looked up
// again after the write: a replaced source must not inherit an old outcome.
// A non-nil epoch also rejects outcomes after an in-place publication reset.
func (d *videoIngressDiagnostic) recordKeyframeRequest(reason keyframeRequestReason, outcome keyframeRequestOutcome, epoch *time.Time) time.Time {
	if d == nil {
		return time.Time{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if epoch != nil && !d.started.Equal(*epoch) {
		return d.started
	}
	if d.keyframeRequests == nil {
		d.keyframeRequests = &netproto.VideoKeyframeRequestDiagnostics{}
	}
	var counter *netproto.VideoKeyframeRequestCounters
	switch reason {
	case keyframeConsumerStart:
		counter = &d.keyframeRequests.ConsumerStart
	case keyframeRecordingSource:
		counter = &d.keyframeRequests.RecordingSource
	case keyframeWatchStart:
		counter = &d.keyframeRequests.WatchStart
	case keyframeQualityChange:
		counter = &d.keyframeRequests.QualityChange
	case keyframeVP8Bounds:
		counter = &d.keyframeRequests.VP8Bounds
	case keyframeH264Assembly:
		counter = &d.keyframeRequests.H264Assembly
	case keyframeAwaitKeyframe:
		counter = &d.keyframeRequests.AwaitKeyframe
	case keyframeViewerFeedback:
		counter = &d.keyframeRequests.ViewerFeedback
	default:
		counter = &d.keyframeRequests.Explicit
	}
	switch outcome {
	case keyframeRequested:
		counter.Requests++
	case keyframeCoalesced:
		counter.Coalesced++
	case keyframeWriteSucceeded:
		counter.WriteSucceeded++
	case keyframeWriteFailed:
		counter.WriteFailed++
	}
	return d.started
}

// Caller holds d.mu. Fixed value fields make this a detached snapshot.
func (d *videoIngressDiagnostic) keyframeRequestSnapshot() *netproto.VideoKeyframeRequestDiagnostics {
	if d.keyframeRequests == nil {
		return nil
	}
	snapshot := *d.keyframeRequests
	return &snapshot
}
