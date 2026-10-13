package config

import (
	"os"
	"strings"
	"testing"

	"noxa/internal/version"
)

func TestClientMinimumConfiguration(t *testing.T) {
	for _, minimum := range []string{"0.0.0", "0.5.94", "9.9.9", "0.5.94-rc.1+build"} {
		t.Run(minimum, func(t *testing.T) {
			v := newConfigViper()
			v.Set("client_min_version", minimum)
			cfg, err := decodeConfig(v)
			if err != nil || cfg.ClientMinVersion != minimum {
				t.Fatalf("config=%+v error=%v", cfg, err)
			}
		})
	}
	for _, minimum := range []string{"", "9.9.9.9", "latest", "0.5", "0.05.94", "0.5.94-01"} {
		t.Run("invalid/"+minimum, func(t *testing.T) {
			v := newConfigViper()
			v.Set("client_min_version", minimum)
			if _, err := decodeConfig(v); err == nil || !strings.Contains(err.Error(), "client_min_version") {
				t.Fatalf("invalid minimum accepted: %v", err)
			}
		})
	}
	cfg, err := decodeConfig(newConfigViper())
	if err != nil || cfg.ClientMinVersion != "0.0.0" {
		t.Fatalf("default=%+v error=%v", cfg, err)
	}
}

func TestClientMinimumEnvironment(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("NOXA_CLIENT_MIN_VERSION", "0.5.94")
	cfg, err := Load()
	if err != nil || cfg.ClientMinVersion != "0.5.94" {
		t.Fatalf("environment=%+v error=%v", cfg, err)
	}
	t.Setenv("NOXA_CLIENT_MIN_VERSION", "9.9.9.9")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "client_min_version") {
		t.Fatalf("invalid environment accepted: %v", err)
	}
}

func TestClientMinimumEnvironmentOverridesStrictYAML(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("config.yaml", []byte("client_min_version: '0.5.94'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NOXA_CLIENT_MIN_VERSION", "0.0.0")
	cfg, err := Load()
	if err != nil || cfg.ClientMinVersion != "0.0.0" {
		t.Fatalf("environment opt-out=%+v error=%v", cfg, err)
	}
	t.Setenv("NOXA_CLIENT_MIN_VERSION", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "client_min_version") {
		t.Fatalf("empty environment accepted: %v", err)
	}
}

func TestClientMinimumUnknownServerFailsStartupValidation(t *testing.T) {
	before := version.Version
	version.Version = "unknown"
	t.Cleanup(func() { version.Version = before })
	v := newConfigViper()
	v.Set("client_min_version", "9.9.9")
	if _, err := decodeConfig(v); err == nil || !strings.Contains(err.Error(), "client_min_version") {
		t.Fatalf("unknown server accepted: %v", err)
	}
	v.Set("client_min_version", "0.0.0")
	if _, err := decodeConfig(v); err != nil {
		t.Fatalf("explicit any depends on server version: %v", err)
	}
}
