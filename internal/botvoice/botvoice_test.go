package botvoice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/pion/rtp"

	"noxa/internal/netproto"
)

// oggPage builds one Ogg page; segments are the lacing values.
func oggPage(continued bool, granule uint64, segments []byte, payload []byte) []byte {
	var b bytes.Buffer
	b.WriteString("OggS")
	b.WriteByte(0)
	flags := byte(0)
	if continued {
		flags |= 1
	}
	b.WriteByte(flags)
	_ = binary.Write(&b, binary.LittleEndian, granule)
	_ = binary.Write(&b, binary.LittleEndian, uint32(7)) // serial
	_ = binary.Write(&b, binary.LittleEndian, uint32(0)) // page index
	_ = binary.Write(&b, binary.LittleEndian, uint32(0)) // checksum (not verified)
	b.WriteByte(byte(len(segments)))
	b.Write(segments)
	b.Write(payload)
	return b.Bytes()
}

func lacing(size int) []byte {
	var out []byte
	for size >= 255 {
		out = append(out, 255)
		size -= 255
	}
	return append(out, byte(size))
}

func opusHead(channels byte) []byte {
	head := []byte("OpusHead")
	head = append(head, 1, channels, 0x38, 0x01) // version, channels, pre-skip 312
	head = append(head, 0x80, 0xbb, 0, 0)        // 48 kHz
	return append(head, 0, 0, 0)                 // gain, mapping family
}

func testStream(t *testing.T) []byte {
	t.Helper()
	var s bytes.Buffer
	head := opusHead(2)
	s.Write(oggPage(false, 0, lacing(len(head)), head))
	tags := []byte("OpusTags\x00\x00\x00\x00\x00\x00\x00\x00")
	s.Write(oggPage(false, 0, lacing(len(tags)), tags))
	// Page 3: a 20 ms CELT packet (config 31, code 0) and the first part of
	// a large 2-frame packet that continues on page 4.
	small := []byte{0xf8, 1, 2, 3}
	big := append([]byte{0xf9}, bytes.Repeat([]byte{9}, 299)...) // code 1: two 20 ms frames
	s.Write(oggPage(false, 960, append(lacing(len(small)), 255), append(small, big[:255]...)))
	s.Write(oggPage(true, 2880, lacing(len(big)-255), big[255:]))
	return s.Bytes()
}

func TestOggOpusReaderReassemblesPackets(t *testing.T) {
	r, err := NewOggOpusReader(bytes.NewReader(testStream(t)))
	if err != nil {
		t.Fatal(err)
	}
	if r.PreSkip() != 312 {
		t.Fatalf("pre-skip = %d", r.PreSkip())
	}
	first, err := r.Next()
	if err != nil || first.Samples != 960 || len(first.Data) != 4 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := r.Next()
	if err != nil || second.Samples != 1920 || len(second.Data) != 300 {
		t.Fatalf("spanning packet = %d bytes, %d samples, %v", len(second.Data), second.Samples, err)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("end = %v", err)
	}
}

func TestOggOpusReaderRejectsBadInput(t *testing.T) {
	if _, err := NewOggOpusReader(bytes.NewReader([]byte("not ogg at all, definitely not"))); err == nil {
		t.Fatal("garbage accepted")
	}
	head := opusHead(6)
	if _, err := NewOggOpusReader(bytes.NewReader(oggPage(false, 0, lacing(len(head)), head))); err == nil {
		t.Fatal("surround stream accepted")
	}
	stream := testStream(t)
	r, err := NewOggOpusReader(bytes.NewReader(stream[:len(stream)-10]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Next(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated packet = %v", err)
	}
}

func TestOpusSamples(t *testing.T) {
	for _, tc := range []struct {
		packet []byte
		want   uint32
	}{
		{[]byte{0x08}, 960},        // SILK NB 20 ms
		{[]byte{0x18}, 2880},       // SILK 60 ms
		{[]byte{0x78}, 960},        // Hybrid 20 ms
		{[]byte{0x80}, 120},        // CELT 2.5 ms
		{[]byte{0xf9}, 1920},       // CELT 20 ms, two frames
		{[]byte{0xfb, 0x03}, 2880}, // code 3, three 20 ms frames
		{[]byte{}, 0},              // DTX
	} {
		got, err := OpusSamples(tc.packet)
		if err != nil || got != tc.want {
			t.Errorf("% x: %d, %v; want %d", tc.packet, got, err, tc.want)
		}
	}
	for _, bad := range [][]byte{{0xfb}, {0xfb, 0x00}, {0x1b, 0x03}} {
		if _, err := OpusSamples(bad); err == nil {
			t.Errorf("% x accepted", bad)
		}
	}
}

type packets []Packet

func (p *packets) Next() (Packet, error) {
	if len(*p) == 0 {
		return Packet{}, io.EOF
	}
	next := (*p)[0]
	*p = (*p)[1:]
	return next, nil
}

type recorder struct {
	packets []*rtp.Packet
	at      []time.Time
}

func (r *recorder) WriteRTP(p *rtp.Packet) error {
	r.packets = append(r.packets, p)
	r.at = append(r.at, time.Now())
	return nil
}

func TestStreamPacesRTP(t *testing.T) {
	src := packets{{Data: []byte{1}, Samples: 960}, {Data: []byte{2}, Samples: 960}, {Data: []byte{3}, Samples: 960}}
	var w recorder
	if err := Stream(t.Context(), &src, &w, 65535, 100, 42); err != nil {
		t.Fatal(err)
	}
	if len(w.packets) != 3 {
		t.Fatalf("wrote %d packets", len(w.packets))
	}
	if !w.packets[0].Marker || w.packets[1].Marker || w.packets[1].SequenceNumber != 0 || w.packets[2].Timestamp != 100+1920 || w.packets[2].SSRC != 42 {
		t.Fatalf("RTP headers: %+v", []rtp.Header{w.packets[0].Header, w.packets[1].Header, w.packets[2].Header})
	}
	if gap := w.at[2].Sub(w.at[0]); gap < 35*time.Millisecond {
		t.Fatalf("40 ms of audio sent in %v", gap)
	}
}

func TestStreamStopsOnCancel(t *testing.T) {
	src := packets{{Data: []byte{1}, Samples: 5760}, {Data: []byte{2}, Samples: 5760}}
	ctx, cancel := context.WithCancel(t.Context())
	var w recorder
	done := make(chan error, 1)
	go func() { done <- Stream(ctx, &src, &w, 0, 0, 1) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream = %v", err)
	}
}

func TestCheckServer(t *testing.T) {
	if err := CheckServer([]string{"x"}); err != nil {
		t.Fatal(err)
	}
	if err := CheckServer([]string{netproto.CapabilityMediaE2EE}); !errors.Is(err, ErrMediaE2EERequired) {
		t.Fatalf("E2EE server accepted: %v", err)
	}
}
