// Package channels keeps the in-memory channel tree in step with the database
// and schedules the automatic cleanup of temporary channels when they become
// empty.
//
// A temporary channel (ChannelTypeTemporary) is deleted automatically after a
// configurable grace period once it has zero members. Semi-permanent and
// permanent channels persist until explicitly deleted.
package channels

import (
	"fmt"
)

// ChannelType distinguishes temporary, semi-permanent, and permanent channels.
// The integer values mirror the channel_type SMALLINT column in the channels
// table (see internal/store/migrations/001_init.sql).
type ChannelType int8

const (
	// ChannelTypeTemporary is a channel that is automatically deleted after a
	// grace period once it becomes empty.
	ChannelTypeTemporary = 0
	// ChannelTypeSemiPermanent is a channel that persists across restarts but
	// is not protected from explicit deletion.
	ChannelTypeSemiPermanent = 1
	// ChannelTypePermanent is a channel that persists across restarts and is
	// intended to be long-lived (e.g. the server's default channel).
	ChannelTypePermanent = 2
)

// String returns a human-readable name for the channel type: "temporary",
// "semi-permanent", or "permanent". Unknown values return "unknown".
func (t ChannelType) String() string {
	switch t {
	case ChannelTypeTemporary:
		return "temporary"
	case ChannelTypeSemiPermanent:
		return "semi-permanent"
	case ChannelTypePermanent:
		return "permanent"
	default:
		return "unknown"
	}
}

// Valid reports whether t is one of the defined channel types.
func (t ChannelType) Valid() bool {
	switch t {
	case ChannelTypeTemporary, ChannelTypeSemiPermanent, ChannelTypePermanent:
		return true
	default:
		return false
	}
}

// ParseChannelType validates an integer before converting it to ChannelType.
// It is used at wire and database boundaries where narrowing first could turn
// an out-of-range value such as 256 into a valid temporary-channel value.
func ParseChannelType(value int) (ChannelType, error) {
	switch value {
	case int(ChannelTypeTemporary):
		return ChannelTypeTemporary, nil
	case int(ChannelTypeSemiPermanent):
		return ChannelTypeSemiPermanent, nil
	case int(ChannelTypePermanent):
		return ChannelTypePermanent, nil
	default:
		return 0, fmt.Errorf("invalid channel type %d", value)
	}
}
