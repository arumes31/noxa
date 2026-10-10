package webrtc

import (
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

func TestVideoPacerDiagnosticsDistinguishLocalDropReasons(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newMediaPacer()
		defer func() { _ = p.Close() }()
		p.SetTargetBitrate(0)
		var fail atomic.Bool
		p.AddStream(1, interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
			if fail.Load() {
				return 0, errors.New("test write failure")
			}
			return header.MarshalSize() + len(payload), nil
		}))
		send := func() { _, _ = p.Write(&rtp.Header{Version: 2, SSRC: 1}, make([]byte, 100), nil) }
		for range mediaPacerPackets + 1 {
			send()
		}
		time.Sleep(100 * time.Millisecond)
		queued := p.diagnosticSnapshot(time.Now())
		if queued.QueuedPackets != mediaPacerPackets || queued.QueuedBytes != mediaPacerPackets*112 ||
			queued.DroppedQueueFull != 1 || queued.OldestAgeMS != 100 || queued.FeedbackAgeMS != nil {
			t.Fatalf("incorrect bounded queue diagnostic: %+v", queued)
		}
		time.Sleep(mediaPacerLifetime)
		expired := p.diagnosticSnapshot(time.Now())
		if expired.QueuedPackets != 0 || expired.QueuedBytes != 0 || expired.OldestAgeMS != 0 || expired.DroppedExpired != mediaPacerPackets {
			t.Fatalf("expiration not distinguished from queue overflow: %+v", expired)
		}
		p.SetTargetBitrate(1_000_000)
		send()
		time.Sleep(10 * time.Millisecond)
		fail.Store(true)
		send()
		time.Sleep(10 * time.Millisecond)
		written := p.diagnosticSnapshot(time.Now())
		if written.SentPackets != 1 || written.SentBytes != 112 || written.FinalWriteFailures != 1 || written.DroppedScope != 0 {
			t.Fatalf("writes/authorization/failures were conflated: %+v", written)
		}
		denied := newMediaPacer()
		defer func() { _ = denied.Close() }()
		denied.egress = &mediaEgressRegistry{}
		denied.AddStream(1, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
			t.Error("missing output binding reached network writer")
			return 0, nil
		}))
		_, _ = denied.Write(&rtp.Header{Version: 2, SSRC: 1}, []byte{1}, nil)
		time.Sleep(10 * time.Millisecond)
		if got := denied.diagnosticSnapshot(time.Now()); got.DroppedScope != 1 || got.FinalWriteFailures != 0 {
			t.Fatalf("scope denial was confused with a write failure: %+v", got)
		}
	})
}
