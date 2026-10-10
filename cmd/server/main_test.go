package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"noxa/internal/version"
)

func TestVersionFlagsDoNotStartServer(t *testing.T) {
	if os.Getenv("NOXA_TEST_VERSION_HELPER") == "1" {
		os.Args = []string{"noxa", os.Getenv("NOXA_TEST_VERSION_FLAG")}
		version.Version = "0.5.20"
		main()
		os.Exit(0)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--version", "-version"} {
		t.Run(flag, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestVersionFlagsDoNotStartServer$")
			command.Env = append(os.Environ(), "NOXA_TEST_VERSION_HELPER=1", "NOXA_TEST_VERSION_FLAG="+flag, "NOXA_DATABASE_URL=invalid")
			output, err := command.CombinedOutput()
			if err != nil || strings.TrimSpace(string(output)) != "0.5.20" {
				t.Fatalf("version command must work without server configuration: output=%q error=%v", output, err)
			}
		})
	}
}
