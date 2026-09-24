package netproto

const MsgAssetMutationSaved MessageType = 159

// AssetMutationSaved confirms a completed avatar, branding or emoji mutation.
// Operation is the original request type; emoji operations echo their names.
// ClientID identifies the authenticated connection that submitted the change.
type AssetMutationSaved struct {
	Operation MessageType `json:"operation"`
	ClientID  string      `json:"client_id"`
	Name      string      `json:"name,omitempty"`
	NewName   string      `json:"new_name,omitempty"`
}
