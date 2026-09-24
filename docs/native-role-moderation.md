# Native role moderation acknowledgements

This documents implemented native role-mode contracts. Native role-model
negotiation is implemented; production role activation remains pending.

## Remove a ban

`RoleBanRemove` (147) accepts `{ "ban_id": <positive integer> }` and returns
`RoleBanRemoved` (148) with that same ID only after the database deletion succeeds.
The request requires a configured role Authority, current global `BanMembers`
permission and a non-revoked session. Normal dispatch also requires authentication
and accepted server rules. Missing authority, invalid IDs and failed authorization
or persistence return a protocol error, never a success acknowledgement.

Removing an already absent ID succeeds. Only that ID is deleted; other bans on
the same account or address remain. A timeout or disconnected socket leaves the
outcome uncertain: read the current ban list before retrying.

After successful persistence, the server attempts the existing canonical
`ban_remove` audit under a separate five-second context, including when the
request has just been canceled. That audit writer remains best effort; deletion
and audit are not one transaction. A separate bounded reply window preserves a
committed result after request cancellation.

`RemoveRoleBanForTab(tabID, banID)` captures the initiating native connection,
rejects a tab activation gap and verifies that the returned ID matches. The role
ban dialog freezes actions through confirmation, removal and reload. It reloads
only after acknowledgement; an uncertain failure requires an explicit reload.
Closed or replaced dialogs ignore late replies.

Legacy servers keep `BanRemove` (83) and `BanRemoveForTab` with their existing
write-only semantics. The role dialog does not use that path. This new native
operation does not itself add Query/SSH/gRPC ban-removal adapters.

## Save a channel icon

`RoleChannelIconSet` (149) accepts the existing `ChannelIconSet` shape: a positive
`channel_id` and either `data_base64` or a positive `copy_from_channel_id`.
`RoleChannelIconSaved` (150) returns the matching `channel_id` after the image
write and live state update succeed. A configured Authority, current target
`ManageChannels` permission and a non-revoked session are required. Copying also
requires current `ViewChannel` on the source. The policy lease and channel
lifecycle lock cover the write; failed storage never produces a success reply.

The server attempts a channel-scoped `channel_icon_set` audit under a separate
five-second context after saving. This existing audit writer is best effort,
separate from filesystem persistence. A separate bounded reply context preserves
the committed result after request cancellation.

`SetRoleChannelIconForTab` captures the initiating connection and checks the
acknowledged channel ID. The role channel editor offers upload, reuse and preview
in a separate dialog; saving an icon does not submit pending channel settings.
It waits for acknowledgement, blocks duplicate submissions and requires explicit
refresh after an uncertain failure. Closing or replacing the server dialog
discards late reads and image preparation. Legacy `ChannelIconSet` retains its
existing write-only behavior.

## Poke acceptance

Role-mode native `Poke` and `PokeForTab` calls request `PokeAccepted` and wait on
the captured connection. The response identifies the exact target connection;
malformed or mismatched responses fail the call. The existing native request
gate serializes this response type and closes a timed-out transport so a late
reply cannot satisfy a later action. No automatic retry is introduced.

The server sends acceptance only after the target's outgoing queue accepts the
event. Existing capability, visibility and cooldown checks remain in effect,
and queue failures return correlated errors without acceptance. This confirms
queueing, not recipient delivery: disconnects and delivery-time role revocation
can still suppress the event. Legacy clients that do not request an
acknowledgement retain their previous write-only behavior.

The poke dialog reports returned failures only in its originating server
generation. Native tests cover held replies, mismatches and tab switches; TCP
tests cover accepted/unrequested sends, denial, cooldown, invalid targets and
full or missing queues. Browser tests cover current and obsolete failures.

## Move, disconnect, kick and ban results

Role-mode `MoveClientForTab` and `KickClientForTab` now request explicit outcomes
on the connection captured at entry. `ClientMoved` (156) confirms the target
connection and committed destination channel. `ClientRemoved` (157) echoes the
target and submitted server/ban flags. Channel disconnect reports the actual
prior channel; server removal reports session revocation and whether resource
cleanup is pending. Ban outcomes separately identify `saved` versus
`unconfirmed` persistence. Both revoke the target account's matching sessions;
an unconfirmed result requires reading the ban list before retrying.

Malformed or mismatched replies fail the native call. Partial outcomes return
an explicit warning through the existing string-result API, stop remaining
batch actions and are shown without a misleading failure prefix. Server and
dialog generation guards keep results on the initiating server. No automatic
retry is added. A lost reply leaves the outcome unknown: refresh membership or
the ban list before deciding what to do next. Membership confirmation does not
promise every key, event or media notification reached its recipient.

These acknowledged moderation requests require role authority. Legacy requests
without `AckRequested` keep their previous behavior; an acknowledged request to
a legacy server is rejected before effects. This avoids claiming persisted bans
when no ban store is configured or completed revocation while legacy teardown
is still asynchronous.

Committed replies get a fresh five-second window covering both socket-writer
lock acquisition and transmission. The same bound now applies to chat mutation,
message-send, own-DM echo and poke acknowledgements. A canceled initiating
request does not erase a completed result's reply opportunity. Authorization and
hierarchy checks remain in the existing operation paths.

Controlled-effect tests verify held, failed, denied and unrequested operations;
kick cleanup failures report revocation rather than an uncommitted failure.
PostgreSQL tests hold the ban INSERT behind a real table lock and cover saved,
unconfirmed, pending, denied and unrequested outcomes, persisted rows and all
matching session revocations. Native tests cover response validation and tab
changes; browser tests cover pending batches, partial results and stale errors.
Self-join/leave acknowledgements are described below. Asset confirmations are
documented in `native-asset-mutations.md`; file mutation confirmations are in
`native-file-transfers.md`.

## Channel-disconnect source precondition

Role-mode single and batch menus capture each member's connection ID and channel
when opened. `DisconnectMemberForTab(tabID, clientID, expectedChannelID, reason)`
requires a positive displayed source channel and sends it as `expected_channel_id`
with the acknowledged kick-from-channel request. The existing unscoped native
channel-kick method rejects role-mode use instead of resolving a newer channel.
Legacy UI calls retain their original method and behavior.

The server requires the source for acknowledged channel disconnects, rejects
source fields on server kicks/bans, and checks the expected channel under the
target action lock. A member who moved while the dialog was open or while the
operation waited keeps their new membership. The returned prior channel must
exactly match the native request. Explicit source fields also require role
authority; a legacy server cannot silently ignore them. Older unrequested role
protocol calls retain their current-channel semantics pending full retirement.

Regression tests reproduce a stale channel-1 dialog removing channel-2
membership before the fix. TCP tests also cover source validation and movement
while waiting for the target lock. Native tests reject invalid tabs, targets,
sources and mismatched replies before reporting success. Browser tests freeze
single and batch targets through menu/dialog waits, stop on stale members, and
hide channel disconnect for an already unassigned member. Wails bindings expose
the new scoped API.

## Self-join and leave confirmation

Role-mode `JoinChannel` and `JoinChannelForTab` request `ChannelJoined` and wait
on the captured connection for the exact authenticated connection ID and
destination. Channel zero explicitly confirms leaving, including an already
unassigned session. An omitted channel field cannot confirm a leave. The UI
continues to take membership from server state; it does not optimistically move
the user while awaiting a result. Obsolete tab generations suppress late errors.

The server replies only after the existing join or leave path succeeds. Connect,
password, capacity, session and rules checks remain in that path. Leaving does
not require Connect. Requested acknowledgements require role authority; legacy
unrequested requests retain their wire behavior. Committed replies use the
shared bounded writer, and the native wait is 20 seconds. Confirmation describes
membership at the operation boundary, not delivery of keys, media or subsequent
membership events. A lost response remains an unknown outcome; no automatic retry
is introduced.

Controlled TCP tests hold the membership backend and cover success, failure,
Connect denial, permission-independent leave, repeated operations, state fallback
and legacy rejection. Native tests cover held denials, exact response matching,
missing fields, malformed replies and tab changes. A regression also reproduced
empty server error text becoming the native empty-string success signal; shared
request handling now supplies `server error` instead. Browser tests cover pending
join/leave, rejection feedback and replacement-server controls.
