package netproto

import (
	"strings"
	"unicode/utf8"
)

// ServerTextSet exposes only named presentation/rules settings. A present empty
// value clears the override; nil is not a request to erase existing content.
type ServerTextSet struct {
	Key   string  `json:"key"`
	Value *string `json:"value"`
}

func (s ServerTextSet) Valid() bool {
	if s.Value == nil || !utf8.ValidString(*s.Value) || strings.ContainsRune(*s.Value, '\x00') {
		return false
	}
	switch s.Key {
	case "server_name":
		return len(*s.Value) <= 256 && !strings.ContainsAny(*s.Value, "\r\n")
	case "motd", "announcement", "server_rules":
		return len(*s.Value) <= 64*1024
	default:
		return false
	}
}

// ServerTextResult confirms one saved value without echoing operator content.
// ContentHash is the SHA-256 of the submitted UTF-8 bytes, including empty text.
type ServerTextResult struct {
	Key         string `json:"key"`
	ContentHash string `json:"content_hash"`
}

// MaxServerBroadcastBytes bounds a one-time administrative alert.
const MaxServerBroadcastBytes = 2048

// ServerBroadcast is a one-time alert to every connected member. Unlike the
// stored announcement it is not persisted or replayed on login.
type ServerBroadcast struct {
	Text string `json:"text"`
}

func (b ServerBroadcast) Valid() bool {
	return strings.TrimSpace(b.Text) != "" && len(b.Text) <= MaxServerBroadcastBytes &&
		utf8.ValidString(b.Text) && !strings.ContainsRune(b.Text, '\x00')
}

// ServerBroadcastResult confirms a sent alert without echoing its content.
type ServerBroadcastResult struct {
	ContentHash string `json:"content_hash"`
}
