package auth

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Exercise the actual helper in a subprocess: Fatal must fail the process,
// whereas a misleading Skip would leave the configured integration gate green.
func TestConfiguredDatabaseFailureIsFatal(t *testing.T) {
	if os.Getenv("NOXA_DB_CONTRACT_CHILD") == "1" {
		testAuthService(t)
		return
	}
	t.Setenv("NOXA_DB_CONTRACT_CHILD", "1")
	t.Setenv("NOXA_TEST_DATABASE_URL", "postgres://audit:audit@127.0.0.1:1/audit?sslmode=disable")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestConfiguredDatabaseFailureIsFatal$", "-test.v")
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	if err == nil || !strings.Contains(string(output), "configured database unavailable") {
		t.Fatalf("configured database failure must fail explicitly; error=%v\n%s", err, output)
	}
}
