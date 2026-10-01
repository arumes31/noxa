package server

import "noxa/internal/netproto"

func (s *TCPServer) acknowledgeMediaControl(client *Client, requested bool, saved netproto.MediaControlSaved) error {
	if !requested {
		return nil
	}
	saved.ClientID = client.ID
	if saved.UniqueIDs == nil {
		saved.UniqueIDs = []string{}
	}
	if saved.ChannelIDs == nil {
		saved.ChannelIDs = []int64{}
	}
	return s.writeCommittedReply(client, netproto.MsgMediaControlSaved, saved)
}
