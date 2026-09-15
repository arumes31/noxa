package config

import (
	"strings"
	"testing"
)

func TestWebRTCNetworkEnvironment(t *testing.T) {
	v := newConfigViper()
	v.Set("dev_mode", true)
	v.SetEnvPrefix("NOXA")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	t.Setenv("NOXA_WEBRTC_UDP_ADDR", ":12341")
	t.Setenv("NOXA_WEBRTC_EXTERNAL_IPS", "203.0.113.10,100.103.150.8")
	cfg, err := decodeConfig(v)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WebRTC.UDPAddr != ":12341" || len(cfg.WebRTC.ExternalIPs) != 2 {
		t.Fatalf("network settings not decoded: %+v", cfg.WebRTC)
	}
}

func TestWebRTCNetworkValidation(t *testing.T) {
	for _, tc := range []struct {
		name, addr string
		ips        []string
	}{
		{"bad port", ":65536", nil},
		{"IPv6 bind", "[::1]:12341", nil},
		{"mapped IPv6 bind", "[::ffff:127.0.0.1]:12341", nil},
		{"hostname bind", "localhost:12341", nil},
		{"missing bind", "", []string{"203.0.113.10"}},
		{"hostname", ":12341", []string{"example.com"}},
		{"IPv6 unsupported", ":12341", []string{"::1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newConfigViper()
			v.Set("dev_mode", true)
			v.Set("webrtc.udp_addr", tc.addr)
			v.Set("webrtc.external_ips", tc.ips)
			if _, err := decodeConfig(v); err == nil {
				t.Fatal("expected invalid network config to fail")
			}
		})
	}
}
