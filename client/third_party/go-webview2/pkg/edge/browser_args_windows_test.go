package edge

import (
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestHardwareBrowserArguments(t *testing.T) {
	const feature = "ForceSoftwareForRtcLowResolutions"
	for _, tt := range []struct {
		name      string
		fragments []string
		want      []string
	}{
		{"default", nil, []string{"--disable-features=" + feature}},
		{"actual Wails defaults", []string{"--disable-features=msSmartScreenProtection"},
			[]string{"--disable-features=msSmartScreenProtection," + feature}},
		{"repeated switches and duplicate features", []string{"--disable-features=msSmartScreenProtection,RendererCodeIntegrity", "--disable-features=" + feature + ",msSmartScreenProtection", "--remote-debugging-port=9661"},
			[]string{"--disable-features=msSmartScreenProtection,RendererCodeIntegrity," + feature, "--remote-debugging-port=9661"}},
		{"Chromium Windows switch aliases", []string{"--DISABLE-FEATURES=Alpha -disable-features=Beta /Disable-Features=Gamma --other=Keep"},
			[]string{"--disable-features=Alpha,Beta,Gamma," + feature, "--other=Keep"}},
		{"Windows quoting and fragment whitespace", []string{"  --disable-features=\"Alpha, Beta\"\t--lang=de-DE  ", `"--disable-features=Gamma" --custom="C:\a folder\file"`, `--message="a \"quoted\" value"`},
			[]string{"--disable-features=Alpha,Beta,Gamma," + feature, "--lang=de-DE", `--custom=C:\a folder\file`, `--message=a "quoted" value`}},
		{"other switches and empty arguments survive", []string{`--enable-features=Other --disable-features= --disable-features "" --disable-features-extra=Keep --foo=first --foo=last`},
			[]string{"--disable-features=" + feature, "--enable-features=Other", "", "--disable-features-extra=Keep", "--foo=first", "--foo=last"}},
		{"switch terminator preserves positional text", []string{"-- --disable-features=Positional"},
			[]string{"--disable-features=" + feature, "--", "--disable-features=Positional"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := append([]string(nil), tt.fragments...)
			command, err := hardwareBrowserArguments(tt.fragments)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := windows.DecomposeCommandLine("noxa-webview " + command)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(parsed[1:], tt.want) {
				t.Fatalf("got %#v, want %#v", parsed[1:], tt.want)
			}
			if !reflect.DeepEqual(tt.fragments, before) {
				t.Fatal("caller arguments were mutated")
			}
			count := 0
			for _, arg := range parsed[1:] {
				if arg == "--" {
					break
				}
				if strings.HasPrefix(arg, "--disable-features=") {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("got %d active disable-features switches", count)
			}
		})
	}
}

func TestHardwareBrowserArgumentsRejectsNUL(t *testing.T) {
	if _, err := hardwareBrowserArguments([]string{"--custom=a\x00b"}); err == nil {
		t.Fatal("NUL argument unexpectedly accepted")
	}
}
