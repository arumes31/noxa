package version

import (
	"strings"
	"testing"
)

func TestClientMinimum(t *testing.T) {
	for _, tc := range []struct {
		name, configured, server, client string
		allow, invalid                   bool
	}{
		{"any legacy", "0.0.0", "unknown", "", true, false},
		{"any descriptive", "0.0.0", "unknown", "0.5.59 (Windows amd64)", true, false},
		{"old", "0.5.94", "0.5.95", "0.5.59", false, false},
		{"boundary", "0.5.94", "0.5.95", "0.5.94", true, false},
		{"newer", "0.5.94", "0.5.95", "0.6.0", true, false},
		{"missing", "0.5.94", "0.5.95", "", false, false},
		{"unknown", "0.5.94", "0.5.95", "unknown", false, false},
		{"malformed client", "0.5.94", "0.5.95", "0.5.94.1", false, false},
		{"leading zero", "0.5.94", "0.5.95", "0.5.094", false, false},
		{"prerelease below stable", "0.5.94", "0.5.95", "0.5.94-rc.1", false, false},
		{"later prerelease", "0.5.94", "0.5.95", "0.5.95-rc.1", true, false},
		{"prerelease numeric order", "0.5.94-rc.10", "0.5.95", "0.5.94-rc.2", false, false},
		{"metadata ignored", "0.5.94+server", "0.5.95", "v0.5.94+client.123", true, false},
		{"sentinel old", "9.9.9", "0.5.94+server", "0.5.93", false, false},
		{"sentinel equal", "9.9.9", "0.5.94+server", "0.5.94+client", true, false},
		{"sentinel newer", "9.9.9", "0.5.94", "0.5.95", true, false},
		{"sentinel prerelease", "9.9.9", "0.5.94-rc.2", "0.5.94-rc.1", false, false},
		{"sentinel unknown", "9.9.9", "unknown", "99.0.0", false, true},
		{"sentinel missing", "9.9.9", "", "99.0.0", false, true},
		{"four part invalid", "9.9.9.9", "0.5.94", "99.0.0", false, true},
		{"exact sentinel only", "v9.9.9", "0.5.94", "0.5.94", false, false},
		{"metadata not sentinel", "9.9.9+build", "0.5.94", "0.5.94", false, false},
		{"empty invalid", "", "0.5.94", "0.5.94", false, true},
		{"garbage invalid", "latest", "0.5.94", "0.5.94", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := NewClientMinimum(tc.configured, tc.server)
			if (err != nil) != tc.invalid {
				t.Fatalf("error = %v, invalid=%v", err, tc.invalid)
			}
			if err != nil {
				return
			}
			reason := policy.Rejection(tc.client)
			if (reason == "") != tc.allow {
				t.Fatalf("rejection=%q, allow=%v", reason, tc.allow)
			}
			if reason != "" && (!strings.Contains(reason, "required >=") || !strings.Contains(reason, "update noXa")) {
				t.Fatalf("not actionable: %s", reason)
			}
		})
	}
}
