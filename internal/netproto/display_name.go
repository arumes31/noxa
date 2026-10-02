package netproto

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MsgDisplayNameSet    MessageType = 184
	MsgDisplayNameSaved  MessageType = 185
	MaxDisplayNameLength             = 64
)

type DisplayNameSet struct {
	Nickname string `json:"nickname"`
}

// DisplayNameSaved confirms a public session name, never an account rename.
type DisplayNameSaved struct {
	ClientID string `json:"client_id"`
	Nickname string `json:"nickname"`
}

// NormalizeDisplayName validates a public name before it reaches session state.
func NormalizeDisplayName(name string) (string, error) {
	if !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) {
		return "", errors.New("display name must not contain control characters")
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxDisplayNameLength {
		return "", errors.New("display name must contain 1–64 characters")
	}
	return name, nil
}
