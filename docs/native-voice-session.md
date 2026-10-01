# Native voice session ownership

ICE configuration, media limits, offers, answers and candidates use expected-tab
native methods. The native bridge rejects an empty, missing, offline or inactive
tab, captures its connection once and retains it through the operation. The
legacy bridge methods remain available for compatibility. ICE configuration
copies include nested URL lists so callers cannot mutate the cached settings.

Offers retain the existing request/answer protocol. Answer and candidate success
means transport submission only; it does not confirm server acceptance. This
change adds no acknowledgement protocol or automatic retry.

Voice startup captures its tab, server generation and voice epoch. Camera and
screen publication capture their tab before capture/negotiation awaits. Queued
offers and answers reject obsolete peers before further work. Late candidate
callbacks cannot submit through a replacement peer, and late received tracks
are stopped before creating attribution, video tiles or audio playback.

Startup has a cancelable owner token. Reset releases its promise immediately;
late completion cannot clear, report against or tear down a replacement start.
An installed peer with a pending first offer is still connecting. Initial media
limit reads and live limit refreshes have ten-second deadlines. A live refresh
waits for startup, applies current capture/encoder limits and requests an
ICE-restart offer when dimensions change. Failures reset only the owning peer.

The server retains confirmed whisper intent and receive-quality preference
when rebuilding the transport. It recreates permitted whisper tracks and
continues current per-packet authorization; it never drops into ordinary channel
routing solely because of a rebuild. Final voice teardown clears the controls.
See `media-resource-limits.md` for the complete live-update boundary and remaining
server management/persistence work.

Track cleanup checks ownership as well as the publisher/slot ID. An earlier
track ending cannot remove its replacement's attribution, tile or audio chain,
including when an ICE restart reuses the same track ID. Audio cleanup retains
entry identity so old-session callbacks cannot detach a replacement chain.

Native tests cover stale-tab rejection, held original-connection answers,
configuration copying and write failures. Browser regressions cover native
activation before frontend reset, queued signaling, late peer callbacks and
replacement microphone/camera/system-audio cleanup. Camera and screen fixtures
use explicit tab IDs; the screen-control workflow negotiates with an in-page
peer rather than assuming a workspace has an active voice connection.

Confirmed media controls are described in `native-media-controls.md`. Physical
capture/revocation/ICE rehearsal remains in `native-api-inventory.md` and
`../tasks/todo.md`. This is not a claim
of complete permission cutover or physical-device validation.
