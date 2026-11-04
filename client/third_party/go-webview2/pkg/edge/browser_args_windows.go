package edge

import (
	"strings"

	"golang.org/x/sys/windows"
)

// AdditionalBrowserArgs contains command-line fragments, not unquoted argv.
// Parse Windows quoting before merging switches, then escape each argument for
// the loader. In particular, Wails adds its own --disable-features switch: two
// occurrences in the same argument string use the last value, losing our policy.
func hardwareBrowserArguments(fragments []string) (string, error) {
	// CommandLineToArgvW treats the executable specially; supply a dummy name so
	// every caller-provided fragment uses ordinary argument parsing rules.
	args, err := windows.DecomposeCommandLine("noxa-webview " + strings.Join(fragments, " "))
	if err != nil {
		return "", err
	}
	const disabledSwitch = "--disable-features"
	const enabledSwitch = "--enable-features"
	const hardwareFeature = "ForceSoftwareForRtcLowResolutions"
	var features, enabledFeatures, retained []string
	seen := make(map[string]bool)
	addFeature := func(feature string) {
		feature = strings.TrimSpace(feature)
		if feature != "" && !seen[feature] {
			seen[feature] = true
			features = append(features, feature)
		}
	}
	seenEnabled := make(map[string]bool)
	addEnabled := func(feature string) {
		feature = strings.TrimSpace(feature)
		if feature != "" && !seenEnabled[feature] {
			seenEnabled[feature] = true
			enabledFeatures = append(enabledFeatures, feature)
		}
	}

	switches := true
	for _, arg := range args[1:] {
		if arg == "--" {
			switches = false
		}
		name, value, hasValue := strings.Cut(arg, "=")
		// Chromium on Windows accepts all three prefixes and folds ASCII case.
		for _, prefix := range []string{"--", "-", "/"} {
			if suffix, ok := strings.CutPrefix(name, prefix); ok {
				name = "--" + strings.Map(func(r rune) rune {
					if r >= 'A' && r <= 'Z' {
						return r + ('a' - 'A')
					}
					return r
				}, suffix)
				break
			}
		}
		if switches && name == disabledSwitch {
			if hasValue {
				for _, feature := range strings.Split(value, ",") {
					addFeature(feature)
				}
			}
			continue
		}
		if switches && name == enabledSwitch {
			if hasValue {
				for _, feature := range strings.Split(value, ",") {
					addEnabled(feature)
				}
			}
			continue
		}
		retained = append(retained, arg)
	}
	addFeature(hardwareFeature)
	// Keep hardware H.264 on the MFT's CBR controller: Chromium's camera-mode
	// software bitrate controller can suppress input during low-rate screen
	// sharing. This also changes trusted-rate-controller behavior and affects
	// cameras; the measured improvement is NVIDIA-specific, not a vendor guarantee.
	addFeature("MediaFoundationUseSWBRCForH264Camera")
	// WebRtcAllowWgcUsingTexture (WGC delivering GPU textures instead of CPU
	// frames) is deliberately not enabled by default. With it enabled, an NVIDIA
	// sender reported zero captured frames feeding the H.264 Media Foundation
	// encoder, so screen shares never started. Callers may still opt in via an
	// explicit --enable-features switch, which is passed through unchanged.
	// Place the merged switch before a possible -- terminator. Positional
	// arguments after it, even ones resembling switches, remain untouched.
	merged := []string{disabledSwitch + "=" + strings.Join(features, ",")}
	if len(enabledFeatures) != 0 {
		merged = append(merged, enabledSwitch+"="+strings.Join(enabledFeatures, ","))
	}
	retained = append(merged, retained...)
	for i, arg := range retained {
		retained[i] = windows.EscapeArg(arg)
	}
	return strings.Join(retained, " "), nil
}
