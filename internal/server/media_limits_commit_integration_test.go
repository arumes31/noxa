//go:build integration

package server

import (
	"strings"
	"testing"

	pion "github.com/pion/webrtc/v4"
	"go.uber.org/zap"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/webrtc"
)

func TestMediaLimitsCommitPostgresRollbackAndRestart(t *testing.T) {
	db := integrationManagementStore(t)
	engine, err := webrtc.New(zap.NewNop(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.Close() }()
	voice := webrtc.NewVoice(engine, webrtc.NewRouter(zap.NewNop()), zap.NewNop())
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Chat: db, Groups: db, Voice: voice})
	want := netproto.MediaLimits{VideoMaxBitrate: 800000, VideoMaxWidth: 640, VideoMaxHeight: 360}
	first, err := srv.saveMediaLimits(t.Context(), "media-test-actor", want)
	if err != nil {
		t.Fatal(err)
	}
	assertSaved := func(expected netproto.MediaLimitsChanged, bounded bool) {
		t.Helper()
		var cfg config.Config
		if err := LoadPersistedServerConfig(t.Context(), &cfg, db); err != nil {
			t.Fatal(err)
		}
		got := netproto.MediaLimits{VideoMaxBitrate: cfg.VideoMaxBitrate, VideoMaxWidth: cfg.VideoMaxWidth, VideoMaxHeight: cfg.VideoMaxHeight}
		if got != expected.MediaLimits || srv.mediaLimitsSnapshot() != expected {
			t.Fatalf("persisted/publication mismatch: got=%+v want=%+v", got, expected)
		}
		client, err := pion.NewPeerConnection(pion.Configuration{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = client.Close() }()
		if _, err := client.AddTransceiverFromKind(pion.RTPCodecTypeVideo); err != nil {
			t.Fatal(err)
		}
		offer, err := client.CreateOffer(nil)
		if err != nil {
			t.Fatal(err)
		}
		voice.JoinChannel("probe", 1)
		defer func() { _ = voice.ClosePeer("probe") }()
		answer, err := voice.HandleOffer("probe", offer.SDP, func(string, string, uint16) {})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(answer, "VP8/90000") || strings.Contains(answer, "max-fs=920") != bounded || strings.Contains(answer, "VP9/90000") == bounded {
			t.Fatal("new peer did not use the committed codec limits")
		}
	}
	assertSaved(first, true)
	// Width sorts last, after bitrate and height have already been written.
	if _, err := db.DB().ExecContext(t.Context(), `CREATE FUNCTION reject_media_width() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.key='video_max_width' AND NEW.value='1280' THEN RAISE EXCEPTION 'forced media failure'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(t.Context(), `CREATE TRIGGER reject_media_width BEFORE INSERT OR UPDATE ON server_settings FOR EACH ROW EXECUTE FUNCTION reject_media_width()`); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.saveMediaLimits(t.Context(), "media-test-actor", netproto.MediaLimits{VideoMaxBitrate: 1600000, VideoMaxWidth: 1280, VideoMaxHeight: 720}); err == nil {
		t.Fatal("failed transaction reported success")
	}
	assertSaved(first, true)
	entries, err := db.AuditList(t.Context(), 0, 10)
	if err != nil || len(entries) != 1 || entries[0].Action != "media_limits_set" {
		t.Fatalf("failed transaction claimed successful audit: %+v %v", entries, err)
	}
	unlimited, err := srv.saveMediaLimits(t.Context(), "media-test-actor", netproto.MediaLimits{})
	if err != nil {
		t.Fatal(err)
	}
	assertSaved(unlimited, false)
	if err := db.SetServerSettings(t.Context(), map[string]string{"video_max_bitrate": "", "video_max_width": "", "video_max_height": ""}, 0); err != nil {
		t.Fatal(err)
	}
	if err := LoadPersistedServerConfig(t.Context(), &config.Config{}, db); err == nil {
		t.Fatal("empty stored rows were mistaken for missing values")
	}
	if err := db.SetServerSettings(t.Context(), map[string]string{"video_max_bitrate": "0", "video_max_width": "0", "video_max_height": "0"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := LoadPersistedServerConfig(t.Context(), &config.Config{}, db); err == nil {
		t.Fatal("sealed runtime settings accepted as plaintext")
	}
}
