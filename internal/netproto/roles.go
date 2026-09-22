package netproto

import "noxa/internal/authorization"

const (
	MsgRoleQuery         MessageType = 133
	MsgRoleState         MessageType = 134
	MsgRoleChange        MessageType = 135
	MsgRoleChangeResult  MessageType = 136
	MsgAccessCheck       MessageType = 137
	MsgAccessCheckResult MessageType = 138
	MsgRoleMemberQuery   MessageType = 139
	MsgRoleMembers       MessageType = 140
	MsgMemberVoiceSet    MessageType = 141
	MsgMemberVoiceState  MessageType = 142
)

// MemberVoiceSet changes only the provided flags. ChannelID is the target's
// displayed scope; reject a stale menu after that session moves elsewhere.
type MemberVoiceSet struct {
	ClientID  string `json:"client_id"`
	ChannelID int64  `json:"channel_id"`
	Muted     *bool  `json:"muted,omitempty"`
	Deafened  *bool  `json:"deafened,omitempty"`
}

type MemberVoiceState struct {
	Revision  int64  `json:"revision"`
	ClientID  string `json:"client_id"`
	ChannelID int64  `json:"channel_id"`
	Muted     bool   `json:"muted"`
	Deafened  bool   `json:"deafened"`
}

// MemberDisconnect targets the displayed voice membership. The session and its
// independent chat/file permissions remain active after leaving the channel.
type MemberDisconnect struct {
	ClientID  string `json:"client_id"`
	ChannelID int64  `json:"channel_id"`
	Reason    string `json:"reason,omitempty"`
}

type MemberDisconnectResult struct {
	ClientID  string `json:"client_id"`
	ChannelID int64  `json:"channel_id"`
}

// RoleQuery requests server role administration (channel_id=0) or access
// administration for a specific channel. Responses never include hidden
// channel policies outside the caller's management scope.
type RoleQuery struct {
	ChannelID int64 `json:"channel_id"`
}

type RoleState struct {
	ImpactChannelIDs      []int64                        `json:"impact_channel_ids"`
	ParentAccessAvailable bool                           `json:"parent_access_available"`
	EffectiveOverrides    []authorization.RoleOverride   `json:"effective_overrides"`
	ParentOverrides       []authorization.RoleOverride   `json:"parent_overrides"`
	ActorID               int64                          `json:"actor_id"`
	Policy                authorization.RolePolicy       `json:"policy"`
	Capabilities          []authorization.CapabilityInfo `json:"capabilities"`
	ManageableRoleIDs     []int64                        `json:"manageable_role_ids"`
	GrantableCapabilities []authorization.Capability     `json:"grantable_capabilities"`
}

// RoleChangeResult is an acknowledgement of the committed revision. Callers
// refresh their scoped RoleState after success. EnforcementPending still means
// the change was saved; protected operations are closed pending server recovery.
type RoleChangeResult struct {
	Revision           int64 `json:"revision"`
	CreatedRoleID      int64 `json:"created_role_id,omitempty"`
	EnforcementPending bool  `json:"enforcement_pending,omitempty"`
}

type AccessCheck struct {
	UserID           int64                    `json:"user_id"`
	ChannelID        int64                    `json:"channel_id"`
	Capability       authorization.Capability `json:"capability"`
	ExpectedRevision int64                    `json:"expected_revision"`
}

type AccessCheckResult struct {
	Decision        authorization.RoleDecision `json:"decision"`
	CanManageMember bool                       `json:"can_manage_member"`
}
