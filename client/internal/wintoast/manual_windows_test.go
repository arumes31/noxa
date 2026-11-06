//go:build windows && manualtoast

package wintoast

import (
	"os"
	"testing"
	"time"
)

// TestManualToast shows, replaces and removes a real toast. Run on a Windows
// desktop with: go test -tags manualtoast -run TestManualToast -v
func TestManualToast(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const app = "noXa.Desktop.ManualTest"
	if err := Register(AppData{AppID: app, DisplayName: "noXa test", CLSID: "{5B0C2E52-6F7A-4E2B-9A44-2D7C0B9E1A11}", Executable: exe}); err != nil {
		t.Fatal(err)
	}
	activated := make(chan string, 1)
	SetCallback(func(_, args string, inputs []UserData) { activated <- args })
	xml := `<toast launch="noxa-toast:v1:open:test"><visual><binding template="ToastGeneric"><text>noXa test</text><text>first</text></binding></visual><audio silent="true"/></toast>`
	if err := Show(app, Toast{XML: xml, Tag: "t1", Group: "noxa"}); err != nil {
		t.Fatal(err)
	}
	if err := Show(app, Toast{XML: xml, Tag: "t1", Group: "noxa"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if err := Remove(app, "noxa", "t1"); err != nil {
		t.Fatal(err)
	}
	if err := Clear(app); err != nil {
		t.Fatal(err)
	}
	select {
	case args := <-activated:
		t.Logf("activated: %s", args)
	default:
	}
}
