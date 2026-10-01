package server

import "noxa/internal/netproto"

func (s *TCPServer) acknowledgeFile(client *Client, requested bool, result netproto.FileMutationSaved) error {
	if !requested {
		return nil
	}
	result.ClientID = client.ID
	return s.writeCommittedReply(client, netproto.MsgFileMutationSaved, result)
}
