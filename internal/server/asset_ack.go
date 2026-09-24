package server

import "noxa/internal/netproto"

func (s *TCPServer) acknowledgeAsset(client *Client, requested bool, operation netproto.MessageType, name, newName string) error {
	if !requested {
		return nil
	}
	return s.writeCommittedReply(client, netproto.MsgAssetMutationSaved, netproto.AssetMutationSaved{
		Operation: operation, ClientID: client.ID, Name: name, NewName: newName,
	})
}
