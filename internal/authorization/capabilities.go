package authorization

// Capability is a named privilege in the role authorization model.
type Capability string

const (
	Administrator           Capability = "administrator"
	ViewChannel             Capability = "view_channel"
	ReadHistory             Capability = "read_history"
	SendMessages            Capability = "send_messages"
	MentionEveryone         Capability = "mention_everyone"
	ManageMessages          Capability = "manage_messages"
	BypassSlowmode          Capability = "bypass_slowmode"
	Connect                 Capability = "connect"
	Speak                   Capability = "speak"
	ShareCamera             Capability = "share_camera"
	ShareScreen             Capability = "share_screen"
	Whisper                 Capability = "whisper"
	PrioritySpeaker         Capability = "priority_speaker"
	UploadFiles             Capability = "upload_files"
	DownloadFiles           Capability = "download_files"
	ManageFiles             Capability = "manage_files"
	MoveMembers             Capability = "move_members"
	MuteMembers             Capability = "mute_members"
	DeafenMembers           Capability = "deafen_members"
	DisconnectMembers       Capability = "disconnect_members"
	KickMembers             Capability = "kick_members"
	BanMembers              Capability = "ban_members"
	ManageChannels          Capability = "manage_channels"
	ManageChannelAccess     Capability = "manage_channel_access"
	CreateTemporaryChannels Capability = "create_temporary_channels"
	BypassChannelPassword   Capability = "bypass_channel_password"
	ManageRoles             Capability = "manage_roles"
	ManageServer            Capability = "manage_server"
	ManageEmoji             Capability = "manage_emoji"
	ManageChatFilters       Capability = "manage_chat_filters"
	ViewAuditLog            Capability = "view_audit_log"
	ViewConnectionInfo      Capability = "view_connection_info"
	ViewRemoteAddresses     Capability = "view_remote_addresses"
	RecordChannel           Capability = "record_channel"
	UploadAvatar            Capability = "upload_avatar"
	PokeMembers             Capability = "poke_members"
)

// CapabilityInfo drives validation and the localized permission editor.
type CapabilityInfo struct {
	Key      Capability   `json:"key"`
	Group    string       `json:"group"`
	English  string       `json:"en"`
	German   string       `json:"de"`
	Channel  bool         `json:"channel"`
	Requires []Capability `json:"requires,omitempty"`
}

var capabilityCatalog = []CapabilityInfo{
	{Administrator, "server", "Administrator", "Administrator", false, nil},
	{ViewChannel, "access", "View channel", "Kanal sehen", true, nil},
	{ReadHistory, "text", "Read message history", "Nachrichtenverlauf lesen", true, []Capability{ViewChannel}},
	{SendMessages, "text", "Send messages", "Nachrichten senden", true, []Capability{ViewChannel}},
	{MentionEveryone, "text", "Mention everyone", "Alle erwähnen", true, []Capability{SendMessages}},
	{ManageMessages, "text", "Manage messages", "Nachrichten verwalten", true, []Capability{ViewChannel}},
	{BypassSlowmode, "text", "Bypass slowmode", "Langsamen Modus umgehen", true, []Capability{SendMessages}},
	{Connect, "voice", "Connect to voice", "Sprachkanal betreten", true, []Capability{ViewChannel}},
	{Speak, "voice", "Speak", "Sprechen", true, []Capability{Connect}},
	{ShareCamera, "voice", "Share camera", "Kamera teilen", true, []Capability{Connect}},
	{ShareScreen, "voice", "Share screen", "Bildschirm teilen", true, []Capability{Connect}},
	{Whisper, "voice", "Whisper", "Flüstern", true, []Capability{Speak}},
	{PrioritySpeaker, "voice", "Priority speaker", "Vorrangiger Sprecher", true, []Capability{Speak}},
	{UploadFiles, "files", "Upload files", "Dateien hochladen", true, []Capability{ViewChannel}},
	{DownloadFiles, "files", "Download files", "Dateien herunterladen", true, []Capability{ViewChannel}},
	{ManageFiles, "files", "Manage others' files", "Dateien anderer verwalten", true, []Capability{ViewChannel}},
	{MoveMembers, "moderation", "Move members", "Mitglieder verschieben", true, []Capability{ViewChannel}},
	{MuteMembers, "moderation", "Mute members", "Mitglieder stummschalten", true, []Capability{ViewChannel}},
	{DeafenMembers, "moderation", "Deafen members", "Mitglieder gehörlos schalten", true, []Capability{ViewChannel}},
	{DisconnectMembers, "moderation", "Disconnect members from voice", "Mitglieder aus Sprachkanal entfernen", true, []Capability{ViewChannel}},
	{KickMembers, "moderation", "Kick members from server", "Mitglieder vom Server entfernen", false, nil},
	{BanMembers, "moderation", "Ban members", "Mitglieder sperren", false, nil},
	{ManageChannels, "access", "Manage channels", "Kanäle verwalten", true, []Capability{ViewChannel}},
	{ManageChannelAccess, "access", "Manage channel access", "Kanalzugriff verwalten", true, []Capability{ViewChannel}},
	{CreateTemporaryChannels, "access", "Create temporary channels", "Temporäre Kanäle erstellen", true, []Capability{ViewChannel}},
	{BypassChannelPassword, "access", "Bypass channel password", "Kanalpasswort umgehen", true, []Capability{ViewChannel}},
	{ManageRoles, "server", "Manage roles", "Rollen verwalten", false, nil},
	{ManageServer, "server", "Manage server", "Server verwalten", false, nil},
	{ManageEmoji, "server", "Manage emoji", "Emoji verwalten", false, nil},
	{ManageChatFilters, "server", "Manage chat filters", "Chatfilter verwalten", false, nil},
	{ViewAuditLog, "server", "View audit log", "Prüfprotokoll ansehen", false, nil},
	{ViewConnectionInfo, "server", "View connection statistics and invisible members", "Verbindungsstatistik und unsichtbare Mitglieder ansehen", false, nil},
	{ViewRemoteAddresses, "server", "View remote addresses", "Remote-Adressen ansehen", false, nil},
	{RecordChannel, "voice", "Record channel", "Kanal aufnehmen", true, []Capability{Connect}},
	{UploadAvatar, "profile", "Upload avatar", "Avatar hochladen", false, nil},
	{PokeMembers, "profile", "Poke members", "Mitglieder anstupsen", false, nil},
}

// Capabilities returns a defensive copy in editor display order.
func Capabilities() []CapabilityInfo {
	result := append([]CapabilityInfo(nil), capabilityCatalog...)
	for i := range result {
		result[i].Requires = append([]Capability(nil), result[i].Requires...)
	}
	return result
}

func capabilityInfo(key Capability) (CapabilityInfo, bool) {
	for _, info := range capabilityCatalog {
		if info.Key == key {
			return info, true
		}
	}
	return CapabilityInfo{}, false
}
