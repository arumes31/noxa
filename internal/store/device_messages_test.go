package store

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestDeviceEndorsementAndHistoryCopies(t *testing.T) {
	s := pollTestStore(t)
	ctx := t.Context()
	principal := fmt.Sprintf("dm-principal-%d", time.Now().UnixNano())
	first, err := s.AddDevice(ctx, newTestDevice(principal, 0), 10)
	if err != nil {
		t.Fatal(err)
	}
	phone, err := s.AddDevice(ctx, newTestDevice(principal, 1), 10)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.EndorseDevice(ctx, principal, first.DeviceID, phone.DeviceID, []byte("sig")); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("endorsing the first device err = %v", err)
	}
	endorsed, err := s.EndorseDevice(ctx, principal, phone.DeviceID, first.DeviceID, []byte("sig"))
	if err != nil || endorsed.ApprovedBy != first.DeviceID || string(endorsed.ApprovalSignature) != "sig" {
		t.Fatalf("endorse = %+v, %v", endorsed, err)
	}
	if _, err := s.EndorseDevice(ctx, principal, phone.DeviceID, first.DeviceID, []byte("again")); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("second endorsement err = %v", err)
	}

	put := func(seq, total int, data string, limit int64) error {
		return s.PutHistoryChunk(ctx, principal, HistoryChunk{DeviceID: phone.DeviceID, Seq: seq, Total: total, FromDeviceID: first.DeviceID, Data: []byte(data)}, limit)
	}
	if err := put(0, 2, "aaaa", 6); err != nil {
		t.Fatal(err)
	}
	if err := put(1, 2, "bbbb", 6); !errors.Is(err, ErrHistoryLimit) {
		t.Fatalf("over-limit chunk err = %v", err)
	}
	if err := put(1, 2, "bb", 6); err != nil {
		t.Fatal(err)
	}
	if err := s.PutHistoryChunk(ctx, "someone-else", HistoryChunk{DeviceID: phone.DeviceID, Seq: 0, Total: 1, Data: []byte("x")}, 6); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("chunk for another account's device err = %v", err)
	}
	chunks, err := s.HistoryChunks(ctx, principal, phone.DeviceID, 0, 10)
	if err != nil || len(chunks) != 2 || string(chunks[1].Data) != "bb" || chunks[0].FromDeviceID != first.DeviceID {
		t.Fatalf("chunks = %+v, %v", chunks, err)
	}
	if err := s.AckHistoryChunks(ctx, principal, phone.DeviceID, 0); err != nil {
		t.Fatal(err)
	}
	if chunks, _ := s.HistoryChunks(ctx, principal, phone.DeviceID, 0, 10); len(chunks) != 1 || chunks[0].Seq != 1 {
		t.Fatalf("after ack = %+v", chunks)
	}
	if n, err := s.ExpireHistoryChunks(ctx, time.Now().Add(time.Minute)); err != nil || n < 1 {
		t.Fatalf("expire = %d, %v", n, err)
	}

	// Revocation drops a pending copy.
	if err := put(0, 1, "c", 6); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeDevice(ctx, principal, phone.DeviceID, DeviceRevokedByUser); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM device_history_chunks WHERE device_id=$1`, phone.DeviceID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("chunks after revoke = %d, %v", left, err)
	}
}
