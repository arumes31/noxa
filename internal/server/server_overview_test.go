package server

import (
	"math"
	"noxa/internal/netproto"
	"noxa/internal/state"
	"testing"
	"time"
)

func number(v float64) *float64 { return &v }
func TestServerOverviewWeightsFreshAudioAndExcludesStaleSessions(t *testing.T) {
	now := time.Now()
	s := &TCPServer{deps: &Deps{State: state.New(nil)}, clients: map[string]*Client{}}
	for i, id := range []string{"a", "b", "stale", "epoch", "video"} {
		s.clients[id] = &Client{ID: id, authed: true, rttKnown: true, rttNs: int64((i+1)*10) * int64(time.Millisecond), rttObservedAt: now, voiceTelemetryAt: now, voiceTelemetryEpoch: 4, voiceTelemetry: &netproto.VoiceTelemetry{ChannelID: 1, Tracks: []netproto.VoiceReceiverDiagnostics{{Codec: "audio/opus", PacketsReceived: number(90), PacketsLost: number(10), JitterMS: number(2)}}}}
		s.deps.State.AddClient(&state.Client{ClientID: id, ChannelID: 1, ChannelEpoch: 4})
	}
	s.clients["b"].voiceTelemetry.Tracks[0] = netproto.VoiceReceiverDiagnostics{Codec: "audio/opus", PacketsReceived: number(970), PacketsLost: number(30), JitterMS: number(6)}
	s.clients["stale"].voiceTelemetryAt = now.Add(-16 * time.Second)
	s.clients["stale"].rttObservedAt = now.Add(-31 * time.Second)
	s.clients["epoch"].voiceTelemetryEpoch = 3
	s.clients["video"].voiceTelemetry.Tracks[0].Codec = "video/VP8"
	v := s.serverOverview(now)
	if v.Clients != 5 || v.LatencySamples != 4 || v.LossSamples != 2 || v.JitterSamples != 2 || v.LatencyMS == nil || *v.LatencyMS != 30 || *v.JitterMS != 4 || math.Abs(*v.LossPercent-100.0*40/1100) > 0.0001 {
		t.Fatalf("bad overview: %+v", v)
	}
	for _, c := range s.clients {
		c.voiceTelemetry = nil
		c.rttKnown = false
	}
	v = s.serverOverview(now)
	if v.LatencyMS != nil || v.LossPercent != nil || v.JitterMS != nil {
		t.Fatal("missing samples became zeros")
	}
	if (&TCPServer{}).serverOverview(now) != nil {
		t.Fatal("unavailable state")
	}
}
func TestServerOverviewRequiresConnectionInformationPermission(t *testing.T) {
	env, _, _ := voiceDiagnosticsEnv(t)
	for _, id := range []string{"admin-uid", "user-uid"} {
		conn, _ := dialAuthed(t, env.addr, id)
		send(t, conn, netproto.MsgServerInfoQuery, netproto.ServerInfoQuery{})
		var info netproto.ServerInfoResponse
		if err := netproto.Decode(readOfType(t, conn, netproto.MsgServerInfoResponse), &info); err != nil {
			t.Fatal(err)
		}
		if (info.Overview != nil) != (id == "user-uid") {
			t.Fatalf("overview access for %s", id)
		}
		_ = conn.Close()
	}
}
