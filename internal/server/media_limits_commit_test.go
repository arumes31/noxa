package server

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/webrtc"
)

type committingMediaVoice struct {
	*fakeVoice
	limits netproto.MediaLimits
	before func(context.Context) error
	calls  int
}

func (v *committingMediaVoice) CommitVideoLimits(ctx context.Context, rate int, bounds webrtc.VideoBounds, commit func(context.Context) error) error {
	v.calls++
	if v.before != nil {
		if err := v.before(ctx); err != nil {
			return err
		}
	}
	if err := commit(ctx); err != nil {
		return err
	}
	v.limits = netproto.MediaLimits{VideoMaxBitrate: rate, VideoMaxWidth: bounds.Width, VideoMaxHeight: bounds.Height}
	return nil
}

func TestSaveMediaLimitsCommitPublicationAndRestart(t *testing.T) {
	b := &configCommitStore{fakeChat: newFakeChat()}
	v := &committingMediaVoice{fakeVoice: &fakeVoice{}}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Chat: b, Voice: v})
	want := netproto.MediaLimits{VideoMaxBitrate: 800000, VideoMaxWidth: 640, VideoMaxHeight: 360}
	before := srv.mediaLimitsSnapshot()
	failure := errors.New("rolled back")
	b.failure = failure
	if _, err := srv.saveMediaLimits(t.Context(), "actor", want); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if srv.mediaLimitsSnapshot() != before || v.limits != before.MediaLimits {
		t.Fatal("failed persistence changed runtime or publication")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b.failure, b.afterSave = nil, func() {
		if srv.mediaLimitsSnapshot() != before || v.limits != before.MediaLimits {
			t.Error("publication/application preceded confirmed persistence")
		}
		cancel()
	}
	got, err := srv.saveMediaLimits(ctx, "actor", want)
	if err != nil || got.MediaLimits != want || got.Revision != before.Revision+1 || srv.mediaLimitsSnapshot() != got || v.limits != want {
		t.Fatalf("commit not applied/published: got=%+v error=%v", got, err)
	}
	var cfg config.Config
	if err := LoadPersistedServerConfig(t.Context(), &cfg, b); err != nil {
		t.Fatal(err)
	}
	if cfg.VideoMaxBitrate != want.VideoMaxBitrate || cfg.VideoMaxWidth != want.VideoMaxWidth || cfg.VideoMaxHeight != want.VideoMaxHeight {
		t.Fatal("restart lost persisted media limits")
	}
	b.afterSave = nil
	if same, err := srv.saveMediaLimits(t.Context(), "actor", want); err != nil || same != got {
		t.Fatalf("identical save changed revision: %+v %v", same, err)
	}
	if _, err := srv.saveMediaLimits(t.Context(), "actor", netproto.MediaLimits{}); err != nil {
		t.Fatal(err)
	}
	if err := LoadPersistedServerConfig(t.Context(), &cfg, b); err != nil || cfg.VideoMaxBitrate != 0 || cfg.VideoMaxWidth != 0 || cfg.VideoMaxHeight != 0 {
		t.Fatal("explicit unlimited values not restored on restart", err)
	}
}

func TestSaveMediaLimitsRejectsBeforePersistence(t *testing.T) {
	b := &configCommitStore{fakeChat: newFakeChat()}
	v := &committingMediaVoice{fakeVoice: &fakeVoice{}}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Chat: b, Voice: v})
	if _, err := srv.saveMediaLimits(t.Context(), "actor", netproto.MediaLimits{VideoMaxWidth: 640}); err == nil {
		t.Fatal("accepted invalid dimensions")
	}
	srv.mediaLimitsRevision = ^uint64(0)
	if _, err := srv.saveMediaLimits(t.Context(), "actor", netproto.MediaLimits{VideoMaxBitrate: 800}); err == nil {
		t.Fatal("accepted revision exhaustion")
	}
	srv.mediaLimitsRevision = 0
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := srv.saveMediaLimits(ctx, "actor", netproto.MediaLimits{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if b.calls != 0 || v.calls != 0 {
		t.Fatal("invalid/canceled/exhausted request reached effects")
	}
	v.before = func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("unbounded media save")
		}
		return context.DeadlineExceeded
	}
	if _, err := srv.saveMediaLimits(t.Context(), "actor", netproto.MediaLimits{}); !errors.Is(err, context.DeadlineExceeded) || b.calls != 0 {
		t.Fatal("failed media preparation reached persistence", err)
	}
	srv.deps.Voice = &fakeVoice{}
	if _, err := srv.saveMediaLimits(t.Context(), "actor", netproto.MediaLimits{}); err == nil || b.calls != 0 {
		t.Fatal("backend without atomic media support accepted save", err)
	}
}

func TestPersistedMediaLimitsRequireCompleteValidSet(t *testing.T) {
	for _, settings := range []memorySettings{
		{"video_max_bitrate": ""},
		{"video_max_bitrate": "", "video_max_width": "", "video_max_height": ""},
		{"video_max_bitrate": "800"},
		{"video_max_bitrate": "0", "video_max_width": "640", "video_max_height": "0"},
		{"video_max_bitrate": "bad", "video_max_width": "0", "video_max_height": "0"},
		{"video_max_bitrate": "100000001", "video_max_width": "0", "video_max_height": "0"},
	} {
		settings["max_clients_override"] = "500"
		cfg := config.Config{MaxClients: 10, VideoMaxBitrate: 1000, VideoMaxWidth: 1280, VideoMaxHeight: 720}
		if err := LoadPersistedServerConfig(t.Context(), &cfg, settings); err == nil {
			t.Fatal("accepted corrupt media settings", settings)
		}
		if cfg.MaxClients != 10 || cfg.VideoMaxBitrate != 1000 || cfg.VideoMaxWidth != 1280 || cfg.VideoMaxHeight != 720 {
			t.Fatal("failed load partially changed startup configuration")
		}
	}
	cfg := config.Config{VideoMaxBitrate: 1000, VideoMaxWidth: 1280, VideoMaxHeight: 720}
	if err := LoadPersistedServerConfig(t.Context(), &cfg, memorySettings{}); err != nil || cfg.VideoMaxBitrate != 1000 || cfg.VideoMaxWidth != 1280 || cfg.VideoMaxHeight != 720 {
		t.Fatal("missing persisted settings discarded startup defaults", err)
	}
}
