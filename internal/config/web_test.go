package config

import (
	"strings"
	"testing"
)

func validWebConfig() WebConfig {
	return WebConfig{
		Enabled:              true,
		Addr:                 DefaultWebAddr,
		PublicURL:            "https://chat.example",
		TrustedProxies:       []string{"10.0.0.0/8"},
		MaxDevicesPerAccount: 10,
		WSConnectionsPerIP:   8,
		WSAttemptsPerMinute:  30,
	}
}

func TestWebConfigDefaults(t *testing.T) {
	cfg := &Config{}
	if err := newConfigViper().Unmarshal(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Web.Enabled || cfg.Web.Addr != DefaultWebAddr || cfg.Web.MaxDevicesPerAccount != 10 {
		t.Fatalf("web defaults = %+v", cfg.Web)
	}
}

func TestWebConfigValidate(t *testing.T) {
	if errs := validWebConfig().validate(); len(errs) != 0 {
		t.Fatalf("valid config rejected: %v", errs)
	}
	cases := []struct {
		want   string
		mutate func(*WebConfig)
	}{
		{"public_url", func(w *WebConfig) { w.PublicURL = "http://chat.example" }},
		{"domain name", func(w *WebConfig) { w.PublicURL = "https://192.0.2.1" }},
		{"domain name", func(w *WebConfig) { w.PublicURL = "https://localhost" }},
		{"set together", func(w *WebConfig) { w.CertFile = "cert.pem" }},
		{"acme excludes", func(w *WebConfig) { w.CertFile, w.KeyFile, w.ACME = "c", "k", true }},
		{"TLS-terminating proxy", func(w *WebConfig) { w.TrustedProxies = nil }},
		{"trusted_proxies", func(w *WebConfig) { w.TrustedProxies = []string{"0.0.0.0/0"} }},
		{"allowed_origins", func(w *WebConfig) { w.AllowedOrigins = []string{"http://x.example"} }},
		{"max_devices_per_account", func(w *WebConfig) { w.MaxDevicesPerAccount = 101 }},
		{"ws_connections_per_ip", func(w *WebConfig) { w.WSConnectionsPerIP = 0 }},
		{"ws_attempts_per_minute", func(w *WebConfig) { w.WSAttemptsPerMinute = 0 }},
	}
	for _, tc := range cases {
		cfg := validWebConfig()
		tc.mutate(&cfg)
		errs := cfg.validate()
		if len(errs) == 0 || !strings.Contains(errs[0].Error(), tc.want) {
			t.Errorf("%s: errors = %v", tc.want, errs)
		}
	}
}

func TestWebConfigDisabledOnlyChecksDeviceLimit(t *testing.T) {
	cfg := WebConfig{}
	if errs := cfg.validate(); len(errs) != 0 {
		t.Fatalf("disabled web config rejected: %v", errs)
	}
	if got := cfg.DeviceLimit(); got != DefaultMaxDevicesPerAccount {
		t.Fatalf("unset DeviceLimit = %d", got)
	}
}

func TestWebConfigOriginsAndRPID(t *testing.T) {
	cfg := validWebConfig()
	cfg.PublicURL = "https://Chat.Example:8443"
	cfg.AllowedOrigins = []string{"https://app.example"}
	if got := cfg.RPID(); got != "chat.example" {
		t.Fatalf("RPID = %q", got)
	}
	origins := cfg.Origins()
	if len(origins) != 2 || origins[0] != "https://chat.example:8443" || origins[1] != "https://app.example" {
		t.Fatalf("Origins = %v", origins)
	}
}
