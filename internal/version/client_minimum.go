package version

import "fmt"

// ClientMinimum is a native-client admission policy. Its zero value allows
// every version; structural wire validation still applies independently.
type ClientMinimum struct{ required string }

// NewClientMinimum resolves the exact sentinel 9.9.9 against the running
// server's version. All other values are concrete SemVer minima, except the
// exact opt-out 0.0.0. Build metadata never affects precedence.
func NewClientMinimum(configured, serverVersion string) (ClientMinimum, error) {
	if configured == "0.0.0" {
		return ClientMinimum{}, nil
	}
	if configured == "9.9.9" {
		configured = serverVersion
	}
	if _, valid := parseSemver(configured); !valid {
		return ClientMinimum{}, fmt.Errorf("client_min_version must be 0.0.0, 9.9.9 (a known server version), or a semantic version")
	}
	return ClientMinimum{required: normalizeVersion(configured)}, nil
}

// Rejection returns an existing-wire-compatible, human-readable reason.
// Missing and unparseable client versions are rejected whenever a minimum is
// enabled. Callers must apply their wire length/control-character checks first.
func (p ClientMinimum) Rejection(clientVersion string) string {
	if p.required == "" {
		return ""
	}
	if _, valid := parseSemver(clientVersion); valid && !Compare(p.required, clientVersion) {
		return ""
	}
	if clientVersion == "" {
		clientVersion = "unknown"
	}
	return fmt.Sprintf("client version rejected: installed %s; required >=%s; update noXa to connect", clientVersion, p.required)
}

// Required returns the normalized minimum, or "" when every version is allowed.
func (p ClientMinimum) Required() string { return p.required }

// ClientRecommendedNone disables the recommended client version.
const ClientRecommendedNone = "none"

// ClientRecommended resolves client_recommended_version: empty means the
// running server's own version, "none" disables, anything else must be a
// semantic version. Clients below it update in the background. A server
// version that is not a plain release (dev builds) recommends nothing.
func ClientRecommended(configured, serverVersion string) (string, error) {
	switch configured {
	case ClientRecommendedNone:
		return "", nil
	case "":
		if parsed, valid := parseSemver(serverVersion); !valid || len(parsed.prerelease) > 0 {
			return "", nil
		}
		return normalizeVersion(serverVersion), nil
	}
	if _, valid := parseSemver(configured); !valid {
		return "", fmt.Errorf("client_recommended_version must be empty (the server version), none, or a semantic version")
	}
	return normalizeVersion(configured), nil
}
