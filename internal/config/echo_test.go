package config

import (
	"os"
	"testing"
)

func TestEchoEnvironmentDisablesYAMLDefault(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("config.yaml", []byte("echo_channel_enabled: true\necho_channel_name: Custom Echo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NOXA_ECHO_CHANNEL_ENABLED", "false")
	t.Setenv("NOXA_ECHO_CHANNEL_NAME", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EchoChannelEnabled || cfg.EchoChannelName != "Custom Echo" {
		t.Fatalf("environment override ignored: %t %q", cfg.EchoChannelEnabled, cfg.EchoChannelName)
	}
}
