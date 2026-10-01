package server

import (
	"fmt"
	"testing"

	"noxa/internal/config"
	"noxa/internal/netproto"
)

func TestAuthenticationAdvertisesMediaLimits(t *testing.T) {
	for _, limits := range []netproto.MediaLimits{{}, {VideoMaxBitrate: 3000000, VideoMaxWidth: 1280, VideoMaxHeight: 720}} {
		t.Run(fmt.Sprint(limits), func(t *testing.T) {
			env := startTestEnvFull(t, nil, func(cfg *config.Config) {
				cfg.VideoMaxBitrate = limits.VideoMaxBitrate
				cfg.VideoMaxWidth = limits.VideoMaxWidth
				cfg.VideoMaxHeight = limits.VideoMaxHeight
			})
			defer env.stop()
			conn := dialRetry(t, env.addr)
			defer func() { _ = conn.Close() }()
			send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw"})
			var response netproto.AuthResponse
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
				t.Fatal(err)
			}
			if !response.OK || response.MediaLimits == nil || *response.MediaLimits != limits {
				t.Fatalf("authentication response = %+v, want limits %+v", response, limits)
			}
		})
	}
}
