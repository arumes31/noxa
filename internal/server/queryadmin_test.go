package server

import (
	"context"
	"testing"

	"noxa/internal/config"
)

func TestEffectiveMaxClientsUsesOnlyValidNonNegativeOverride(t *testing.T) {
	env := startTestEnvFull(t, nil, func(cfg *config.Config) { cfg.MaxClients = 9 })
	defer env.stop()
	ctx := context.Background()
	if got := env.srv.EffectiveMaxClients(ctx); got != 9 {
		t.Fatalf("default max clients = %d, want 9", got)
	}
	for _, test := range []struct {
		value string
		want  int
	}{
		{value: "24", want: 24},
		{value: "-1", want: 9},
		{value: "not-a-number", want: 9},
	} {
		t.Run(test.value, func(t *testing.T) {
			if err := env.chat.SetServerSetting(ctx, "max_clients_override", test.value, 0); err != nil {
				t.Fatalf("set override: %v", err)
			}
			if got := env.srv.EffectiveMaxClients(ctx); got != test.want {
				t.Fatalf("EffectiveMaxClients(%q) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}
