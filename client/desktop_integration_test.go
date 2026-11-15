package main

import (
	"strings"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/options"
)

func TestDeepLinkArgFilter(t *testing.T) {
	link := "noxa://example.com:9987/invite/12#" + strings.Repeat("A", 43)
	for _, ok := range []string{link, "NOXA://host/invite/1#x"} {
		if deepLinkArg(ok) != ok {
			t.Errorf("rejected %q", ok)
		}
	}
	for _, bad := range []string{
		"", "noxa://", "--minimized", "https://example.com/invite/1#x", "file:///C:/x",
		"noxa://" + strings.Repeat("a", deepLinkMaxLength), "noxa://host/\x00", "noxa://host/a\nb",
		"noxa://host /x", "noxa://host/\u00e9", `noxa://host" --flag`, `noxa://host\x`,
	} {
		if got := deepLinkArg(bad); got != "" {
			t.Errorf("accepted %q", bad)
		}
	}
	minimized, got := readLaunchArgs([]string{"--minimized", "https://x", link, "noxa://second/invite/2#y"})
	if !minimized || got != link {
		t.Fatalf("readLaunchArgs = %v, %q", minimized, got)
	}
	if minimized, got := readLaunchArgs([]string{"--other"}); minimized || got != "" {
		t.Fatalf("plain flags read as %v, %q", minimized, got)
	}
}

func TestSingleInstanceExemptsUpdateProcesses(t *testing.T) {
	none := func(string) string { return "" }
	if !singleInstanceAllowed(false, false, none) {
		t.Fatal("normal launch must take the lock")
	}
	if singleInstanceAllowed(true, false, none) || singleInstanceAllowed(false, true, none) {
		t.Fatal("update-launched or handover process took the lock")
	}
	for _, name := range []string{updateHandoverEnv, updateReadyAddressEnv, updateReadyNonceEnv, updateReconnectEnv, updateWindowEnv} {
		env := func(key string) string {
			if key == name {
				return "x"
			}
			return ""
		}
		if singleInstanceAllowed(false, false, env) {
			t.Errorf("%s set but lock enabled", name)
		}
	}
}

func TestRegistryCommandValues(t *testing.T) {
	exe := `C:\Program Files\noXa\noxa.exe`
	if got := autostartCommand(exe); got != `"C:\Program Files\noXa\noxa.exe" --minimized` {
		t.Errorf("autostart = %s", got)
	}
	if got := protocolCommand(exe); got != `"C:\Program Files\noXa\noxa.exe" "%1"` {
		t.Errorf("protocol = %s", got)
	}
}

func TestDeepLinkQueuedUntilFrontendReady(t *testing.T) {
	var emitted []string
	a := &App{eventEmit: func(name string, payload any) {
		if name == deepLinkEvent {
			emitted = append(emitted, payload.(string))
		}
	}}
	a.deliverDeepLink("noxa://a")
	if len(emitted) != 0 {
		t.Fatal("emitted before ready")
	}
	if got := a.TakeDeepLink(); got != "noxa://a" {
		t.Fatalf("TakeDeepLink = %q", got)
	}
	a.secondInstanceLaunch(options.SecondInstanceData{Args: []string{"--minimized", "noxa://b"}})
	a.secondInstanceLaunch(options.SecondInstanceData{Args: []string{"https://c"}})
	if len(emitted) != 1 || emitted[0] != "noxa://b" {
		t.Fatalf("emitted %v", emitted)
	}
}

func TestSecondInstanceLinkReplacesColdStartLink(t *testing.T) {
	a := &App{deepLink: deepLinkInbox{pending: "noxa://cold"}}
	a.secondInstanceLaunch(options.SecondInstanceData{Args: []string{"noxa://newer"}})
	if got := a.TakeDeepLink(); got != "noxa://newer" {
		t.Fatalf("TakeDeepLink = %q, want the newer link", got)
	}
}
