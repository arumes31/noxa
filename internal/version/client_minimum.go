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
