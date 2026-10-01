package main

import (
	"fmt"
	"time"

	"noxa/internal/netproto"
)

func (cm *connManager) mutateAsset(kind netproto.MessageType, name, newName, dataBase64 string) error {
	ack := cm.usesRoleAuthorization()
	var msg any
	switch kind {
	case netproto.MsgAvatarSet:
		msg = netproto.AvatarSet{AckRequested: ack, DataBase64: dataBase64}
	case netproto.MsgServerIconSet:
		msg = netproto.ServerIconSet{AckRequested: ack, DataBase64: dataBase64}
	case netproto.MsgServerBannerSet:
		msg = netproto.ServerBannerSet{AckRequested: ack, DataBase64: dataBase64}
	case netproto.MsgEmojiUpload:
		msg = netproto.EmojiUpload{AckRequested: ack, Name: name, DataBase64: dataBase64}
	case netproto.MsgEmojiDelete:
		msg = netproto.EmojiDelete{AckRequested: ack, Name: name}
	case netproto.MsgEmojiRename:
		msg = netproto.EmojiRename{AckRequested: ack, Name: name, NewName: newName}
	default:
		return fmt.Errorf("unsupported asset operation")
	}
	if !ack {
		return cm.write(kind, msg)
	}
	want := netproto.AssetMutationSaved{Operation: kind, ClientID: cm.clientIDSnapshot(), Name: name, NewName: newName}
	if want.ClientID == "" {
		return fmt.Errorf("invalid asset request; refresh the session")
	}
	f, err := cm.request(kind, netproto.MsgAssetMutationSaved, msg, 20*time.Second)
	if err != nil {
		return err
	}
	var saved netproto.AssetMutationSaved
	if err := netproto.Decode(f, &saved); err != nil {
		return err
	}
	if saved != want {
		return fmt.Errorf("asset acknowledgement does not match the request; refresh before retrying")
	}
	return nil
}
