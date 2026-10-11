package webrtc

import (
	"io"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

// Model Pion's independent 50-packet repair queue and primary read: RTX is
// polled before the primary read, but cannot wake it once that read blocks.
type repairPollingTrack struct {
	initial      []*rtp.Packet
	repairs      chan *rtp.Packet
	closed       chan struct{}
	deadline     time.Time
	dropped      int
	timeouts     int
	gapDeadlines int
}

func (*repairPollingTrack) RID() string     { return "" }
func (*repairPollingTrack) SSRC() pion.SSRC { return 1 }
func (r *repairPollingTrack) SetReadDeadline(deadline time.Time) error {
	r.deadline = deadline
	if !deadline.IsZero() {
		r.gapDeadlines++
	}
	return nil
}

func (r *repairPollingTrack) ReadRTP() (*rtp.Packet, interceptor.Attributes, error) {
	select {
	case packet := <-r.repairs:
		return packet, nil, nil
	default:
	}
	if len(r.initial) != 0 {
		packet := r.initial[0]
		r.initial = r.initial[1:]
		return packet, nil, nil
	}
	if r.deadline.IsZero() {
		<-r.closed
		return nil, nil, io.EOF
	}
	timer := time.NewTimer(max(0, time.Until(r.deadline)))
	defer timer.Stop()
	select {
	case <-r.closed:
		return nil, nil, io.EOF
	case <-timer.C:
		r.timeouts++
		return nil, nil, os.ErrDeadlineExceeded
	}
}

func repairPollingFragment(sequence uint16) *rtp.Packet {
	header := byte(1)
	if sequence == 1 {
		header |= 0x80
	}
	if sequence == 102 {
		header |= 0x40
	}
	return &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 1, SequenceNumber: sequence, Timestamp: 9000, Marker: sequence == 102}, Payload: []byte{0x7c, header, 1}}
}

func TestVideoReorderPollsRepairBurstBeforePrimaryDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		track := &repairPollingTrack{initial: []*rtp.Packet{repairPollingFragment(1), repairPollingFragment(102)}, repairs: make(chan *rtp.Packet, 50), closed: make(chan struct{})}
		go func() {
			time.Sleep(20 * time.Millisecond)
			for sequence := uint16(2); sequence <= 101; sequence++ {
				select {
				case track.repairs <- repairPollingFragment(sequence):
				default:
					track.dropped++
				}
				time.Sleep(time.Millisecond)
			}
			time.Sleep(180 * time.Millisecond)
			close(track.closed)
		}()
		var sequences []uint16
		var frame h264ForwardFrame
		completeFrames := 0
		readOrderedVideo(track, func() bool { return true }, func() string { return pion.MimeTypeH264 }, func(packet *rtp.Packet, _ string) {
			sequences = append(sequences, packet.SequenceNumber)
			if packets, _, _ := frame.push(packet, time.Now()); len(packets) != 0 {
				completeFrames++
			}
		}, nil)
		if track.dropped != 0 || len(sequences) != 102 || completeFrames != 1 {
			t.Fatalf("timely repair overflow: dropped=%d emitted=%d complete=%d", track.dropped, len(sequences), completeFrames)
		}
		for i, sequence := range sequences {
			if int(sequence) != i+1 {
				t.Fatalf("sequence %d at position %d", sequence, i)
			}
		}
	})
}

func TestVideoReorderDoesNotPollContiguousOrIdleSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		track := &repairPollingTrack{initial: []*rtp.Packet{repairPollingFragment(1), repairPollingFragment(2)}, repairs: make(chan *rtp.Packet, 50), closed: make(chan struct{})}
		go func() { time.Sleep(time.Second); close(track.closed) }()
		readOrderedVideo(track, func() bool { return true }, func() string { return pion.MimeTypeH264 }, func(*rtp.Packet, string) {}, nil)
		if track.gapDeadlines != 0 || track.timeouts != 0 {
			t.Fatalf("contiguous/idle source acquired polling: deadlines=%d timeouts=%d", track.gapDeadlines, track.timeouts)
		}
	})
}

func TestVideoReorderPollDoesNotExtendOrShortenGapWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		track := &repairPollingTrack{initial: []*rtp.Packet{repairPollingFragment(1), repairPollingFragment(3)}, repairs: make(chan *rtp.Packet, 50), closed: make(chan struct{})}
		started := time.Now()
		delivered := 0
		readOrderedVideo(track, func() bool { return delivered < 2 }, func() string { return pion.MimeTypeH264 }, func(*rtp.Packet, string) { delivered++ }, nil)
		if elapsed := time.Since(started); elapsed != videoReorderWait {
			t.Fatalf("poll changed original gap deadline: %v", elapsed)
		}
		if track.gapDeadlines != int(videoReorderWait/videoRepairPoll) {
			t.Fatalf("poll loop was unbounded or stopped servicing repairs: %d", track.gapDeadlines)
		}
	})
}

type floodingRepairTrack struct {
	repairPollingTrack
	reads       int
	repairsRead int
	timedOut    bool
}

func (r *floodingRepairTrack) ReadRTP() (*rtp.Packet, interceptor.Attributes, error) {
	r.reads++
	if r.reads <= 2 {
		return repairPollingFragment(uint16(2*r.reads - 1)), nil, nil
	}
	if !r.timedOut {
		time.Sleep(max(0, time.Until(r.deadline)))
		r.timedOut = true
		return nil, nil, os.ErrDeadlineExceeded
	}
	if r.gapDeadlines > 1 {
		return nil, nil, io.EOF
	}
	r.repairsRead++
	return repairPollingFragment(1), nil, nil // endless obsolete repairs
}

func TestVideoReorderPollBoundsAdversarialRepairDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		track := &floodingRepairTrack{}
		readOrderedVideo(track, func() bool { return true }, func() string { return pion.MimeTypeH264 }, func(*rtp.Packet, string) {}, nil)
		if track.repairsRead != videoReorderPackets || track.gapDeadlines != 2 {
			t.Fatalf("repair flood escaped bounded drain: repairs=%d deadlines=%d", track.repairsRead, track.gapDeadlines)
		}
	})
}
