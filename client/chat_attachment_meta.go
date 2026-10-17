// Attachment metadata inside the sealed [file:...] token.
package main

import (
	"regexp"
	"strconv"
	"strings"
)

// An attachment token may carry a metadata segment between the key and the
// display name:
//
//	[file:<storage>#<keyB64>#nx1;s=<bytes>;d=<ms>;w=<peaks>#<display name>]
//
// s is the plaintext size in bytes, d a voice note's duration in
// milliseconds, and w its waveform: up to 64 peak levels, one base64url
// character (0..63) each. Every field is optional and unknown fields are
// ignored, so later versions can add some.
//
// The token only ever travels inside the message body, so the metadata gets
// exactly the protection the body and the file key get: end-to-end sealed
// for DMs and private groups, and sealed with the scope key for channels.
// That scope key is the same one the server already holds to moderate the
// body. The server never parses or stores the segment.
//
// Compatibility: a display name can never contain '#' (safeDisplayName), so
// the segment cannot be confused with a name. Older clients split on the
// first two '#', so they still get storage and key and download the file;
// they show the segment as part of the file name. Old tokens without the
// segment parse exactly as before, with no metadata.
const attachmentMetaVersion = "nx1"

var attachmentMetaSegment = regexp.MustCompile(`^nx1(;[a-z]{1,8}=[A-Za-z0-9_.-]+)*$`)

// maxAttachmentMetaSegment bounds the segment (fields + waveform) so a
// crafted token cannot carry an unbounded blob next to the name.
const maxAttachmentMetaSegment = 256
var attachmentPeaks = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

const (
	maxAttachmentMetaSize     = int64(1) << 40
	maxAttachmentMetaDuration = int64(6 * 60 * 60 * 1000)
)

// attachmentMeta is the parsed metadata segment. Zero values mean unknown.
type attachmentMeta struct {
	Size       int64  `json:"size,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Peaks      string `json:"peaks,omitempty"`
}

func (m attachmentMeta) empty() bool { return m == attachmentMeta{} }

// encode returns the segment, or "" when there is nothing to carry (the
// token then keeps the legacy three-part form).
func (m attachmentMeta) encode() string {
	var b strings.Builder
	if m.Size > 0 && m.Size <= maxAttachmentMetaSize {
		b.WriteString(";s=" + strconv.FormatInt(m.Size, 10))
	}
	if m.DurationMS > 0 && m.DurationMS <= maxAttachmentMetaDuration {
		b.WriteString(";d=" + strconv.FormatInt(m.DurationMS, 10))
	}
	if attachmentPeaks.MatchString(m.Peaks) {
		b.WriteString(";w=" + m.Peaks)
	}
	if b.Len() == 0 {
		return ""
	}
	return attachmentMetaVersion + b.String()
}

// parseAttachmentMeta reads a segment. ok is false for anything that is not a
// well-formed nx1 segment; out-of-range values are dropped field by field.
func parseAttachmentMeta(segment string) (attachmentMeta, bool) {
	var m attachmentMeta
	if len(segment) > maxAttachmentMetaSegment || !attachmentMetaSegment.MatchString(segment) {
		return m, false
	}
	for _, field := range strings.Split(segment, ";")[1:] {
		key, value, _ := strings.Cut(field, "=")
		switch key {
		case "s":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil && n > 0 && n <= maxAttachmentMetaSize {
				m.Size = n
			}
		case "d":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil && n > 0 && n <= maxAttachmentMetaDuration {
				m.DurationMS = n
			}
		case "w":
			if attachmentPeaks.MatchString(value) {
				m.Peaks = value
			}
		}
	}
	return m, true
}

// formatFileRef builds the body token. Without metadata it is byte-for-byte
// the legacy token.
func formatFileRef(storage, keyB64 string, meta attachmentMeta, name string) string {
	name = safeDisplayName(name)
	if segment := meta.encode(); segment != "" {
		return "[file:" + storage + "#" + keyB64 + "#" + segment + "#" + name + "]"
	}
	return "[file:" + storage + "#" + keyB64 + "#" + name + "]"
}

// splitFileRef is parseFileRef plus the metadata segment.
func splitFileRef(capture string) (storage, keyB64, name string, meta attachmentMeta) {
	storage, keyB64, name = parseFileRefParts(capture)
	if keyB64 == "" {
		return storage, keyB64, name, meta
	}
	if segment, rest, found := strings.Cut(name, "#"); found {
		if parsed, ok := parseAttachmentMeta(segment); ok {
			return storage, keyB64, rest, parsed
		}
	}
	return storage, keyB64, name, meta
}
