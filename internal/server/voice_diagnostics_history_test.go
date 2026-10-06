package server

import (
	"noxa/internal/netproto"
	"testing"
	"time"
)

func TestVoiceHistoryBoundsCountAgeAndChannelEpoch(t *testing.T) {
	client := &Client{}
	now := time.Unix(1000, 0)
	report := voiceTelemetryFixture()
	loss, buffer := 2.0, 350.0
	report.Tracks[0].LossPercent, report.Tracks[0].BufferMS = &loss, &buffer
	for i := range 80 {
		retainVoiceHistory(client, report, 1, now.Add(time.Duration(i)*time.Second))
	}
	if len(client.voiceHistory) != 60 {
		t.Fatalf("history count = %d", len(client.voiceHistory))
	}
	if *client.voiceHistory[0].BufferMS != 350 {
		t.Fatal("buffer evidence missing")
	}
	retainVoiceHistory(client, report, 1, now.Add(7*time.Minute))
	if len(client.voiceHistory) != 1 {
		t.Fatal("expired history retained")
	}
	retainVoiceHistory(client, report, 2, now.Add(8*time.Minute))
	if len(client.voiceHistory) != 1 {
		t.Fatal("history leaked across channel epochs")
	}
	if client.voiceHistory[0].ObservedAt != now.Add(8*time.Minute).UnixMilli() {
		t.Fatal("wrong sample retained")
	}
	report.SessionID = "replacement-peer"
	retainVoiceHistory(client, report, 2, now.Add(8*time.Minute+time.Second))
	if len(client.voiceHistory) != 1 {
		t.Fatal("history leaked across media peer replacement")
	}
}

func TestVoiceCorrelationRequiresCurrentSSRCAndFreshPublisherReport(t *testing.T) {
	ssrc := uint32(123)
	receiver := netproto.VoiceReceiverDiagnostics{SSRC: &ssrc, PublisherID: "spoofed"}
	ingress := netproto.VoiceIngressDiagnostics{SSRC: 456, StartedAt: 1000}
	path := netproto.VoiceMediaPath{PublisherID: "actual", OutputSSRC: 123, Ingress: &ingress}
	publisher := &netproto.VoiceClientReport{ReceivedAt: 2000, Report: netproto.VoiceTelemetry{Senders: []netproto.VoiceSenderDiagnostics{{SSRC: 456}}}}
	correlated := correlateVoicePath(receiver, 2000, path, publisher)
	if correlated.Sender == nil || correlated.PublisherID != "actual" {
		t.Fatal("did not use authoritative SFU binding")
	}
	publisher.Stale = true
	if correlateVoicePath(receiver, 2000, path, publisher).Sender != nil {
		t.Fatal("stale publisher measurement matched")
	}
	publisher.Stale = false
	publisher.ReceivedAt = 999
	if correlateVoicePath(receiver, 2000, path, publisher).Sender != nil {
		t.Fatal("old publication measurement matched")
	}
	publisher.ReceivedAt = 2000
	if correlateVoicePath(receiver, 999, path, publisher).Receiver != nil {
		t.Fatal("receiver measurements from previous publication matched")
	}
	receiver.SSRC = nil
	if correlateVoicePath(receiver, 2000, path, publisher).Receiver != nil {
		t.Fatal("legacy receiver without SSRC was guessed")
	}
}
