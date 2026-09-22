package server

import "noxa/internal/netproto"

func (s *TCPServer) acknowledgeJoin(client *Client, msg netproto.JoinChannel) error {
	if !msg.AckRequested {
		return nil
	}
	return s.writeCommittedReply(client, netproto.MsgChannelJoined, netproto.ChannelJoined{ClientID: client.ID, ChannelID: msg.ChannelID})
}

func (s *TCPServer) acknowledgeMove(client *Client, msg netproto.MoveClient) error {
	if !msg.AckRequested {
		return nil
	}
	return s.writeCommittedReply(client, netproto.MsgClientMoved, netproto.ClientMoved{ClientID: msg.ClientID, ChannelID: msg.ChannelID})
}

func (s *TCPServer) acknowledgeRemoval(client *Client, msg netproto.KickClient, channelID int64, persistence netproto.BanPersistence, pending bool) error {
	if !msg.AckRequested {
		return nil
	}
	return s.writeCommittedReply(client, netproto.MsgClientRemoved, netproto.ClientRemoved{
		ClientID: msg.ClientID, FromServer: msg.FromServer, Ban: msg.Ban,
		ChannelID: channelID, Persistence: persistence, CleanupPending: pending,
	})
}
