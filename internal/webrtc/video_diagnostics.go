package webrtc

import (
	"sort"
	"sync"
	"time"

	"github.com/pion/rtp"
	"noxa/internal/netproto"
)

const videoDiagnosticBucketDuration = 100 * time.Millisecond
const videoDiagnosticBuckets = 20

type videoDiagnosticBucket struct {
	tick          int64
	bytes, frames uint64
}

// A fixed two-second window counts distinct RTP timestamps, not decodable
// frames. Repeated/reordered packets and padding cannot inflate the FPS rate.
type videoStageCounter struct {
	started, last                   time.Time
	packets, bytes, frames, markers uint64
	timestamps                      map[uint32]bool
	timestampOrder                  [512]uint32
	timestampCount, timestampNext   int
	buckets                         [videoDiagnosticBuckets]videoDiagnosticBucket
}

func (d *videoStageCounter) observe(header *rtp.Header, size int, media bool, now time.Time) {
	if d.started.IsZero() {
		d.started = now
	}
	d.last = now
	d.packets++
	if size > 0 {
		d.bytes += uint64(size)
	}
	frame := media && !d.timestamps[header.Timestamp]
	if frame {
		if d.timestamps == nil {
			d.timestamps = make(map[uint32]bool)
		}
		if d.timestampCount == len(d.timestampOrder) {
			delete(d.timestamps, d.timestampOrder[d.timestampNext])
		} else {
			d.timestampCount++
		}
		d.timestampOrder[d.timestampNext] = header.Timestamp
		d.timestampNext = (d.timestampNext + 1) % len(d.timestampOrder)
		d.timestamps[header.Timestamp] = true
		d.frames++
	}
	if media && header.Marker {
		d.markers++
	}
	tick := max(0, int64(now.Sub(d.started)/videoDiagnosticBucketDuration))
	bucket := &d.buckets[tick%videoDiagnosticBuckets]
	if bucket.tick != tick {
		*bucket = videoDiagnosticBucket{tick: tick}
	}
	if size > 0 {
		bucket.bytes += uint64(size)
	}
	if frame {
		bucket.frames++
	}
}

func (d *videoStageCounter) snapshot(now time.Time) netproto.VideoStreamStageDiagnostics {
	result := netproto.VideoStreamStageDiagnostics{Packets: d.packets, Bytes: d.bytes, Frames: d.frames, Markers: d.markers, Stale: true}
	if d.last.IsZero() {
		return result
	}
	result.AgeMS = max(0, now.Sub(d.last).Milliseconds())
	result.Stale = now.Sub(d.last) >= 2*time.Second
	tick := max(0, int64(now.Sub(d.started)/videoDiagnosticBucketDuration))
	firstTick := max(0, tick-videoDiagnosticBuckets+1)
	span := now.Sub(d.started.Add(time.Duration(firstTick) * videoDiagnosticBucketDuration))
	result.SampleMS = max(0, span.Milliseconds())
	if span < 250*time.Millisecond {
		return result
	}
	var bytes, frames uint64
	for _, bucket := range d.buckets {
		if bucket.tick >= firstTick && bucket.tick <= tick {
			bytes += bucket.bytes
			frames += bucket.frames
		}
	}
	fps, bitrate := float64(frames)/span.Seconds(), float64(bytes)*8/span.Seconds()
	result.FPS, result.BitrateBPS = &fps, &bitrate
	return result
}

type videoIngressKey struct{ publisher, slot, rid string }

func diagnosticVideoRID(rid string) bool {
	return rid == "" || rid == "q" || rid == "h" || rid == "f"
}

type videoIngressDiagnostic struct {
	mu      sync.Mutex
	ssrc    uint32
	started time.Time
	stage   videoStageCounter
}

func (d *videoIngressDiagnostic) observe(packet *rtp.Packet, now time.Time) {
	d.mu.Lock()
	d.stage.observe(&packet.Header, packet.MarshalSize(), !isVideoPadding(packet) && len(packet.Payload) > 0, now)
	d.mu.Unlock()
}

type videoForwardDiagnostic struct {
	mu                      sync.Mutex
	sourceSSRC              uint32
	rid                     string
	publication, watchEpoch uint64
	latestTicket            uint64
	stage                   videoStageCounter
}

func (s *mediaEgressStream) observeVideo(header *rtp.Header, size int, ticket *mediaTicket, now time.Time) {
	if ticket.videoSource == 0 || (ticket.delivery.Slot != SlotCam && ticket.delivery.Slot != SlotScreen) {
		return
	}
	d := &s.videoDiagnostic
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sourceSSRC != ticket.videoSource || d.publication != ticket.delivery.Publication || d.watchEpoch != ticket.delivery.WatchEpoch {
		// A retransmission from a retired layer must not replace the current
		// layer's diagnostic binding after a simulcast switch.
		if header.SSRC != s.outputSSRC.Load() || ticket.id < d.latestTicket {
			return
		}
		d.sourceSSRC, d.rid = ticket.videoSource, ticket.videoRID
		d.publication, d.watchEpoch = ticket.delivery.Publication, ticket.delivery.WatchEpoch
		d.stage = videoStageCounter{}
	}
	d.latestTicket = max(d.latestTicket, ticket.id)
	d.stage.observe(header, size, ticket.videoMedia, now)
}

// StreamDiagnostics is available to the same current channel member who may
// watch this exact publication. Active watching is needed only for forwarding
// details, never for inspecting a source before choosing to watch it.
func (r *Router) StreamDiagnostics(subscriber, publisher, slot string, generation, session uint64) (*netproto.VideoStreamDiagnostics, error) {
	if (slot != SlotCam && slot != SlotScreen) || generation == 0 || session == 0 {
		return nil, ErrVideoWatch
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	channel := r.clientChan[subscriber]
	if channel <= 0 || channel != r.clientChan[publisher] || !r.publisherAllowedLocked(subscriber, publisher) {
		return nil, ErrVideoWatch
	}
	r.watchMu.RLock()
	defer r.watchMu.RUnlock()
	if r.watchSessions[subscriber] != session || r.publications[publicationKey{publisher, slot}] != generation {
		return nil, ErrVideoWatch
	}
	now := time.Now()
	result := &netproto.VideoStreamDiagnostics{PublisherID: publisher, Slot: slot, Generation: generation, Session: session,
		SampledAt: now.UnixMilli(), Layers: []netproto.VideoStreamLayerDiagnostics{}}
	for key, input := range r.videoIngress {
		if key.publisher != publisher || key.slot != slot || r.videoSources[publisher][slot][key.rid] != input.ssrc {
			continue
		}
		input.mu.Lock()
		layer := netproto.VideoStreamLayerDiagnostics{RID: key.rid, SSRC: input.ssrc, StartedAt: input.started.UnixMilli(), Ingress: input.stage.snapshot(now)}
		input.mu.Unlock()
		result.Layers = append(result.Layers, layer)
	}
	sort.Slice(result.Layers, func(i, j int) bool { return result.Layers[i].RID < result.Layers[j].RID })
	watch := r.watches[watchKey{subscriber, publisher, slot}]
	if watch.active && watch.publication == generation && watch.session == session {
		if tracks := r.pubTracks[subscriber][publisher]; tracks != nil {
			if output := tracks.video[slot]; output != nil && output.egress != nil {
				d := &output.egress.videoDiagnostic
				d.mu.Lock()
				if d.publication == generation && d.watchEpoch == watch.epoch && !d.stage.last.IsZero() {
					result.Forwarding = &netproto.VideoStreamForwardDiagnostics{SourceSSRC: d.sourceSSRC, RID: d.rid,
						OutputSSRC: output.egress.outputSSRC.Load(), Stage: d.stage.snapshot(now)}
				}
				d.mu.Unlock()
			}
		}
	}
	return result, nil
}

func (v *Voice) StreamDiagnostics(subscriber, publisher, slot string, generation, session uint64) (*netproto.VideoStreamDiagnostics, error) {
	return v.router.StreamDiagnostics(subscriber, publisher, slot, generation, session)
}
