package webrtc

import (
	"time"

	"noxa/internal/netproto"
)

type mediaPacerCounters struct {
	droppedQueueFull, droppedExpired, droppedRetired, droppedScope uint64
	sentPackets, sentBytes, finalWriteFailures, feedbackReports    uint64
	lastFeedback                                                   time.Time
}

func (p *mediaPacer) recordVideoWrite(audio, attempted bool, n int, err error) {
	if audio {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case !attempted:
		p.diagnostics.droppedScope++
	case err != nil:
		p.diagnostics.finalWriteFailures++
	case n > 0:
		p.diagnostics.sentPackets++
		p.diagnostics.sentBytes += uint64(n)
	}
}

func (p *mediaPacer) recordVideoFeedback(now time.Time) {
	p.mu.Lock()
	p.diagnostics.feedbackReports++
	p.diagnostics.lastFeedback = now
	p.mu.Unlock()
}

func (p *mediaPacer) diagnosticSnapshot(now time.Time) *netproto.VideoPacerDiagnostics {
	p.mu.Lock()
	defer p.mu.Unlock()
	d := p.diagnostics
	result := &netproto.VideoPacerDiagnostics{
		TargetBitrateBPS: p.bitrate, QueuedPackets: p.count, QueuedBytes: p.bytes,
		DroppedQueueFull: d.droppedQueueFull, DroppedExpired: d.droppedExpired,
		DroppedRetired: d.droppedRetired, DroppedScope: d.droppedScope,
		SentPackets: d.sentPackets, SentBytes: d.sentBytes, FinalWriteFailures: d.finalWriteFailures,
		FeedbackReports: d.feedbackReports,
	}
	if p.count > 0 {
		result.OldestAgeMS = max(0, now.Sub(p.queue[p.head].expires.Add(-mediaPacerLifetime)).Milliseconds())
	}
	if !d.lastFeedback.IsZero() {
		age := max(0, now.Sub(d.lastFeedback).Milliseconds())
		result.FeedbackAgeMS = &age
	}
	return result
}
