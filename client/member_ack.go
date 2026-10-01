package main

import (
	"fmt"
	"time"

	"noxa/internal/netproto"
)

func (cm *connManager) usesRoleAuthorization() bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.authorizationModel == netproto.AuthorizationModelRolesV1
}

func (cm *connManager) joinChannelAcknowledged(msg netproto.JoinChannel) error {
	msg.AckRequested = cm.usesRoleAuthorization()
	if !msg.AckRequested {
		return cm.write(netproto.MsgJoinChannel, msg)
	}
	clientID := cm.clientIDSnapshot()
	if msg.ChannelID < 0 || clientID == "" {
		return fmt.Errorf("invalid membership request; refresh the session")
	}
	f, err := cm.request(netproto.MsgJoinChannel, netproto.MsgChannelJoined, msg, 20*time.Second)
	if err != nil {
		return err
	}
	// Require an explicit channel field: an omitted destination must not
	// accidentally confirm a leave request (channel zero).
	var joined struct {
		ClientID  string `json:"client_id"`
		ChannelID *int64 `json:"channel_id"`
	}
	if err := netproto.Decode(f, &joined); err != nil {
		return err
	}
	if joined.ClientID != clientID || joined.ChannelID == nil || *joined.ChannelID != msg.ChannelID {
		return fmt.Errorf("membership acknowledgement does not match the request; refresh membership before retrying")
	}
	return nil
}

func (cm *connManager) moveClientAcknowledged(msg netproto.MoveClient) error {
	msg.AckRequested = cm.usesRoleAuthorization()
	if !msg.AckRequested {
		return cm.write(netproto.MsgMoveClient, msg)
	}
	f, err := cm.request(netproto.MsgMoveClient, netproto.MsgClientMoved, msg, 20*time.Second)
	if err != nil {
		return err
	}
	var moved netproto.ClientMoved
	if err := netproto.Decode(f, &moved); err != nil {
		return err
	}
	if msg.ClientID == "" || msg.ChannelID <= 0 || moved.ClientID != msg.ClientID || moved.ChannelID != msg.ChannelID {
		return fmt.Errorf("move acknowledgement does not match the request; refresh membership before retrying")
	}
	return nil
}

func (cm *connManager) kickClientAcknowledged(msg netproto.KickClient) error {
	msg.AckRequested = cm.usesRoleAuthorization()
	if !msg.AckRequested {
		return cm.write(netproto.MsgKickClient, msg)
	}
	if !msg.FromServer && !msg.Ban && msg.ExpectedChannelID <= 0 {
		return fmt.Errorf("choose a member's current channel before disconnecting")
	}
	// Removal can outlive the initial policy deadline while completing bounded
	// cleanup, audit and the committed reply. Do not retry on a lost response.
	f, err := cm.request(netproto.MsgKickClient, netproto.MsgClientRemoved, msg, 40*time.Second)
	if err != nil {
		return err
	}
	var removed netproto.ClientRemoved
	if err := netproto.Decode(f, &removed); err != nil {
		return err
	}
	if !removed.Matches(msg) {
		return fmt.Errorf("removal acknowledgement does not match the request; refresh before retrying")
	}
	if removed.Persistence == netproto.BanUnconfirmed {
		if removed.CleanupPending {
			return fmt.Errorf("sessions revoked; ban persistence is unconfirmed and resource cleanup is pending; refresh the ban list before retrying")
		}
		return fmt.Errorf("sessions revoked; ban persistence is unconfirmed; refresh the ban list before retrying")
	}
	if removed.CleanupPending {
		if removed.Ban {
			return fmt.Errorf("ban saved and sessions revoked; resource cleanup is pending")
		}
		return fmt.Errorf("session revoked; resource cleanup is pending")
	}
	return nil
}
