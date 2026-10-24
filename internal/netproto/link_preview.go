package netproto

const (
	MsgLinkPreviewRequest MessageType = 198
	MsgLinkPreviewResult  MessageType = 199
	CapabilityLinkPreview             = "server_link_previews_v1"
)

// Provider is youtube, x, a supported portal, or web (an HTTPS page).
type LinkPreviewRequest struct {
	Provider string `json:"provider"`
	Resource string `json:"resource"`
}

// Thumbnail is always a bounded, freshly encoded JPEG data URL; never a remote URL.
type LinkPreviewResult struct {
	Title       string `json:"title"`
	Author      string `json:"author"`
	Text        string `json:"text"`
	Description string `json:"description"`
	Thumbnail   string `json:"thumbnail"`
	LiveState   string `json:"live_state,omitempty"`
	Category    string `json:"category,omitempty"`
	StartsAt    string `json:"starts_at,omitempty"`
}
