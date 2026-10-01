package netproto

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxCustomMetadataKeyBytes = 128
const MaxCustomMetadataValueBytes = 4096

// CustomMetadataQuery addresses opaque annotation subjects, including retained
// legacy subjects that no longer have an account. It grants no account access.
type CustomMetadataQuery struct {
	UniqueID string `json:"unique_id"`
	AfterKey string `json:"after_key,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

func (q CustomMetadataQuery) Valid() bool {
	return validMetadataID(q.UniqueID) && (q.AfterKey == "" || validMetadataID(q.AfterKey)) && q.Limit >= 0 && q.Limit <= 100
}

type CustomMetadataEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type CustomMetadataPage struct {
	UniqueID     string                `json:"unique_id"`
	Entries      []CustomMetadataEntry `json:"entries"`
	NextAfterKey string                `json:"next_after_key,omitempty"`
}

// Exactly one of Value and Delete is supplied. Empty text is a stored value;
// deletion must be explicit and addresses only this exact subject/key pair.
type CustomMetadataChange struct {
	UniqueID string  `json:"unique_id"`
	Key      string  `json:"key"`
	Value    *string `json:"value,omitempty"`
	Delete   bool    `json:"delete,omitempty"`
}

func (c CustomMetadataChange) Valid() bool {
	if !validMetadataID(c.UniqueID) || !validMetadataID(c.Key) || c.Delete == (c.Value != nil) {
		return false
	}
	return c.Value == nil || (len(*c.Value) <= MaxCustomMetadataValueBytes && utf8.ValidString(*c.Value) && !strings.ContainsRune(*c.Value, 0))
}

type CustomMetadataResult struct {
	UniqueID string `json:"unique_id"`
	Key      string `json:"key"`
	Deleted  bool   `json:"deleted"`
}

func validMetadataID(value string) bool {
	return value != "" && len(value) <= MaxCustomMetadataKeyBytes && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}
