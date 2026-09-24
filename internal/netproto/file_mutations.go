package netproto

const MsgFileMutationSaved MessageType = 160

// FileMutationSaved confirms the file backend completed the submitted operation.
// It echoes the request paths and destination sentinel (NewChannelID zero means
// the source channel). Delete confirms logical record removal, not secure blob
// erasure; the existing backend performs blob cleanup on a best-effort basis.
type FileMutationSaved struct {
	Operation    MessageType `json:"operation"`
	ClientID     string      `json:"client_id"`
	ChannelID    int64       `json:"channel_id"`
	Folder       string      `json:"folder"`
	Name         string      `json:"name"`
	NewChannelID int64       `json:"new_channel_id"`
	NewFolder    string      `json:"new_folder"`
	NewName      string      `json:"new_name"`
}
