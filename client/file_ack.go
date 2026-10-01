package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"noxa/internal/netproto"
)

func (cm *connManager) mutateFile(want netproto.FileMutationSaved) error {
	ack := cm.usesRoleAuthorization()
	var msg any
	switch want.Operation {
	case netproto.MsgFileDelete:
		msg = netproto.FileDelete{AckRequested: ack, ChannelID: want.ChannelID, Folder: want.Folder, Name: want.Name}
	case netproto.MsgFileRename:
		msg = netproto.FileRename{AckRequested: ack, ChannelID: want.ChannelID, Folder: want.Folder, Name: want.Name,
			NewChannelID: want.NewChannelID, NewFolder: want.NewFolder, NewName: want.NewName}
	default:
		return fmt.Errorf("unsupported file mutation")
	}
	if !ack {
		return cm.write(want.Operation, msg)
	}
	want.ClientID = cm.clientIDSnapshot()
	if want.ClientID == "" || want.ChannelID < 0 || want.Name == "" || want.NewChannelID < 0 {
		return fmt.Errorf("invalid file request; refresh the file browser")
	}
	f, err := cm.request(want.Operation, netproto.MsgFileMutationSaved, msg, 20*time.Second)
	if err != nil {
		return err
	}
	// Zero is a valid global/source-channel value and folders can be empty.
	// Require the complete echoed tuple instead of accepting omitted/null fields
	// as those valid zero values.
	var fields map[string]json.RawMessage
	if err := netproto.Decode(f, &fields); err != nil {
		return err
	}
	for _, key := range []string{"operation", "client_id", "channel_id", "folder", "name", "new_channel_id", "new_folder", "new_name"} {
		if len(fields[key]) == 0 || bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) {
			return fmt.Errorf("incomplete file acknowledgement; refresh before retrying")
		}
	}
	var saved netproto.FileMutationSaved
	if err := netproto.Decode(f, &saved); err != nil {
		return err
	}
	if saved != want {
		return fmt.Errorf("file acknowledgement does not match the request; refresh before retrying")
	}
	return nil
}
