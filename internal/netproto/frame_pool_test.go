package netproto

import (
	"bytes"
	"errors"
	"testing"
)

func encodedFrame(t testing.TB, typ uint16, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteFrame(&buf, &Frame{Type: typ, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReadFrameLimitPooledMatchesReadFrame(t *testing.T) {
	for _, size := range []int{0, 1, 4<<10 - 2, 4 << 10, 64<<10 - 2, 64<<10 + 5, MaxPayloadSize} {
		payload := bytes.Repeat([]byte{byte(size)}, size)
		f, err := ReadFrameLimitPooled(bytes.NewReader(encodedFrame(t, 7, payload)), MaxFrameSize)
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if f.Type != 7 || !bytes.Equal(f.Payload, payload) {
			t.Fatalf("size %d: frame mismatch", size)
		}
		f.Release()
		f.Release() // a second release is a no-op
		if f.Payload != nil {
			t.Fatal("released frame keeps its payload")
		}
	}
}

func TestReadFrameLimitPooledLimitsAndErrors(t *testing.T) {
	big := encodedFrame(t, 1, make([]byte, 100))
	if _, err := ReadFrameLimitPooled(bytes.NewReader(big), 50); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("limit not enforced: %v", err)
	}
	short := []byte{0, 0, 0, 1, 9}
	if _, err := ReadFrameLimitPooled(bytes.NewReader(short), MaxFrameSize); !errors.Is(err, ErrInvalidFrameLength) {
		t.Fatalf("short frame: %v", err)
	}
	truncated := big[:20]
	if _, err := ReadFrameLimitPooled(bytes.NewReader(truncated), MaxFrameSize); err == nil {
		t.Fatal("truncated body accepted")
	}
}

func TestReleasedPooledFramesArePoisonedInTests(t *testing.T) {
	SetFramePoisoning(true)
	defer SetFramePoisoning(false)
	f, err := ReadFrameLimitPooled(bytes.NewReader(encodedFrame(t, 3, []byte(`{"a":1}`))), MaxFrameSize)
	if err != nil {
		t.Fatal(err)
	}
	payload := f.Payload
	f.Release()
	for _, b := range payload {
		if b != 0xA5 {
			t.Fatalf("released payload not poisoned: %q", payload)
		}
	}
}

func BenchmarkReadControlFrame(b *testing.B) {
	payload := bytes.Repeat([]byte("x"), 512)
	raw := encodedFrame(b, 5, payload)
	b.Run("alloc", func(b *testing.B) {
		b.ReportAllocs()
		r := bytes.NewReader(nil)
		for range b.N {
			r.Reset(raw)
			if _, err := ReadFrameLimit(r, MaxFrameSize); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("pooled", func(b *testing.B) {
		b.ReportAllocs()
		r := bytes.NewReader(nil)
		for range b.N {
			r.Reset(raw)
			f, err := ReadFrameLimitPooled(r, MaxFrameSize)
			if err != nil {
				b.Fatal(err)
			}
			f.Release()
		}
	})
}
