# Lobby and managed public channels

Lobby is the permanent home for connected users outside a voice channel. It has a house icon, cannot be deleted or edited, and provides neither channel chat nor voice/media/file access. Direct messages and other accessible channel chats remain available. Leaving voice returns to Lobby without changing microphone/deafen settings or playing the own-channel leave announcement.

The server keeps two **empty** Public subchannels below Lobby in addition to occupied rooms. For example, two occupied public rooms produce four rooms in total. Membership changes trigger coalesced maintenance; a five-second fallback checks occupancy in memory. Unchanged pools cause no database writes. Creation/deletion and permission updates commit together under the authorization lifecycle gate; occupied rooms are protected from concurrent cleanup.

On upgrade, the server adopts the existing root named Lobby. Its direct, password-free, non-text leaf children named Public1, Public2, etc. become managed rooms. Existing permissions on adopted rooms are retained. Other custom channels remain unchanged. If no Lobby exists, one is created. New Public rooms receive public chat, voice, camera and screen permissions. Managed rooms cannot be renamed, moved or manually deleted; their permissions remain configurable.

Surplus empty managed rooms are deleted, highest numbered first; channel history and other channel-owned data follow the existing channel deletion rules. Use an ordinary custom channel for a persistent conversation. The lowest available Public number is reused when adding capacity. A restart considers all rooms empty before accepting connections and retains two managed rooms.

For protocol compatibility, neutral membership remains channel ID 0. The persisted Lobby has a positive ID and `SystemKind: "home"`; the UI displays neutral users under that anchor. Joining its positive ID also returns to neutral membership. Public rooms carry `SystemKind: "public"`. These are server-owned lifecycle markers, not client-editable permission attributes.
