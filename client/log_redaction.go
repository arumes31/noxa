package main

import (
	"regexp"
)

// Pairing and device-link secrets must never reach the client log (item 253).
// The daily log writer runs every line through redactLogSecrets, so a stray
// log.Printf of a device-link response or a pasted link cannot leak the
// short-lived secret. Patterns mirror frontend/src/secret-redaction.js.
const logRedacted = "[redacted]"

var (
	// https://host/link#id=…&k=… — the QR / mobile login URL.
	logLinkURLFragment = regexp.MustCompile(`(?i)(https?://[^\s"'<>\\]*?/link)#[^\s"'<>\\]*`)
	// Secret-carrying query or fragment parameters anywhere.
	logSecretParameters = regexp.MustCompile(`(?i)([#&?](?:k|key|secret|token|code|link|pair(?:ing)?[_-]?code|link[_-]?secret|device[_-]?link)=)[^\s&"'<>\\]+`)
	// JSON members whose value is a secret by construction.
	logSecretJSONMembers = regexp.MustCompile(`(?i)("(?:k|secret|token|access_token|refresh_token|link_secret|pairing_code|pairing_token|link_code|device_link|qr)"\s*:\s*")(?:[^"\\]|\\.)*(")`)
	// Formatted pairing code XXXX-XXXX from the link alphabet.
	logPairingCode = regexp.MustCompile(`\b[23456789ABCDEFGHJKMNPQRSTUVWXYZ]{4}-[23456789ABCDEFGHJKMNPQRSTUVWXYZ]{4}\b`)
	logHasLetter   = regexp.MustCompile(`[A-Z]`)
	// A QR image embedded as a data URL.
	logQRDataURL = regexp.MustCompile(`data:image/(?:png|svg\+xml|jpeg|gif);base64,[A-Za-z0-9+/=]{64,}`)
)

// redactLogSecrets masks pairing URLs, link secrets, typed pairing codes and
// QR data URLs in one log record. Text without secrets is returned unchanged.
func redactLogSecrets(p []byte) []byte {
	out := logLinkURLFragment.ReplaceAll(p, []byte("${1}#"+logRedacted))
	out = logSecretParameters.ReplaceAll(out, []byte("${1}"+logRedacted))
	out = logSecretJSONMembers.ReplaceAll(out, []byte("${1}"+logRedacted+"${2}"))
	out = logPairingCode.ReplaceAllFunc(out, func(match []byte) []byte {
		// Purely numeric blocks such as 2345-6789 are ranges, not codes.
		if !logHasLetter.Match(match) {
			return match
		}
		return []byte(logRedacted)
	})
	out = logQRDataURL.ReplaceAll(out, []byte("data:image/png;base64,"+logRedacted))
	return out
}
