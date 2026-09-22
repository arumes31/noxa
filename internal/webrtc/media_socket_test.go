package webrtc

import (
	"errors"
	"net"
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
