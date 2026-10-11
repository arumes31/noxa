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
	const hardwareFeature = "ForceSoftwareForRtcLowResolutions"
	var features, retained []string
	seen := make(map[string]bool)
	addFeature := func(feature string) {
		feature = strings.TrimSpace(feature)
		if feature != "" && !seen[feature] {
			seen[feature] = true
			features = append(features, feature)
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
		retained = append(retained, arg)
	}
	addFeature(hardwareFeature)
	// Place the merged switch before a possible -- terminator. Positional
	// arguments after it, even ones resembling switches, remain untouched.
	retained = append([]string{disabledSwitch + "=" + strings.Join(features, ",")}, retained...)
	for i, arg := range retained {
		retained[i] = windows.EscapeArg(arg)
	}
	return strings.Join(retained, " "), nil
}
