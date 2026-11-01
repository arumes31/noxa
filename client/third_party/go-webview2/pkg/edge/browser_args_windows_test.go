package edge

import (
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestHardwareBrowserArguments(t *testing.T) {
	const hardwareFeature = "ForceSoftwareForRtcLowResolutions"
	const cameraRateControlFeature = "MediaFoundationUseSWBRCForH264Camera"
	const feature = hardwareFeature + "," + cameraRateControlFeature
	const textureFeature = "WebRtcAllowWgcUsingTexture"
	const textureSwitch = "--enable-features=" + textureFeature
	for _, tt := range []struct {
		name      string
		fragments []string
		want      []string
	}{
		{"default", nil, []string{"--disable-features=" + feature, textureSwitch}},
		{"actual Wails defaults", []string{"--disable-features=msSmartScreenProtection"},
			[]string{"--disable-features=msSmartScreenProtection," + feature, textureSwitch}},
		{"repeated switches and duplicate features", []string{"--disable-features=msSmartScreenProtection,RendererCodeIntegrity", "--disable-features=" + feature + ",msSmartScreenProtection", "--remote-debugging-port=9661"},
			[]string{"--disable-features=msSmartScreenProtection,RendererCodeIntegrity," + feature, textureSwitch, "--remote-debugging-port=9661"}},
		{"Chromium Windows switch aliases", []string{"--DISABLE-FEATURES=Alpha -disable-features=Beta /Disable-Features=Gamma --other=Keep"},
			[]string{"--disable-features=Alpha,Beta,Gamma," + feature, textureSwitch, "--other=Keep"}},
		{"Windows quoting and fragment whitespace", []string{"  --disable-features=\"Alpha, Beta\"\t--lang=de-DE  ", `"--disable-features=Gamma" --custom="C:\a folder\file"`, `--message="a \"quoted\" value"`},
			[]string{"--disable-features=Alpha,Beta,Gamma," + feature, textureSwitch, "--lang=de-DE", `--custom=C:\a folder\file`, `--message=a "quoted" value`}},
		{"other switches and empty arguments survive", []string{`--enable-features=Other --disable-features= --disable-features "" --disable-features-extra=Keep --foo=first --foo=last`},
			[]string{"--disable-features=" + feature, "--enable-features=Other," + textureFeature, "", "--disable-features-extra=Keep", "--foo=first", "--foo=last"}},
		{"switch terminator preserves positional text", []string{"-- --disable-features=Positional"},
			[]string{"--disable-features=" + feature, textureSwitch, "--", "--disable-features=Positional"}},
		// The controller workaround must merge with an explicit caller policy,
		// without selecting a software codec or changing unrelated GPU switches.
		{"existing camera controller policy is deduplicated", []string{`"--disable-features=MediaFoundationUseSWBRCForH264Camera, msSmartScreenProtection"`, "--disable-features=" + cameraRateControlFeature, "--enable-features=Other --use-angle=d3d11"},
			[]string{"--disable-features=" + cameraRateControlFeature + ",msSmartScreenProtection," + hardwareFeature, "--enable-features=Other," + textureFeature, "--use-angle=d3d11"}},
		{"controller-looking positional argument stays positional", []string{`--message="keep quoted text" -- --disable-features=` + cameraRateControlFeature},
			[]string{"--disable-features=" + feature, textureSwitch, "--message=keep quoted text", "--", "--disable-features=" + cameraRateControlFeature}},
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

func TestWindowCaptureFeaturesPreserveCallerPolicy(t *testing.T) {
	const required = "--disable-features=ForceSoftwareForRtcLowResolutions,MediaFoundationUseSWBRCForH264Camera"
	const texture = "WebRtcAllowWgcUsingTexture"
	for _, tc := range []struct {
		name            string
		fragments, want []string
	}{
		{"merge enable lists", []string{"--enable-features=Other,Alpha --enable-features=Alpha,Beta"}, []string{required, "--enable-features=Other,Alpha,Beta," + texture}},
		{"enable aliases", []string{"/ENABLE-FEATURES=Other -enable-features=Beta --Enable-Features=Other"}, []string{required, "--enable-features=Other,Beta," + texture}},
		{"explicit texture opt-out", []string{"--disable-features=" + texture}, []string{"--disable-features=" + texture + ",ForceSoftwareForRtcLowResolutions,MediaFoundationUseSWBRCForH264Camera"}},
		{"opt-out retains other enabled features", []string{"--enable-features=Other --disable-features=" + texture}, []string{"--disable-features=" + texture + ",ForceSoftwareForRtcLowResolutions,MediaFoundationUseSWBRCForH264Camera", "--enable-features=Other"}},
		{"parameterized caller enable", []string{"--enable-features=" + texture + ":option/value,Other"}, []string{required, "--enable-features=" + texture + ":option/value,Other"}},
		{"trial caller enable", []string{"--enable-features=" + texture + "<CallerTrial"}, []string{required, "--enable-features=" + texture + "<CallerTrial"}},
		{"parameterized opt-out", []string{"--disable-features=" + texture + "<CallerTrial"}, []string{"--disable-features=" + texture + "<CallerTrial,ForceSoftwareForRtcLowResolutions,MediaFoundationUseSWBRCForH264Camera"}},
		{"positional opt-out is not a switch", []string{"-- --disable-features=" + texture + " --enable-features=Positional"}, []string{required, "--enable-features=" + texture, "--", "--disable-features=" + texture, "--enable-features=Positional"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, err := hardwareBrowserArguments(tc.fragments)
			if err != nil {
				t.Fatal(err)
			}
			args, err := windows.DecomposeCommandLine("noxa-webview " + command)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(args[1:], tc.want) {
				t.Fatalf("got %#v, want %#v", args[1:], tc.want)
			}
		})
	}
}
