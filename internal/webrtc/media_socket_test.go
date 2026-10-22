package webrtc

import (
	"errors"
	"net"
	"noxa/internal/traffic"
	"os"
	"testing"
	"time"
)

func TestMediaSocketWriteDeadlineCannotBeExtended(t *testing.T) {
	writer, reader := net.Pipe()
	defer func() { _ = writer.Close(); _ = reader.Close() }()
	var gate socketWriteGate
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := gate.write(writer.SetWriteDeadline, func() (int, error) { close(started); return writer.Write([]byte("blocked")) })
		done <- err
	}()
	<-started
	if err := gate.setDeadline(time.Now().Add(time.Hour), writer.SetWriteDeadline); err != nil {
		t.Fatal(err)
	}
	if err := gate.setDeadline(time.Time{}, writer.SetWriteDeadline); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("write: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stalled socket outlived its media deadline")
	}
}

func TestMediaSocketWaitConsumesWriteBudget(t *testing.T) {
	var gate socketWriteGate
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = gate.write(func(time.Time) error { return nil }, func() (int, error) { close(entered); <-release; return 0, nil })
	}()
	<-entered
	_, err := gate.write(func(time.Time) error { t.Error("second writer acquired occupied socket"); return nil }, func() (int, error) { t.Error("second writer ran"); return 0, nil })
	close(release)
	<-done
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("waiting writer: %v", err)
	}
}

func TestMeteredSharedMediaSocketPreservesVoiceWrites(t *testing.T) {
	raw, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	peer, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	counter := &traffic.Counter{}
	socket := &boundedPacketConn{PacketConn: raw, traffic: counter}
	for _, packet := range [][]byte{{0x80, 111, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, []byte("video")} {
		if _, err := socket.WriteTo(packet, peer.LocalAddr()); err != nil {
			t.Fatal(err)
		}
		if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if n, _, err := peer.ReadFrom(make([]byte, 100)); err != nil || n != len(packet) {
			t.Fatal(n, err)
		}
	}
	if _, err := peer.WriteTo([]byte("incoming"), raw.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	if err := socket.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := socket.ReadFrom(make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	if counter.Snapshot() != (traffic.Counts{In: 8, Out: 17}) {
		t.Fatal(counter.Snapshot())
	}
}
