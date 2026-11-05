// SPDX-FileCopyrightText: 2026 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package gcc

import (
	"bytes"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

// A payload larger than the pooled 1460-byte buffer is forwarded intact
// instead of being truncated or panicking the pacer goroutine.
func TestLeakyBucketPacerForwardsOversizedPayload(t *testing.T) {
	pacer := NewLeakyBucketPacer(10_000_000)
	defer func() { _ = pacer.Close() }()
	got := make(chan []byte, 1)
	pacer.AddStream(1, interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
		got <- append([]byte(nil), payload...)
		return header.MarshalSize() + len(payload), nil
	}))
	payload := bytes.Repeat([]byte{0xab}, 2000)
	if _, err := pacer.Write(&rtp.Header{SSRC: 1}, payload, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case sent := <-got:
		if !bytes.Equal(sent, payload) {
			t.Fatalf("forwarded %d bytes, want the %d-byte payload intact", len(sent), len(payload))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("payload was not forwarded")
	}
}
