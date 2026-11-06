// Package botvoice is the media half of a voice bot (815): it reads Opus
// packets from an Ogg/Opus stream and paces them as RTP for a WebRTC audio
// track, the way cmd/loadtest publishes its synthetic microphone.
//
// It does not join channels by itself, and it refuses servers that require
// channel media end-to-end encryption (noxa-media-e2ee-v1). There, every
// frame must be encrypted with per-slot epoch keys that members distribute to
// each other's enrolled devices over Olm ratchet controls, so a bot would
// need a full noXa device: device enrollment, Olm admission, key controls and
// the FrameCryptor frame format. Until such a device exists for bots, use
// botvoice only against servers without that capability (development and
// test servers), and call CheckServer with the capabilities from sign-in.
package botvoice

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/pion/rtp"

	"noxa/internal/netproto"
)

// ErrMediaE2EERequired reports a server whose channel media must be
// end-to-end encrypted, which this package cannot do.
var ErrMediaE2EERequired = errors.New("botvoice: server requires end-to-end encrypted channel media; bots cannot publish there yet")

// CheckServer refuses servers that advertise mandatory media encryption.
func CheckServer(capabilities []string) error {
	if slices.Contains(capabilities, netproto.CapabilityMediaE2EE) {
		return ErrMediaE2EERequired
	}
	return nil
}

// SampleRate is the Opus RTP clock rate.
const SampleRate = 48000

// Packet is one Opus packet and its duration in 48 kHz samples.
type Packet struct {
	Data    []byte
	Samples uint32
}

// maxOggPacket bounds one reassembled packet (an Opus packet is at most
// 120 ms, far below this).
const maxOggPacket = 64 << 10

// OggOpusReader yields the Opus packets of a single-stream Ogg/Opus file.
type OggOpusReader struct {
	r       io.Reader
	pending []Packet
	partial []byte
	serial  uint32
	pages   int
	preSkip uint32
}

// NewOggOpusReader validates the OpusHead and OpusTags headers.
func NewOggOpusReader(r io.Reader) (*OggOpusReader, error) {
	o := &OggOpusReader{r: r}
	head, err := o.nextRaw()
	if err != nil {
		return nil, fmt.Errorf("botvoice: reading OpusHead: %w", err)
	}
	if len(head) < 19 || string(head[:8]) != "OpusHead" || head[8] != 1 {
		return nil, errors.New("botvoice: not an Ogg/Opus stream")
	}
	if head[9] == 0 || head[9] > 2 {
		return nil, errors.New("botvoice: only mono or stereo Opus is supported")
	}
	o.preSkip = uint32(binary.LittleEndian.Uint16(head[10:12]))
	tags, err := o.nextRaw()
	if err != nil {
		return nil, fmt.Errorf("botvoice: reading OpusTags: %w", err)
	}
	if len(tags) < 8 || string(tags[:8]) != "OpusTags" {
		return nil, errors.New("botvoice: missing OpusTags header")
	}
	return o, nil
}

// PreSkip is the number of decoder samples to discard at the start.
func (o *OggOpusReader) PreSkip() uint32 { return o.preSkip }

// Next returns the next audio packet, or io.EOF at the end of the stream.
func (o *OggOpusReader) Next() (Packet, error) {
	for {
		data, err := o.nextRaw()
		if err != nil {
			return Packet{}, err
		}
		samples, err := OpusSamples(data)
		if err != nil {
			return Packet{}, err
		}
		if samples == 0 {
			continue // DTX/empty packet carries no audio
		}
		return Packet{Data: data, Samples: samples}, nil
	}
}

// nextRaw returns the next reassembled Ogg packet.
func (o *OggOpusReader) nextRaw() ([]byte, error) {
	for len(o.pending) == 0 {
		if err := o.readPage(); err != nil {
			if errors.Is(err, io.EOF) && len(o.partial) > 0 {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
	}
	p := o.pending[0]
	o.pending = o.pending[1:]
	return p.Data, nil
}

func (o *OggOpusReader) readPage() error {
	var header [27]byte
	if _, err := io.ReadFull(o.r, header[:]); err != nil {
		return err // io.EOF only at a clean page boundary
	}
	if string(header[:4]) != "OggS" || header[4] != 0 {
		return errors.New("botvoice: invalid Ogg page")
	}
	serial := binary.LittleEndian.Uint32(header[14:18])
	if o.pages == 0 {
		o.serial = serial
	} else if serial != o.serial {
		return errors.New("botvoice: multiplexed Ogg streams are not supported")
	}
	o.pages++
	continued := header[5]&0x01 != 0
	if continued != (len(o.partial) > 0) {
		return errors.New("botvoice: broken Ogg packet continuation")
	}
	segments := make([]byte, header[26])
	if _, err := io.ReadFull(o.r, segments); err != nil {
		return noEOF(err)
	}
	for _, size := range segments {
		chunk := make([]byte, size)
		if _, err := io.ReadFull(o.r, chunk); err != nil {
			return noEOF(err)
		}
		o.partial = append(o.partial, chunk...)
		if len(o.partial) > maxOggPacket {
			return errors.New("botvoice: Ogg packet too large")
		}
		if size < 255 { // a lacing value below 255 ends the packet
			o.pending = append(o.pending, Packet{Data: o.partial})
			o.partial = nil
		}
	}
	return nil
}

func noEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// OpusSamples returns a packet's duration in 48 kHz samples from its TOC
// byte and frame count (RFC 6716, section 3.1). An empty packet is 0.
func OpusSamples(packet []byte) (uint32, error) {
	if len(packet) == 0 {
		return 0, nil
	}
	toc := packet[0]
	config := toc >> 3
	var frame uint32 // samples per frame at 48 kHz
	switch {
	case config < 12: // SILK: 10, 20, 40, 60 ms
		frame = []uint32{480, 960, 1920, 2880}[config&3]
	case config < 16: // Hybrid: 10, 20 ms
		frame = []uint32{480, 960}[config&1]
	default: // CELT: 2.5, 5, 10, 20 ms
		frame = []uint32{120, 240, 480, 960}[config&3]
	}
	var frames uint32
	switch toc & 3 {
	case 0:
		frames = 1
	case 1, 2:
		frames = 2
	default:
		if len(packet) < 2 {
			return 0, errors.New("botvoice: truncated Opus packet")
		}
		frames = uint32(packet[1] & 0x3f)
		if frames == 0 {
			return 0, errors.New("botvoice: Opus packet without frames")
		}
	}
	total := frame * frames
	if total > 5760 { // 120 ms limit
		return 0, errors.New("botvoice: Opus packet longer than 120 ms")
	}
	return total, nil
}

// PacketSource yields Opus packets until io.EOF.
type PacketSource interface {
	Next() (Packet, error)
}

// RTPWriter is satisfied by a pion TrackLocalStaticRTP.
type RTPWriter interface {
	WriteRTP(*rtp.Packet) error
}

// Stream writes every packet of src to w as RTP, paced in real time from the
// first packet. sequence, timestamp and ssrc are the starting identifiers
// (pick them at random). It returns nil at the end of src, or the first
// error, including ctx's.
func Stream(ctx context.Context, src PacketSource, w RTPWriter, sequence uint16, timestamp, ssrc uint32) error {
	start := time.Now()
	var elapsed uint64 // samples already scheduled
	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C
	for first := true; ; first = false {
		packet, err := src.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		due := start.Add(time.Duration(elapsed) * time.Second / SampleRate)
		if wait := time.Until(due); wait > 0 {
			timer.Reset(wait)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		} else if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.WriteRTP(&rtp.Packet{
			Header:  rtp.Header{Version: 2, Marker: first, PayloadType: 111, SequenceNumber: sequence, Timestamp: timestamp, SSRC: ssrc},
			Payload: packet.Data,
		}); err != nil {
			return err
		}
		sequence++
		timestamp += packet.Samples
		elapsed += uint64(packet.Samples)
	}
}
