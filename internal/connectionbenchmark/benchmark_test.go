package connectionbenchmark

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"testing"
	"time"
)

func TestMeasurementReconcilesEchoes(t *testing.T) {
	m := newMeasurement()
	base := time.Unix(100, 0)
	m.sentPacket(7, 1000, base, 3*time.Millisecond)
	m.sentPacket(8, 1960, base.Add(20*time.Millisecond), 0)
	m.sentPacket(9, 2920, base.Add(40*time.Millisecond), 0)
	m.receivedPacket(8, 1960, base.Add(70*time.Millisecond))
	m.receivedPacket(7, 1000, base.Add(80*time.Millisecond))
	m.receivedPacket(7, 1000, base.Add(90*time.Millisecond))
	m.receivedPacket(999, 1000, base.Add(100*time.Millisecond))
	r := m.summary()
	if r.Sent != 3 || r.Returned != 2 || r.Unreturned != 1 || r.Duplicates != 1 || r.Reordered != 1 {
		t.Fatalf("incorrect reconciliation: %+v", r)
	}
	if r.RoundTrip == nil || r.RoundTrip.Max != 80 || r.ArrivalGap == nil || r.ArrivalGap.Max != 10 {
		t.Fatalf("incorrect timing: %+v", r)
	}
}

func TestMeasurementWithoutReturnsIsUnknown(t *testing.T) {
	m := newMeasurement()
	m.sentPacket(1, 1, time.Now(), 0)
	r := m.summary()
	if r.RoundTrip != nil || r.ArrivalGap != nil || r.Returned != 0 || r.Unreturned != 1 {
		t.Fatalf("missing returns must not be healthy zero: %+v", r)
	}
}

func TestRunRejectsMissingTrustedPin(t *testing.T) {
	_, err := Run(context.Background(), Options{Address: "127.0.0.1:1"}, nil)
	if err == nil {
		t.Fatal("untrusted destination accepted")
	}
}

func TestPinnedTLSCannotAcceptMissingOrMismatchedCertificate(t *testing.T) {
	raw := []byte("trusted existing certificate")
	digest := sha256.Sum256(raw)
	config, err := trustedTLS(hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		state  tls.ConnectionState
		wantOK bool
	}{
		{name: "absent"},
		{name: "mismatched", state: tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Raw: []byte("different")}}}},
		{name: "exact", state: tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Raw: raw}}}, wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if (config.VerifyConnection(tc.state) == nil) != tc.wantOK {
				t.Fatal("certificate acceptance differed")
			}
		})
	}
}

func TestTailArrivalIsMatchedDuringDrain(t *testing.T) {
	m := newMeasurement()
	sent := time.Now()
	m.sentPacket(1, 960, sent, 0)
	if m.summary().Unreturned != 1 {
		t.Fatal("initial packet is not pending")
	}
	m.receivedPacket(1, 960, sent.Add(1500*time.Millisecond))
	if got := m.summary(); got.Unreturned != 0 || got.Returned != 1 || got.RoundTrip.Max != 1500 {
		t.Fatalf("tail was not reconciled: %+v", got)
	}
}
