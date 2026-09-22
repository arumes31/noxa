package main

import (
	"fmt"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// SetRoleChannelIconForTab waits for storage and validates the target acknowledgement.
func (a *App) SetRoleChannelIconForTab(tabID string, channelID int64, dataBase64 string, copyFromChannelID int64) (netproto.RoleChannelIconSaved, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.RoleChannelIconSaved{}, err
	}
	return m.setRoleChannelIcon(channelID, dataBase64, copyFromChannelID)
}

func (m *connManager) setRoleChannelIcon(channelID int64, dataBase64 string, copyFromChannelID int64) (netproto.RoleChannelIconSaved, error) {
	if channelID < 1 || copyFromChannelID < 0 || (copyFromChannelID != 0 && dataBase64 != "") {
		return netproto.RoleChannelIconSaved{}, fmt.Errorf("invalid channel icon request")
	}
	f, err := m.request(netproto.MsgRoleChannelIconSet, netproto.MsgRoleChannelIconSaved,
		netproto.ChannelIconSet{ChannelID: channelID, DataBase64: dataBase64, CopyFromChannelID: copyFromChannelID}, 15*time.Second)
	if err != nil {
		return netproto.RoleChannelIconSaved{}, err
	}
	var result netproto.RoleChannelIconSaved
	if err := netproto.Decode(f, &result); err != nil {
		return result, err
	}
	if result.ChannelID != channelID {
		return netproto.RoleChannelIconSaved{}, fmt.Errorf("icon acknowledgement does not match the channel")
	}
	return result, nil
}

// RemoveRoleBanForTab waits for the selected ban's committed removal.
func (a *App) RemoveRoleBanForTab(tabID string, banID int64) (netproto.RoleBanRemoved, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.RoleBanRemoved{}, err
	}
	f, err := m.request(netproto.MsgRoleBanRemove, netproto.MsgRoleBanRemoved, netproto.BanRemove{BanID: banID}, 15*time.Second)
	if err != nil {
		return netproto.RoleBanRemoved{}, err
	}
	var result netproto.RoleBanRemoved
	if err := netproto.Decode(f, &result); err != nil {
		return result, err
	}
	if result.BanID != banID {
		return netproto.RoleBanRemoved{}, fmt.Errorf("ban removal acknowledgement does not match the request")
	}
	return result, nil
}

func (a *App) RoleChannelState(query netproto.RoleChannelQuery) (netproto.RoleChannelState, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.RoleChannelState{}, err
	}
	return m.roleChannelState(query)
}

// RoleChannelStateForTab rejects requests from an editor opened on another server.
func (a *App) RoleChannelStateForTab(tabID string, query netproto.RoleChannelQuery) (netproto.RoleChannelState, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.RoleChannelState{}, err
	}
	return m.roleChannelState(query)
}

func (m *connManager) roleChannelState(query netproto.RoleChannelQuery) (netproto.RoleChannelState, error) {
	f, err := m.request(netproto.MsgRoleChannelQuery, netproto.MsgRoleChannelState, query, 10*time.Second)
	if err != nil {
		return netproto.RoleChannelState{}, err
	}
	var response netproto.RoleChannelState
	err = decodeJSON(f, &response)
	return response, err
}

func (a *App) ChangeRoleChannel(change netproto.RoleChannelChange) (netproto.RoleChannelResult, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.RoleChannelResult{}, err
	}
	return m.changeRoleChannel(change)
}

// ChangeRoleChannelForTab rejects requests from an editor opened on another server.
func (a *App) ChangeRoleChannelForTab(tabID string, change netproto.RoleChannelChange) (netproto.RoleChannelResult, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.RoleChannelResult{}, err
	}
	return m.changeRoleChannel(change)
}

func (m *connManager) changeRoleChannel(change netproto.RoleChannelChange) (netproto.RoleChannelResult, error) {
	f, err := m.request(netproto.MsgRoleChannelChange, netproto.MsgRoleChannelResult, change, 35*time.Second)
	if err != nil {
		return netproto.RoleChannelResult{}, err
	}
	var response netproto.RoleChannelResult
	err = decodeJSON(f, &response)
	return response, err
}

func (a *App) SetMemberVoice(change netproto.MemberVoiceSet) (netproto.MemberVoiceState, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.MemberVoiceState{}, err
	}
	return m.setMemberVoice(change)
}

// SetMemberVoiceForTab rejects requests from an editor opened on another server.
func (a *App) SetMemberVoiceForTab(tabID string, change netproto.MemberVoiceSet) (netproto.MemberVoiceState, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.MemberVoiceState{}, err
	}
	return m.setMemberVoice(change)
}

func (m *connManager) setMemberVoice(change netproto.MemberVoiceSet) (netproto.MemberVoiceState, error) {
	f, err := m.request(netproto.MsgMemberVoiceSet, netproto.MsgMemberVoiceState, change, 10*time.Second)
	if err != nil {
		return netproto.MemberVoiceState{}, err
	}
	var response netproto.MemberVoiceState
	err = decodeJSON(f, &response)
	return response, err
}

func (a *App) RoleMembers(query authorization.MemberQuery) (authorization.MemberPage, error) {
	m, err := a.requireCM()
	if err != nil {
		return authorization.MemberPage{}, err
	}
	return m.roleMembers(query)
}

// RoleMembersForTab rejects requests from an editor opened on another server.
func (a *App) RoleMembersForTab(tabID string, query authorization.MemberQuery) (authorization.MemberPage, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return authorization.MemberPage{}, err
	}
	return m.roleMembers(query)
}

func (m *connManager) roleMembers(query authorization.MemberQuery) (authorization.MemberPage, error) {
	f, err := m.request(netproto.MsgRoleMemberQuery, netproto.MsgRoleMembers, query, 10*time.Second)
	if err != nil {
		return authorization.MemberPage{}, err
	}
	var response authorization.MemberPage
	err = decodeJSON(f, &response)
	return response, err
}

// RoleState fetches role administration or the selected channel's access editor.
func (a *App) RoleState(channelID int64) (netproto.RoleState, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.RoleState{}, err
	}
	return m.roleState(channelID)
}

// RoleStateForTab rejects requests from an editor opened on another server.
func (a *App) RoleStateForTab(tabID string, channelID int64) (netproto.RoleState, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.RoleState{}, err
	}
	return m.roleState(channelID)
}

func (m *connManager) roleState(channelID int64) (netproto.RoleState, error) {
	f, err := m.request(netproto.MsgRoleQuery, netproto.MsgRoleState, netproto.RoleQuery{ChannelID: channelID}, 10*time.Second)
	if err != nil {
		return netproto.RoleState{}, err
	}
	var response netproto.RoleState
	err = decodeJSON(f, &response)
	return response, err
}

// RoleChange waits for the committed revision; the UI must preserve its draft on
// error and refresh authoritative state only after the acknowledgement arrives.
func (a *App) RoleChange(change authorization.RoleChange) (netproto.RoleChangeResult, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.RoleChangeResult{}, err
	}
	return m.roleChange(change)
}

// RoleChangeForTab rejects requests from an editor opened on another server.
func (a *App) RoleChangeForTab(tabID string, change authorization.RoleChange) (netproto.RoleChangeResult, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.RoleChangeResult{}, err
	}
	return m.roleChange(change)
}

func (m *connManager) roleChange(change authorization.RoleChange) (netproto.RoleChangeResult, error) {
	f, err := m.request(netproto.MsgRoleChange, netproto.MsgRoleChangeResult, change, 10*time.Second)
	if err != nil {
		return netproto.RoleChangeResult{}, err
	}
	var response netproto.RoleChangeResult
	err = decodeJSON(f, &response)
	return response, err
}

func (a *App) CheckAccess(query netproto.AccessCheck) (netproto.AccessCheckResult, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.AccessCheckResult{}, err
	}
	return m.checkAccess(query)
}

// PreviewChannelAccessForTab evaluates an unsaved channel policy without effects.
func (a *App) PreviewChannelAccessForTab(tabID string, query netproto.ChannelAccessPreview) (authorization.ChannelAccessImpact, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return authorization.ChannelAccessImpact{}, err
	}
	f, err := m.request(netproto.MsgChannelAccessPreview, netproto.MsgChannelAccessImpact, query, 10*time.Second)
	if err != nil {
		return authorization.ChannelAccessImpact{}, err
	}
	var response authorization.ChannelAccessImpact
	if err := decodeJSON(f, &response); err != nil {
		return authorization.ChannelAccessImpact{}, err
	}
	revision, channelID := query.Change.ExpectedRevision, query.Change.Channel.ChannelID
	if query.Tree != nil {
		revision, channelID = query.Tree.ExpectedRevision, query.Tree.ChannelID
	}
	if query.ScopeChannelID != 0 {
		channelID = query.ScopeChannelID
	}
	if response.Revision != revision || response.ChannelID != channelID {
		return authorization.ChannelAccessImpact{}, fmt.Errorf("access preview scope changed; refresh the editor")
	}
	return response, nil
}

// CheckAccessForTab rejects requests from an editor opened on another server.
func (a *App) CheckAccessForTab(tabID string, query netproto.AccessCheck) (netproto.AccessCheckResult, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.AccessCheckResult{}, err
	}
	return m.checkAccess(query)
}

func (m *connManager) checkAccess(query netproto.AccessCheck) (netproto.AccessCheckResult, error) {
	f, err := m.request(netproto.MsgAccessCheck, netproto.MsgAccessCheckResult, query, 10*time.Second)
	if err != nil {
		return netproto.AccessCheckResult{}, err
	}
	var response netproto.AccessCheckResult
	err = decodeJSON(f, &response)
	return response, err
}
