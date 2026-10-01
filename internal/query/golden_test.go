package query

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite ServerQuery golden response snapshots")

// TestResponseGolden locks the public text protocol's exact escaping, record
// separators and terminal status lines. Add stable read-only commands here as
// the query surface grows; use -update-golden only after intentional review.
func TestResponseGolden(t *testing.T) {
	addr, _ := startQueryServer(t, &roleQueryBackend{})
	conn, reader := dialQuery(t, addr)
	defer func() { _ = conn.Close() }()
	if got := lastErr(t, sendCmd(t, conn, reader, "login integration pw authorization_model=roles-v1")); got != "error id=0 msg=ok" {
		t.Fatal(got)
	}

	var got strings.Builder
	for _, command := range []string{"clientlist", "channellist", "channelinfo cid=2"} {
		got.WriteString("## " + command + "\n")
		got.WriteString(strings.Join(sendCmd(t, conn, reader, command), "\n"))
		got.WriteByte('\n')
	}
	path := filepath.Join("testdata", "responses.golden")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != string(want) {
		t.Fatalf("ServerQuery response snapshot changed (-update-golden to accept):\n--- got ---\n%s\n--- want ---\n%s", got.String(), want)
	}
}
