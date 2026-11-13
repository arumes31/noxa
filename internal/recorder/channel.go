package recorder

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"go.uber.org/zap"

	"noxa/internal/webrtc"
)

const captureQueueBytes = 2 * 1024 * 1024

// captureIdleTimeout ends a video source that sent nothing for this long; the
// next keyframe starts a new segment. Tests shorten it.
var captureIdleTimeout = 10 * time.Second

// ChannelRecorder coordinates separate participant/slot files. The underlying
// Recorder retains ownership of every subprocess through complete cleanup;
// its global MaxConcurrent limit applies to processes, not just channels.
type ChannelRecorder struct {
	processes *Recorder
	pruneMu   sync.Mutex
	pruneScan *recordingPruneScan
	mu        sync.Mutex
	sessions  map[int64]*channelCapture
	closed    bool
	next      atomic.Int64
}

func NewChannelRecorder(cfg Config, logger *zap.Logger, observers ...Observers) *ChannelRecorder {
	return &ChannelRecorder{processes: New(cfg, logger, observers...), sessions: make(map[int64]*channelCapture)}
}

type captureManifest struct {
	Version   int             `json:"version"`
	ChannelID int64           `json:"channel_id"`
	StartedAt time.Time       `json:"started_at"`
	EndedAt   *time.Time      `json:"ended_at,omitempty"`
	Alignment string          `json:"alignment"`
	Tracks    []*captureTrack `json:"tracks"`
	// Notes lists sources that were not recorded, e.g. at the server-wide
	// process limit.
	Notes []string `json:"notes,omitempty"`
	Error string   `json:"error,omitempty"`
}
type captureTrack struct {
	Publisher string `json:"publisher"`
	Slot      string `json:"slot"`
	SSRC      uint32 `json:"ssrc"`
	Epoch     uint64 `json:"source_epoch"`
	Codec     string `json:"codec"`
	File      string `json:"file"`
	StartMS   int64  `json:"start_ms"`
	EndMS     int64  `json:"end_ms,omitempty"`
}
type capturePacket struct {
	packet   *rtp.Packet
	delivery webrtc.MediaDelivery
	commit   webrtc.MediaCommit
	at       time.Time
	size     int
}
type captureSource struct{ publisher, slot string }

// captureStream identifies one stream of a source; a new SSRC or epoch is a
// new stream.
type captureStream struct {
	ssrc  uint32
	epoch uint64
}
type captureChild struct {
	id                         int64
	session                    *Session
	track                      *captureTrack
	video, started             bool
	readyAt, lastSeen, lastPLI time.Time
}
type channelCapture struct {
	owner        *ChannelRecorder
	channelID    int64
	router       TapRouter
	tapID        string
	ctx          context.Context
	cancel       context.CancelFunc
	ready        chan error
	done         chan struct{}
	filePath     string
	manifestName string
	root         *os.Root
	started      time.Time
	result       error // published by closing done
	life         sync.RWMutex
	stopped      bool
	queueMu      sync.Mutex
	queued       int
	queue        chan capturePacket
	children     map[captureSource]*captureChild // worker-owned
	skipped      map[captureSource]captureStream // worker-owned: streams refused at capacity
	ownedMu      sync.Mutex
	ownedIDs     map[int64]struct{}
	manifest     captureManifest // worker-owned
}

func (c *ChannelRecorder) Start(ctx context.Context, channelID int64, router TapRouter) (*Session, error) {
	if !c.processes.cfg.Enabled {
		return nil, ErrDisabled
	}
	if ctx == nil || channelID <= 0 || nilTapRouter(router) {
		return nil, ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.processes.cfg.validate(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	if c.sessions[channelID] != nil {
		c.mu.Unlock()
		return nil, ErrAlreadyRecording
	}
	if len(c.sessions) >= c.processes.cfg.MaxConcurrent {
		c.mu.Unlock()
		return nil, ErrCapacity
	}
	work, cancel := context.WithCancel(context.Background())
	// #nosec G115 -- generation is an opaque counter bit pattern, not a signed quantity.
	s := &channelCapture{owner: c, channelID: channelID, router: router, tapID: tapID(channelID, uint64(c.next.Add(1))), ctx: work, cancel: cancel, ready: make(chan error, 1), done: make(chan struct{}), queue: make(chan capturePacket, 512), children: make(map[captureSource]*captureChild), ownedIDs: make(map[int64]struct{})}
	c.sessions[channelID] = s
	c.mu.Unlock()
	stopCancellation := context.AfterFunc(ctx, cancel)
	go s.run(stopCancellation)
	timer := time.NewTimer(defaultStopGracePeriod + defaultKillWait)
	defer timer.Stop()
	select {
	case err := <-s.ready:
		if err != nil {
			return nil, err
		}
		return &Session{ChannelID: channelID, FilePath: s.filePath, StartedAt: s.started, done: s.done}, nil
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	case <-timer.C:
		cancel()
		return nil, ErrStartupCleanupTimeout
	}
}

func (c *ChannelRecorder) Stop(channelID int64) error {
	return c.StopContext(context.Background(), channelID)
}
func (c *ChannelRecorder) StopContext(ctx context.Context, channelID int64) error {
	c.mu.Lock()
	s := c.sessions[channelID]
	c.mu.Unlock()
	if s == nil {
		return ErrNotRecording
	}
	s.stop()
	timer := time.NewTimer(defaultStopGracePeriod + defaultKillWait)
	defer timer.Stop()
	select {
	case <-s.done:
		return s.result
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ErrStopTimeout
	}
}
func (s *channelCapture) stop() {
	s.cancel()
	s.life.Lock()
	s.stopped = true
	s.life.Unlock()
	// Published child processes intentionally outlive their startup context.
	// Stop every owned child independently, even if the worker is waiting for
	// a different child's wedged startup or epoch cleanup.
	s.ownedMu.Lock()
	ids := make([]int64, 0, len(s.ownedIDs))
	for id := range s.ownedIDs {
		ids = append(ids, id)
	}
	s.ownedMu.Unlock()
	for _, id := range ids {
		s.owner.processes.mu.Lock()
		pending, active := s.owner.processes.starting[id], s.owner.processes.sessions[id]
		s.owner.processes.mu.Unlock()
		if pending != nil {
			pending.cancel()
		}
		if active != nil {
			// life.Lock above drained committed writes. Empty inputs have no
			// output to finalize, even if their worker is still in startup.
			if active.audioTap.lastSource.Load() == 0 && active.videoTap.tap.lastSource.Load() == 0 {
				active.discardEmpty.Store(true)
			}
			active.requestStop()
		}
	}
}
func (c *ChannelRecorder) SessionCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sessions)
}
func (c *ChannelRecorder) Close() error {
	c.closePruneScan()
	c.mu.Lock()
	c.closed = true
	sessions := make([]*channelCapture, 0, len(c.sessions))
	for _, s := range c.sessions {
		sessions = append(sessions, s)
	}
	c.mu.Unlock()
	for _, s := range sessions {
		s.stop()
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultStopGracePeriod+defaultKillWait)
	defer cancel()
	var result error
	for _, s := range sessions {
		select {
		case <-s.done:
			result = errors.Join(result, s.result)
		case <-ctx.Done():
			return errors.Join(result, ErrStopTimeout)
		}
	}
	return errors.Join(result, c.processes.Close())
}

func (*channelCapture) WriteRTP(*rtp.Packet) error {
	return errors.New("recording requires scoped media")
}
func (s *channelCapture) WriteMedia(packet *rtp.Packet, delivery webrtc.MediaDelivery, commit webrtc.MediaCommit) error {
	if packet == nil || commit == nil {
		return ErrInvalidPacket
	}
	if s.ctx.Err() != nil {
		return ErrClosed
	}
	size := packet.MarshalSize()
	if size > 65535 {
		return ErrInvalidPacket
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if s.queued+size > captureQueueBytes {
		return nil
	}
	entry := capturePacket{packet.Clone(), delivery, commit, time.Now(), size}
	select {
	case s.queue <- entry:
		s.queued += size
	default:
	}
	return nil
}

func (s *channelCapture) run(stopStartupCancellation func() bool) {
	var root *os.Root
	var file *os.File
	registered := false
	ready := false
	defer func() {
		stopStartupCancellation()
		s.stop()
		removed := make(chan struct{})
		go func() {
			defer close(removed)
			if registered {
				s.router.RemoveTap(s.tapID)
			}
		}()
		results := make(chan error, len(s.children))
		for _, child := range s.children {
			go func() { results <- s.finishChild(child) }()
		}
		for range len(s.children) {
			s.result = errors.Join(s.result, <-results)
		}
		<-removed
		if file != nil {
			ended := time.Now().UTC()
			s.manifest.EndedAt = &ended
			if s.result != nil {
				s.manifest.Error = s.result.Error()
			}
			s.result = errors.Join(s.result, s.writeManifest())
		}
		if root != nil {
			s.result = errors.Join(s.result, root.Close())
		}
		if s.result != nil {
			s.owner.processes.reportError("source")
			s.owner.processes.logger.Warn("channel recording failed", zap.Int64("channel_id", s.channelID), zap.Error(s.result))
		}
		if !ready {
			s.ready <- s.result
		}
		s.owner.mu.Lock()
		delete(s.owner.sessions, s.channelID)
		s.owner.mu.Unlock()
		close(s.done)
	}()
	path, err := filepath.Abs(s.owner.processes.cfg.Dir)
	if err != nil {
		s.result = err
		return
	}
	root, err = openRecordingRoot(path)
	if err != nil {
		s.result = err
		return
	}
	s.root = root
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		s.result = err
		return
	}
	name := fmt.Sprintf("channel-%d-%s.json", s.channelID, hex.EncodeToString(nonce[:]))
	file, err = root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		s.result = err
		return
	}
	s.manifestName = name
	if err = file.Close(); err != nil {
		s.result = err
		return
	}
	s.filePath = filepath.Join(path, name)
	s.started = time.Now().UTC()
	s.manifest = captureManifest{Version: 1, ChannelID: s.channelID, StartedAt: s.started, Alignment: "server arrival offsets; approximate cross-source alignment", Tracks: []*captureTrack{}}
	if err = s.writeManifest(); err != nil {
		s.result = err
		return
	}
	if err = s.ctx.Err(); err != nil {
		s.result = err
		return
	}
	s.router.AddTap(s.channelID, s.tapID, s, s)
	registered = true
	if !stopStartupCancellation() || s.ctx.Err() != nil {
		s.result = context.Canceled
		return
	}
	ready = true
	s.ready <- nil
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.ctx.Err() != nil {
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case entry := <-s.queue:
			s.queueMu.Lock()
			s.queued -= entry.size
			s.queueMu.Unlock()
			if time.Since(entry.at) > time.Second {
				continue
			}
			if err = s.consume(entry); err != nil {
				s.result = err
				return
			}
		case now := <-ticker.C:
			for source, child := range s.children {
				select {
				case <-child.session.done:
					s.result = errors.Join(errors.New("recording source process exited unexpectedly"), child.session.result())
					return
				default:
				}
				// Audio is silent while muted; a new process per unmute would
				// add segments until the 1024 cap ends the recording.
				if child.video && now.Sub(child.lastSeen) > captureIdleTimeout {
					s.result = s.finishChild(child)
					delete(s.children, source)
					if s.result != nil {
						return
					}
					continue
				}
				if child.video && now.After(child.readyAt) && now.Sub(child.lastPLI) > 2*time.Second {
					s.requestKeyframe(child)
					child.lastPLI = now
				}
			}
		}
	}
}

func (s *channelCapture) writeManifest() (retErr error) {
	manifest := s.manifest
	manifest.Tracks = make([]*captureTrack, 0, len(s.manifest.Tracks))
	for _, track := range s.manifest.Tracks {
		if track.File != "" {
			manifest.Tracks = append(manifest.Tracks, track)
		}
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	temporary := s.manifestName + ".tmp"
	file, err := s.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err := s.root.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, err)
		}
	}()
	if _, err = file.Write(append(raw, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err = errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	return s.root.Rename(temporary, s.manifestName)
}

// A child router captures the owned process's direct UDP writers. The channel
// dispatcher is the only registered tap, and authorization is checked after
// its queue, immediately around the bounded UDP write (no second buffer).
type captureChildRouter struct{}

func (*captureChildRouter) AddTap(int64, string, webrtc.TrackWriter, webrtc.TrackWriter) {}
func (*captureChildRouter) RemoveTap(string)                                             {}

func (s *channelCapture) consume(entry capturePacket) error {
	allowed := false
	if err := entry.commit(func() error { allowed = true; return nil }); err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	d := entry.delivery
	video := d.Slot == webrtc.SlotCam || d.Slot == webrtc.SlotScreen
	if video && d.Codec != "video/VP8" {
		return fmt.Errorf("recording codec %q is unsupported", d.Codec)
	}
	if !video && d.Slot != webrtc.SlotMic && d.Slot != webrtc.SlotScreenAudio {
		return ErrInvalidPacket
	}
	source := captureSource{d.SenderID, d.Slot}
	child := s.children[source]
	if child != nil && (child.track.SSRC != entry.packet.SSRC || child.track.Epoch != d.SourceEpoch) {
		if err := s.finishChild(child); err != nil {
			return err
		}
		delete(s.children, source)
		child = nil
	}
	if skip, ok := s.skipped[source]; ok && skip == (captureStream{entry.packet.SSRC, d.SourceEpoch}) {
		return nil
	}
	if child == nil {
		if len(s.manifest.Tracks) >= 1024 {
			return errors.New("recording segment limit reached")
		}
		kind, codec := "audio", "opus"
		if video {
			kind, codec = "video", "VP8"
		}
		id := s.owner.next.Add(1)
		s.ownedMu.Lock()
		s.ownedIDs[id] = struct{}{}
		s.ownedMu.Unlock()
		track := &captureTrack{Publisher: d.SenderID, Slot: d.Slot, SSRC: entry.packet.SSRC, Epoch: d.SourceEpoch, Codec: codec, StartMS: entry.at.Sub(s.started).Milliseconds()}
		// List the files before ffmpeg can write them, so a crash or a failed
		// manifest write later cannot leave media retention never deletes.
		process, err := s.owner.processes.start(s.ctx, id, &captureChildRouter{}, kind, s.root, func(outputName string) error {
			track.File = outputName
			s.manifest.Tracks = append(s.manifest.Tracks, track)
			return s.writeManifest()
		})
		if err != nil {
			track.File = "" // start removed the files; writeManifest omits the track
		}
		if errors.Is(err, ErrCapacity) {
			// The limit counts recorded sources server-wide. Skip this stream
			// and keep recording the others; a new stream of the source retries.
			s.ownedMu.Lock()
			delete(s.ownedIDs, id)
			s.ownedMu.Unlock()
			if s.skipped == nil {
				s.skipped = make(map[captureSource]captureStream)
			}
			s.skipped[source] = captureStream{entry.packet.SSRC, d.SourceEpoch}
			s.owner.processes.logger.Warn("recording source skipped: concurrent recording limit reached", zap.Int64("channel_id", s.channelID), zap.String("publisher", d.SenderID), zap.String("slot", d.Slot))
			s.manifest.Notes = append(s.manifest.Notes, fmt.Sprintf("%s %s from %dms not recorded: concurrent recording limit reached", d.SenderID, d.Slot, entry.at.Sub(s.started).Milliseconds()))
			return s.writeManifest()
		}
		if err != nil {
			s.stop()
			// Failed startup may still own a process or a wedged collaborator.
			// Preserve this channel reservation until that exact child is reaped.
			s.owner.processes.mu.Lock()
			pending, active := s.owner.processes.starting[id], s.owner.processes.sessions[id]
			s.owner.processes.mu.Unlock()
			if pending != nil {
				pending.cancel()
				<-pending.done
			}
			if active != nil {
				_ = s.owner.processes.stopSession(active)
				<-active.done
			}
			s.ownedMu.Lock()
			delete(s.ownedIDs, id)
			s.ownedMu.Unlock()
			return fmt.Errorf("recording %s source: %w", d.Slot, err)
		}
		child = &captureChild{id: id, session: process, track: track, video: video, readyAt: time.Now().Add(250 * time.Millisecond), lastSeen: entry.at}
		s.children[source] = child
	}
	child.lastSeen = entry.at
	if time.Now().Before(child.readyAt) {
		return nil
	}
	if video && !child.started {
		var descriptor codecs.VP8Packet
		payload, err := descriptor.Unmarshal(entry.packet.Payload)
		if err != nil || descriptor.S != 1 || descriptor.PID != 0 || len(payload) < 10 || payload[0]&1 != 0 || payload[3] != 0x9d || payload[4] != 1 || payload[5] != 0x2a {
			return nil
		}
	}
	packet := *entry.packet
	packet.Extension = false
	packet.Extensions = nil
	packet.CSRC = nil
	writer := child.session.audioTap
	packet.PayloadType = 111
	if video {
		writer = child.session.videoTap.tap
		packet.PayloadType = 96
	}
	s.life.RLock()
	defer s.life.RUnlock()
	if s.stopped || s.ctx.Err() != nil || time.Since(entry.at) > time.Second {
		return nil
	}
	return entry.commit(func() error {
		if err := writer.WriteRTP(&packet); err != nil {
			return err
		}
		if !child.started {
			child.started = true
			child.track.StartMS = entry.at.Sub(s.started).Milliseconds()
		}
		return nil
	})
}

func (s *channelCapture) requestKeyframe(child *captureChild) {
	if requester, ok := s.router.(interface{ RequestSourceKeyframe(string, string, uint32) }); ok {
		requester.RequestSourceKeyframe(child.track.Publisher, child.track.Slot, child.track.SSRC)
	}
}
func (s *channelCapture) finishChild(child *captureChild) error {
	// No media was committed before startup ended or a keyframe arrived.
	// There is no container to finalize; reap the empty process and omit it.
	if !child.started {
		child.session.discardEmpty.Store(true)
	}
	err := s.owner.processes.stopSession(child.session)
	if err != nil {
		s.stop()
	}
	// A timed-out child remains owned by the process recorder. Keep this
	// channel reserved too until its final cleanup really completes.
	<-child.session.done
	s.ownedMu.Lock()
	delete(s.ownedIDs, child.id)
	s.ownedMu.Unlock()
	child.track.EndMS = child.lastSeen.Sub(s.started).Milliseconds()
	if !child.started && err == nil {
		child.track.File = ""
	}
	return err
}
