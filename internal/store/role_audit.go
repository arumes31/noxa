package store

import (
	"context"
	"database/sql"
	"slices"

	"noxa/internal/authorization"
)

func roleAuditDetail(before, after authorization.RolePolicy, change authorization.RoleChange) AuditDetail {
	detail := AuditDetail{Version: AuditDetailVersion, Revision: after.Revision, Channels: []int64{}}
	switch change.Kind {
	case authorization.RoleCreate, authorization.RoleUpdate:
		detail.Before, detail.After = auditRole(before, change.Role.ID), auditRole(after, change.Role.ID)
	case authorization.RoleDelete:
		detail.Before, detail.After = auditRole(before, change.RoleID), auditRole(after, change.RoleID)
	case authorization.RolesReorder:
		detail.Before, detail.After = before.Roles, after.Roles
	case authorization.MemberRolesSet:
		detail.Before, detail.After = auditMember(before, change.UserID), auditMember(after, change.UserID)
	case authorization.ChannelAccessSet:
		old, next := auditChannel(before, change.Channel.ChannelID), auditChannel(after, change.Channel.ChannelID)
		detail.Before, detail.After = old, next
		detail.Channels = auditChannelScopes(old, next)
	case authorization.DefaultMemberRoleSet:
		detail.Before = map[string]int64{"default_member_role_id": before.DefaultMemberRoleID}
		detail.After = map[string]int64{"default_member_role_id": after.DefaultMemberRoleID}
	case authorization.OwnerTransfer:
		detail.Before = map[string]int64{"owner_id": before.OwnerID}
		detail.After = map[string]int64{"owner_id": after.OwnerID}
	}
	return detail
}

func auditRole(policy authorization.RolePolicy, id int64) *authorization.Role {
	for _, role := range policy.Roles {
		if role.ID == id {
			return &role
		}
	}
	return nil
}

func auditMember(policy authorization.RolePolicy, id int64) authorization.RoleMember {
	for _, member := range policy.Members {
		if member.UserID == id {
			return member
		}
	}
	return authorization.RoleMember{UserID: id, RoleIDs: []int64{}}
}

func auditChannel(policy authorization.RolePolicy, id int64) *authorization.ChannelPolicy {
	for _, channel := range policy.Channels {
		if channel.ChannelID == id {
			return &channel
		}
	}
	return nil
}

func auditChannelScopes(channels ...*authorization.ChannelPolicy) []int64 {
	ids := []int64{}
	for _, channel := range channels {
		if channel == nil {
			continue
		}
		ids = append(ids, channel.ChannelID)
		if channel.ParentID > 0 {
			ids = append(ids, channel.ParentID)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

type channelAuditState struct {
	Access   *authorization.ChannelPolicy `json:"access"`
	Settings RoleChannelSettings          `json:"settings"`
	Type     int                          `json:"channel_type"`
}

// Read only display/edit metadata. Passwords, hashes and encryption material
// must never be selected into an audit object, even for an owner-only record.
func readChannelAuditState(ctx context.Context, tx *sql.Tx, policy authorization.RolePolicy, id int64) (*channelAuditState, error) {
	access := auditChannel(policy, id)
	if access == nil {
		return nil, nil
	}
	result := &channelAuditState{Access: access}
	s := &result.Settings
	err := tx.QueryRowContext(ctx, `SELECT name,COALESCE(topic,''),COALESCE(description,''),order_index,
		COALESCE(max_clients,0),slow_mode_seconds,opus_bitrate,opus_fec,opus_dtx,opus_stereo,channel_type
		FROM channels WHERE id=$1`, id).Scan(&s.Name, &s.Topic, &s.Description, &s.OrderIndex, &s.MaxClients,
		&s.SlowModeSeconds, &s.OpusBitrate, &s.OpusFEC, &s.OpusDTX, &s.OpusStereo, &result.Type)
	return result, err
}
