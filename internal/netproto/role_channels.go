package netproto

import "noxa/internal/authorization"

const (
	MsgRoleChannelQuery  MessageType = 143
	MsgRoleChannelState  MessageType = 144
	MsgRoleChannelChange MessageType = 145
	MsgRoleChannelResult MessageType = 146
)

// For creation, ChannelID is the parent (zero means server root). For all
// other operations it is the channel being managed.
type RoleChannelQuery struct {
	Kind      authorization.ChannelChangeKind `json:"kind"`
	ChannelID int64                           `json:"channel_id"`
}

type RoleChannelSettings struct {
	Name            string `json:"name"`
	Topic           string `json:"topic"`
	Description     string `json:"description"`
	OrderIndex      int    `json:"order_index"`
	MaxClients      int    `json:"max_clients"`
	SlowModeSeconds int    `json:"slow_mode_seconds"`
	OpusBitrate     int    `json:"opus_bitrate"`
	OpusFEC         bool   `json:"opus_fec"`
	OpusDTX         bool   `json:"opus_dtx"`
	OpusStereo      bool   `json:"opus_stereo"`
}

type RoleChannelOption struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	CanSync bool   `json:"can_sync"`
}

type RoleChannelState struct {
	ImpactChannelIDs      []int64                        `json:"impact_channel_ids"`
	Capabilities          []authorization.CapabilityInfo `json:"capabilities"`
	Revision              int64                          `json:"revision"`
	ChannelID             int64                          `json:"channel_id"`
	Name                  string                         `json:"name"`
	Settings              RoleChannelSettings            `json:"settings"`
	AffectedChannels      int                            `json:"affected_channels"`
	CanCreatePermanent    bool                           `json:"can_create_permanent"`
	CanCreateTemporary    bool                           `json:"can_create_temporary"`
	CanManageAccess       bool                           `json:"can_manage_access"`
	EveryoneID            int64                          `json:"everyone_id"`
	Roles                 []RoleChannelOption            `json:"roles"`
	GrantableCapabilities []authorization.Capability     `json:"grantable_capabilities"`
	Destinations          []RoleChannelOption            `json:"destinations"`
}

type RoleChannelAccess struct {
	Synced    bool                         `json:"synced"`
	Overrides []authorization.RoleOverride `json:"overrides"`
}

// An omitted access policy inherits the parent on creation. Moves keep
// effective access unless SyncToParent is explicitly true. No actor identity
// or prepared password hash is accepted from a client.
type RoleChannelChange struct {
	Kind             authorization.ChannelChangeKind `json:"kind"`
	ExpectedRevision int64                           `json:"expected_revision"`
	ChannelID        int64                           `json:"channel_id"`
	ParentID         int64                           `json:"parent_id"`
	SyncToParent     bool                            `json:"sync_to_parent"`
	// Move only. Omitted preserves the current order; zero is an explicit order.
	OrderIndex  *int32               `json:"order_index,omitempty"`
	Access      *RoleChannelAccess   `json:"access,omitempty"`
	Settings    *RoleChannelSettings `json:"settings,omitempty"`
	ChannelType int                  `json:"channel_type"`
	Password    string               `json:"password,omitempty"`
}

type RoleChannelResult struct {
	Revision           int64 `json:"revision"`
	ChannelID          int64 `json:"channel_id"`
	EnforcementPending bool  `json:"enforcement_pending"`
}
