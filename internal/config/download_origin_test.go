package config

import (
	"strings"
	"testing"
)

func TestValidateHTTPSOrigin(t *testing.T) {
	for _, value := range []string{"https://files.example", "https://files.example:8443/", "https://[2001:db8::1]:443", "https://127.0.0.1:12337"} {
		if _, err := ValidateHTTPSOrigin(value); err != nil {
			t.Errorf("valid origin %q: %v", value, err)
		}
	}
	for _, value := range []string{"", "http://files.example", "https://user:secret@files.example", "https://files.example/path", "https://files.example/%2f", "https://files.example?", "https://files.example#", "https://files.example:0", "https://files.example:65536", "https://files.example:", "https://0.0.0.0", "https://[::]", "https://[fe80::1%25en0]", " https://files.example", "https://bad_host.example"} {
		if _, err := ValidateHTTPSOrigin(value); err == nil {
			t.Errorf("unsafe origin accepted: %q", value)
		}
	}
}

func TestDownloadOriginRequiresExplicitProxyTrust(t *testing.T) {
	cfg := loadDefaultConfig(t)
	cfg.DownloadPublicOrigin = "https://files.example"
	if err := cfg.Validate(); err == nil {
		t.Fatal("plaintext backend without trusted proxy accepted")
	}
	cfg.HealthTrustedProxies = []string{"172.18.0.2/32", "2001:db8::2/128"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.HealthProxyNetworks()) != 2 {
		t.Fatal("validated proxy networks missing")
	}
	for _, raw := range []string{"0.0.0.0/0", "::/0", "172.18.0.2/24", "not-a-network", "::ffff:172.18.0.2/128"} {
		cfg.HealthTrustedProxies = []string{raw}
		if err := cfg.Validate(); err == nil {
			t.Errorf("unsafe proxy range accepted: %q", raw)
		}
		if len(cfg.HealthProxyNetworks()) != 0 {
			t.Errorf("unsafe proxy range trusted: %q", raw)
		}
	}
	cfg.HealthTrustedProxies = nil
	cfg.HealthTLSEnabled = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestProductionRefusesPlaintextCredentialOverride(t *testing.T) {
	cfg := loadDefaultConfig(t)
	cfg.DevMode = false
	cfg.HealthAllowPlaintextCredentials = true
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "health_allow_plaintext_credentials is not allowed") {
		t.Fatalf("error=%v", err)
	}
}
