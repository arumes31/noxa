# Integration access during the role replacement

Production uses `roles-v1`; there is no legacy integration fallback.

Role-mode tools must explicitly negotiate `roles-v1`: Query adds
`authorization_model=roles-v1` to login, SSH sets `NOXA_AUTHORIZATION_MODEL=roles-v1`
on each session channel, and every gRPC unary call sends the
`noxa-authorization-model: roles-v1` metadata header. See
[authorization model negotiation](authorization-model-negotiation.md) for exact
syntax, failure behavior and legacy compatibility.

## Account identity and eligibility

Migration 028 adds `users.integration_enabled`, defaulting to false for all
accounts. The operator's `adduser -integration` option enables integration login
for a newly provisioned account. It grants no roles or permissions. `-bot` does
not enable integration access. Repeating `adduser` for an existing
nickname makes no changes, including to this flag.

Role-mode integration login accepts a registered nickname or canonical unique
ID and the account password. The transport supplies the actual peer IP and its
shared login rate limiter. An authenticated principal is local to the issuing
auth service and connection; it cannot be supplied in a request body. Logout
and disconnect discard it. There are no accountless privileged service credentials.

Every protected operation rechecks account eligibility, the original account ID and
unique ID, and current server-wide identity/IP bans. Deleted-and-recreated
accounts cannot inherit an existing session. Equivalent IPv6 and mapped IPv4
ban spellings compare as addresses. Ban reasons are not disclosed at login.
Eligibility changes through offline maintenance require the serving process to
be stopped; live bans serialize with protected reads.

## Implemented Query and SSH commands

In role mode, `clientlist`, `channellist` and `channelinfo` use the same filtered
snapshot as the native client. Hidden channels/members are omitted; visible
children of hidden parents appear at the root. Unknown and hidden channel IDs
have the same response. Each command checks current policy, even on an existing
Query connection or a new channel within an existing SSH connection.

These responses retain the policy and membership/presence barriers through
serialization and transport delivery. Reads have a ten-second deadline,
cancellation-aware filtering/serialization, a 10,000-item source snapshot limit
(channels plus connected clients), and an 8 MiB serialized response limit.
Over-limit reads fail without delivering partial protected results. A blocked
SSH receive window closes the transport on deadline so it cannot hold the
policy barrier indefinitely. Larger deployments require a paginated API.

`serverinfo`, `clientinfo clid=<client_id>` and `serverconfig` return escaped
JSON `data` values in role mode. They recheck admission and retain policy and
metadata barriers through the same bounded delivery. Server information uses
native filtered counts, so hidden channels and their members do not increase
the reported totals. MOTD requires both the server's publication setting and
global ViewChannel. Other server information includes name, version, platform,
uptime and configured capacity.

Client information requires current target visibility; missing, hidden and
inaccessible invisible targets share the same denial. Connection statistics
require ViewConnectionInfo, and IP/port require ViewRemoteAddresses independently.
An integration is not a native session: another connection on the same account
does not get the native self-session exception. `serverconfig` requires
ManageServer and returns the native runtime capacity, timeout and audio settings.
These commands do not change metadata or configuration. Legacy-mode `serverinfo`
keeps its existing text response format.

`rolelist [cid=<channel_id>]` returns the native role-editor projection as an
escaped JSON `data` value. Omit `cid` for server role management, requiring
ManageRoles. A channel scope requires ManageChannelAccess there. Parent IDs are
omitted when the parent is hidden; parent access rules are returned only when
the actor may manage those rules. The complete projection, including nested
assignments and overrides, has a 10,000-item budget checked before cloning.

`rolechange data=<escaped JSON>` accepts the native RoleChange object, including
its required `expected_revision`. It supports role creation/update/deletion,
ordering, member assignments, channel access, default membership selection and
owner transfer under the same hierarchy and delegation checks as native clients.
There is no request field for the acting account. Unknown JSON fields, trailing
values and a missing revision are rejected. JSON values use ordinary ServerQuery
escaping, including `\s` for spaces and `\\` for backslashes.

For example, when the current revision is 7:

```text
rolechange data={"kind":"role_create","expected_revision":7,"role":{"name":"Support"}}
```

Changes use the same transactional store and audit actor as native clients.
Account admission is rechecked inside the exclusive policy barrier before the
commit. Conflict returns error 521 and requires refreshing; denied authority
returns 2568. Successful responses contain the committed revision and created
role ID when applicable. `enforcement_pending: true` still acknowledges a saved
change and must not be retried. Its response gets a separate ten-second delivery
window even if reconciliation exhausted the mutation's thirty-second deadline.

`rolemembers data=<escaped JSON>` accepts `expected_revision`, optional
`channel_id` (zero for server management), `search` (up to 100 bytes), and
`after_id` (zero for the first page). It returns at most 100 registered accounts,
including offline members and protected members the editor may inspect. Use the
last returned `user_id` as `after_id` while `more` is true. Entries contain only
identity, assigned role IDs and a `manageable` flag; no address or authentication
data is returned. Stale revision returns error 521.

`accesscheck data=<escaped JSON>` accepts `expected_revision`, `channel_id`,
target `user_id` (zero for guest), and `capability`. It explains the target's
saved role decision and prerequisites and reports `can_manage_member`
separately. It neither impersonates the target nor accesses protected content.
Both commands require ManageRoles globally or ManageChannelAccess in the chosen
channel, revalidate integration admission, and retain the policy/metadata
barriers through the same bounded JSON delivery as `rolelist`.

```text
rolemembers data={"expected_revision":7,"search":"Support","after_id":0}
accesscheck data={"expected_revision":7,"channel_id":2,"user_id":42,"capability":"speak"}
```

`channelquery data=<escaped JSON>` accepts `kind` (`channel_create`,
`channel_edit`, `channel_move`, or `channel_delete`) and `channel_id`. For
creation that ID is the parent, with zero meaning server root; otherwise it is
the managed channel. The response contains the current revision, authorized
settings, creation capabilities, assignable access roles or valid move
destinations. Hidden channels and unauthorized subtrees are rejected before
their metadata/counts are returned. The source policy and resource lists have
the same 10,000-item limits as other bounded integration projections.

`channelchange data=<escaped JSON>` accepts the native RoleChannelChange, with
`kind`, positive `expected_revision`, `channel_id`, `parent_id`,
`sync_to_parent`, optional `settings`/`access`, move-only `order_index`, and creation-only `channel_type`
and `password`. Creation types are 0 temporary, 1 semi-permanent and 2 permanent.
Create/edit require complete settings; move/delete omit them. Creation inherits
the parent when access is omitted. Moves keep effective access by default;
explicit synchronization requires the same additional access authority as the
native editor. A move may include a signed 32-bit `order_index` to update its
parent and sort order in the same resource/policy/revision/audit transaction.
Omitting it preserves the stored order; an explicit zero sets the order to zero.
Other mutation kinds reject this top-level field (create/edit use settings).
Existing transport command-line limits apply.

```text
channelquery data={"kind":"channel_create","channel_id":0}
channelchange data={"kind":"channel_create","expected_revision":7,"channel_type":2,"settings":{"name":"Support"}}
```

Channel admission is checked before password hashing and again under the
exclusive mutation barrier. Passwords are hashed on the server and omitted
from audit details. Channel resources, access policy, revision and audit commit
together; live reconciliation precedes the acknowledgement. Results contain the
committed `revision`, `channel_id`, and `enforcement_pending`, with the same
saved-change and bounded-reply semantics as `rolechange`.

`membervoice data=<escaped JSON>` accepts `client_id`, the member's current
positive `channel_id`, and one or both boolean fields `muted`/`deafened`.
Omitting a flag preserves it; explicit `false` clears it. Both requested changes
must be authorized before either takes effect. The actor needs current channel
visibility, the relevant MuteMembers/DeafenMembers capability and a higher role
than the target. Invisible targets additionally require ViewConnectionInfo.
Self-moderation and moderation of the owner are rejected.

```text
membervoice data={"client_id":"c-example","channel_id":2,"muted":false,"deafened":true}
```

The response contains `revision`, `client_id`, `channel_id`, `muted` and
`deafened`. This revision belongs to session voice state, not role policy.
Flags apply immediately to audio delivery, survive channel moves and clear on
disconnect; camera/screen video is independent. Admission and current policy
are checked on every operation under the same locks as native moderation.
The mutation has a ten-second context and its acknowledgement a separate
ten-second delivery window.

`membermove data=<escaped JSON>` accepts `client_id` and destination
`channel_id`. The actor needs MoveMembers in both the current source and
destination, a higher role than the target, and visibility of an invisible
target via ViewConnectionInfo when applicable. The target must be able to
Connect at the destination and must have accepted server rules. Capacity
limits apply even to the owner; a full destination returns error 521.

```text
membermove data={"client_id":"c-example","channel_id":3}
```

Movement uses the native membership, media, subscription and chat-key
lifecycle. The response echoes the committed `client_id`/`channel_id`;
it is an acknowledgement of that action, not a later snapshot. Audit records
identify the authenticated account and cover both source and destination.
Session mute/deafen flags survive the move. It uses the same ten-second effect
and separate acknowledgement window as `membervoice`.

`memberdisconnect data=<escaped JSON>` accepts `client_id`, its current positive
`channel_id`, and an optional `reason` of up to 4096 UTF-8 bytes. It requires
DisconnectMembers in that channel, higher hierarchy and invisible-target
visibility where applicable. Missing, protected, already-disconnected and
stale-channel targets return the same permission denial.

```text
memberdisconnect data={"client_id":"c-example","channel_id":3,"reason":"Take\sa\sbreak"}
```

It leaves voice through the native lifecycle, including normal key rotation
and subscription refresh, while preserving the server session, mute/deafen
flags, explicit chat subscriptions and independently granted file access. The
response identifies the committed `client_id` and former `channel_id`.
Notifications use the remaining operation deadline, including while waiting
for the socket writer. The source-scoped audit gets a separate five-second
attempt after commit, even if notification cancellation exhausted the operation
context; the acknowledgement then uses its separate bounded reply window.

Other protected commands currently return an explicit unsupported-command error
in role mode. They do not fall back to legacy administrative helpers. Basic
`login`, `logout`, `help`, `version` and `quit` remain available.

## Implemented gRPC operations

The loopback gRPC API supports `Control.Authenticate`, `Control.ListChannels`,
`Control.ChangeRoles`, `Control.ChangeChannel`, `Control.SetMemberVoice`,
`Control.MoveMember`, `Control.DisconnectMember`, `Control.KickMember`,
`Control.BanMember`, `Control.GetServerInfo`, `Control.GetClientInfo`,
`Control.GetServerConfig`, `Control.ListBans`, `Control.GetRoleState`,
`Control.ListRoleMembers`, `Control.CheckAccess`, `Control.GetChannelOptions`,
`Control.ListClients`, `Control.GetChannelInfo` and `Control.ListAuditLog`
in role mode. Authentication uses the same explicitly enabled integration
accounts and actual transport peer address. `Authenticate` returns the canonical
account unique ID, including when login used a nickname; it issues no session
token. Each protected RPC supplies Basic authorization metadata and verifies
credentials again. Failed logins retain the existing per-principal rate limit.

`ChangeRolesRequest` is a typed protobuf equivalent of the native RoleChange:
`kind`, positive `expected_revision`, `role`, `role_id`, `role_ids`, target
`user_id`, and `channel`. Capability and override-effect strings use the native
roles-v1 names. The acting account is always the authenticated principal. The
same shared authority validates admission again inside its exclusive mutation
barrier and applies revision, hierarchy, transaction and audit rules.

For example, using protobuf JSON notation:

```json
{
  "kind": "role_create",
  "expectedRevision": "7",
  "role": {"name": "Support"}
}
```

`ChangeChannelRequest` is the typed protobuf equivalent of the native
RoleChannelChange described above, including optional settings, creation
access and presence-preserving move `order_index`. Its response returns the committed revision/channel ID and pending
enforcement flag. It uses the same lifecycle transaction and reconciliation as
Query, SSH and native clients. Legacy `CreateChannel` and `DeleteChannel` remain
unavailable in role mode because they do not carry the revision/access contract.

Conflicts return `ABORTED`, permission denials `PERMISSION_DENIED`, invalid
changes `INVALID_ARGUMENT`, and unavailable authority `UNAVAILABLE`. Disabled,
banned or invalid credentials return the same `UNAUTHENTICATED` status.
Authentication has a ten-second context and role/channel mutations a thirty-second context;
the existing 1 MiB request-size limit applies. A successful response contains the
committed revision and any created role ID. `enforcement_pending` means saved,
so clients must not repeat that change. The acknowledgement uses the original
RPC context even if the mutation's own deadline expired. A disconnected client
or expired client deadline can still lose a reply and must refresh before retrying.

`SetMemberVoice` maps the `membervoice` contract above, including optional boolean
presence and session voice revision. Its effect has a ten-second context;
the acknowledgement uses the original RPC context. It performs the same current
admission, visibility, hierarchy and per-flag checks as native/Query/SSH calls.

`MoveMember` maps the `membermove` request and acknowledgement above. It requires
Basic credentials, rechecks admission and current source/destination authority,
and uses the same ten-second effect context. Full capacity returns `ABORTED`.

`DisconnectMember` maps `memberdisconnect`, including current-channel matching,
the optional reason and the acknowledged former membership. It uses the same
native lifecycle, admission/hierarchy checks, ten-second operation context and
separate five-second committed-audit attempt. Server removal uses the distinct
KickMember/BanMember operations described below.

`ListChannels` returns the current native filtered projection. Visible children
of hidden parents are flattened, and member counts omit invisible members
unless the caller can inspect them. Optional `root_channel_id` selects a subtree
of that filtered projection; hidden and missing roots both return an empty list.
Negative or nonnumeric root IDs are rejected. Role changes and disabled account
eligibility take effect on the next RPC on the same connection.

`ListClients` returns the same four identity fields as Query/SSH `clientlist`:
client ID, unique ID, nickname and channel ID. It includes visible unassigned
sessions (channel ID zero), as the native tree does, and omits members of hidden
channels and invisible sessions the caller cannot inspect. It does not expose
connection statistics, IP addresses or internal database user IDs.
`GetChannelInfo` requires a positive channel ID and returns the Query/SSH
`channelinfo` fields: visible parent ID, name/topic, lifetime type, capacity,
visible member count, Opus settings and slow mode. Hidden and missing targets
both return `PERMISSION_DENIED`. Both RPCs use the protected native snapshot
callback and reject legacy mode.

`GetServerInfo` and `GetServerConfig` take empty request messages and return the
same filtered information and ManageServer-gated settings as Query/SSH. The
typed counts, uptime and configuration integers use int64 fields.
`GetClientInfo` requires `client_id` and shares native target visibility, with
separate ViewConnectionInfo and ViewRemoteAddresses gates. Integrations have
no native self-session exemption. Hidden and missing clients share a denial;
unavailable ping remains -1 and other redacted statistics/addresses are zero or
empty. `ListBans` uses the paginated recovery contract below. All four reads use
the same protected delivery boundary as `ListChannels`, and are unavailable in
legacy mode.

`GetRoleState` takes a nonnegative `channel_id`: zero requests server role
management, while a positive ID requests that channel's access editor. It returns
the native scoped policy, capability labels/prerequisites, manageable roles and
grantable capabilities. Effective overrides and parent overrides remain distinct;
hidden parent IDs and inaccessible parent policies are filtered by the native
projection. The response identifies the authenticated actor.

`ListRoleMembers` takes `channel_id`, a positive `expected_revision`, optional
`search` (at most 100 UTF-8 bytes) and nonnegative `after_id`. It returns at most
100 registered identities, including offline and protected members. `manageable`
describes the actor's hierarchy, not whether the identity may be displayed.
When `more` is true, pass the last entry's user ID as the next `after_id`.

`CheckAccess` takes the same scope/revision plus subject `user_id` and capability
name. Zero user ID means guest. The subject never replaces the authenticated
actor. The native decision includes allowed/reason, contributing role IDs,
scope, missing prerequisite and revision; `can_manage_member` is separate.
Unknown capabilities and invalid capability scopes remain native denied
decisions. Stale revisions on either inspection call return `ABORTED`.

`GetChannelOptions` takes a native lifecycle `kind` and `channel_id`. For creation,
the ID is the parent (zero is root); otherwise it is the channel being managed.
The response supplies revision, settings, permitted lifetime/access choices and
filtered destinations with their sync eligibility. It reveals subtree counts
only after the native subtree checks. It is a preflight snapshot: `ChangeChannel`
must still submit that revision and reauthorize when committing.

These four management reads share the bounded protected delivery described below,
recheck eligibility and permissions on every RPC, and reject legacy mode.

Protected reads retain the policy/metadata lease after the unary handler returns.
An owned-socket observer decodes outbound HTTP/2 headers and correlates the opaque
`noxa-read-delivery` response header with the response stream. It releases the
lease only after the complete terminal frame/header block is accepted by the
socket, or after closing the connection and joining its in-flight write. This
is a socket-delivery boundary, not proof that the remote application consumed
the message. Already written bytes cannot be recalled.

The read window is ten seconds, capped by an earlier RPC deadline. A stalled or
cancelled response can close the entire shared gRPC connection, so clients must
reconnect and handle interrupted concurrent RPCs. Protected protobuf responses
are capped at 8 MiB; the native source snapshot limit remains 10,000 items.
The observer closes the connection on invalid/oversized outbound framing
(1 MiB frames, 64 KiB HPACK strings/table limit). Its completion signal includes
split HEADERS/CONTINUATION blocks, and shutdown joins the protected-read workers.
Direct serving paths without the owned-socket tracker fail closed for reads.

The owned-socket tracker also protects role event streams. Its initial header
identifies the stream; each send
registers one fence before queuing a protobuf message. The backend must call it
inside a fresh admission/policy callback. That callback remains held until the
complete gRPC prefix and body reach the socket, independently of RPC completion.
Each send has a ten-second deadline, capped by its callback context, and an
8 MiB message limit. Cancellation, a send error or panic interrupts and joins
the socket write before releasing protected state; this can disconnect other
RPCs sharing the connection. Successful sends keep the stream open.

DATA parsing retains only framing counters and the five-byte gRPC prefix, even
across partial socket writes, DATA frames and padding; bodies are not copied
into the HTTP/2 observer. Idle cancellation retires stream mappings without
waiting behind unrelated socket I/O. Real TCP tests cover two successive
protected messages, blocked delivery versus revocation, cancellation and repeated
idle subscriptions on one connection. The role event adapter described below
supplies fresh admission and filtered projections for each protected send.

Other unsupported RPCs, including raw event subscriptions, return
`FAILED_PRECONDITION` in role mode. Remaining protected read adapters will use
the same delivery contract. Legacy-mode RPC behavior is
unchanged, and the new role/channel/member mutation RPCs are unavailable in legacy mode.

## Role event subscriptions

`Events.Subscribe` supports explicitly enabled integration accounts when its
backend uses roles-v1. Each subscription requires Basic credentials and exactly
one `noxa-authorization-model: roles-v1` metadata value; the response confirms
the model. Legacy mode retains its existing event contract. Production startup
does not yet enable the role backend.

The role filter accepts `ROLE_SNAPSHOT`, optionally with `USER_SPEAKING`. An
empty filter selects both. Speaking-only and raw join/leave/move/kick/ban filters
are rejected: snapshots are required to reconcile visibility and lost activity.
The initial snapshot and each later `ROLE_SNAPSHOT` replace the entire channel,
client and `speaking_client_ids` collections, including empty collections.
Channels and clients have the same filtered discovery fields as the unary APIs.
Hidden parents are flattened, hidden members are omitted, and invisible presence
uses the existing ViewConnectionInfo rule. Speaking state additionally requires
the source's current Speak permission and an unmuted, assigned session.

Structural bus events trigger a fresh projection, never forward their original
payload. Private reasons, hidden source/parent IDs, raw bus timestamps and global
sequence numbers are not exposed. `USER_SPEAKING.user_id` identifies the connected
session. Activity events must match its current channel and state and pass both
viewer visibility and source permission checks. Chat, typing, whisper and other
session-directed events are not included.

Event IDs count only this subscription's delivered messages from 1; timestamps
record delivery preparation. Identical visible snapshots are suppressed using
hashes, including after speaking deltas. Every second, the stream rechecks current
admission and refreshes the snapshot even without bus traffic. This clears mute,
speaking-permission and visibility changes without depending on another media
packet. Every emitted message independently retains current policy and metadata
through bounded socket completion. Account disablement or a new ban ends the feed.

There are at most 64 role subscriptions per gRPC server. The default lifetime is
one hour; reconnect starts with a new full snapshot. A dropped bus event ends the
subscription with `RESOURCE_EXHAUSTED`; bus shutdown returns `UNAVAILABLE`.
Clients must reconnect and replace local state after either condition. Snapshot
construction retains the shared 10,000-item limit and messages the 8 MiB limit.
Slow sends retain the ten-second bound and may close other RPCs on the same socket.

## WebSocket role event subscriptions

`/events` selects the role-aware handler when the supplied backend already uses
roles-v1. This selection does not activate the new Authority; production remains
on its existing legacy handler until the separate cutover.

Role clients send HTTP Basic credentials and exactly one
`Noxa-Authorization-Model: roles-v1` header with their WebSocket upgrade. The
upgrade response confirms the model. Missing/incompatible model metadata returns
412 before login, invalid credentials return 401, and invalid type filters return
400. The existing per-source and shared password-verification limits, connection
limit, same-origin checks and 4 KiB incoming-frame bound still apply.

Use `?types=role_snapshot` for snapshots alone or
`?types=role_snapshot,speaking_changed` for snapshots and speaking deltas. An
empty filter selects both. Speaking-only, raw transition and unknown filters
are rejected. Outgoing text frames use the protobuf JSON representation of the
same `voicx.v1.Event` messages as gRPC: `id`, `type`, `timestamp`, and either
`roleSnapshot` or `userSpeaking`. Enum names use `EVENT_TYPE_*`; int64 values use
JSON strings. Empty collections and false values are emitted explicitly. This
is a negotiated role contract, not the legacy `{seq,type,time,data}` format.

Both transports share the same projection, local sequence and hash-based
duplicate suppression. Initial and one-second idle snapshots, current admission
and source/viewer checks have the same semantics described above. Every frame
is flushed synchronously inside the protected callback. A ten-second upgrade
deadline also prevents a stalled handshake from retaining a slot indefinitely.
Message cancellation closes the owned socket and waits for the interrupted write
before releasing authority. The connection lifetime is one hour, and both the
protobuf message and its JSON encoding must fit within 8 MiB.

Admission failure, bus loss/shutdown, lifetime expiration and failed/slow writes
close the WebSocket. Reconnect and replace all local discovery/activity state
from the new initial snapshot; there is no replay cursor. Raw chat, kick reasons,
bus sequence/timing and other unprojected events are never forwarded.

## Server kick and account ban

Query/SSH `memberkick data=<escaped JSON>` accepts `client_id` and an optional
`reason` of at most 4096 bytes. It requires KickMembers, higher member hierarchy
and current target visibility. It revokes only the selected native session,
including its file capabilities and media peer. The result contains `client_id`
and `cleanup_pending`; a pending cleanup does not mean the session remains active.

`memberban data=<escaped JSON>` accepts `client_id`, optional `reason`, and
`duration_seconds` (zero or omitted means permanent; allowed range is
0–9223372036). It requires BanMembers independently of KickMembers. The server
resolves the target's canonical UID, records the ban, and revokes every matching
native session and account-owned recording delivery. Other guests are not grouped
by their shared numeric user ID zero. Existing integration credentials for the
banned account fail their next admission check.

Both operations revalidate the opaque integration identity inside an exclusive
policy barrier, after current protected effects finish. The actor cannot be set
in request JSON. Missing and inaccessible targets share the same denial. Query/SSH
uses a thirty-second operation context and a separate bounded committed-reply
window. These operations do not change the authorization-policy revision.

Ban results contain canonical `unique_id`, `persistence` (`saved` or
`unconfirmed`), `expires_at` in Unix milliseconds, and `cleanup_pending`.
An unconfirmed result means matching sessions were revoked but persistence could
not be confirmed; it must not be treated as a saved or rejected ban. Expiry is
meaningful only for saved bans, where zero means permanent. Inspect the native
ban list or Query/SSH `banquery` before retrying an unconfirmed request. A saved/pending result must
not be retried as a new ban. Lost transport replies likewise require inspection.

gRPC `KickMember` and `BanMember` expose the same contracts with Basic integration
credentials and thirty-second operation contexts. `BanMemberResponse.persistence`
uses the typed SAVED/UNCONFIRMED enum. Replies use the original RPC lifetime;
client cancellation or disconnect can still lose an acknowledgement. Both RPCs
are unavailable in legacy mode.

## Ban recovery reads

Query/SSH `banquery data=<escaped JSON>` requires current server-wide BanMembers,
matching the native ban list. Each page revalidates integration eligibility and
bans and retains policy/metadata locks through the ten-second bounded response.
Legacy administrator status grants no access.

The request accepts optional `before_id` (zero starts with the newest ban) and
`limit` (zero defaults to 50; maximum 100). Results contain `bans` and an optional
`next_before_id`; pass that cursor as `before_id` to read the next page. Missing
or zero `next_before_id` means the end. IDs descend, so newly inserted bans do
not displace entries on later pages. Pages are separate current reads, not one
database snapshot; restart at zero to see newly added bans.

Entries use the native ban fields: `id`, `type`, `value`, optional `reason` and
`banned_by`, `created_at`, and optional `expires_at`. These entry timestamps are
Unix seconds (the member-ban acknowledgement uses milliseconds). Zero expiry
means permanent. Expired records are included, as in the native list; compare
their expiry before deciding whether a ban is active. This read does not lift
bans. gRPC `ListBans` exposes the same fields and cursor in typed request/response
messages, including Unix-second entry timestamps and expired records. It shares
the current global BanMembers check and protected socket-delivery boundary.

## Audit reads

Query/SSH `auditquery data=<escaped JSON>` and gRPC `ListAuditLog` require current
global ViewAuditLog. The request accepts nonnegative `before_id` (zero starts at
the newest row) and `limit` from 0 to 200 (zero defaults to 50). Results contain
`entries` and native capability descriptors for interpreting role-change details.
Continue with the last returned ID as `before_id`; a short or empty page ends
traversal. An exactly full final page may require one empty read. New entries
require restarting at zero. Each page rechecks admission and current permissions.

Rows preserve ID and Unix-second timestamp. Ordinary audit readers see detail
only when every trusted stored channel scope remains visible; explicitly stored
server scope is visible. Hidden, mixed, deleted and unclassified legacy scopes
become placeholders with `restricted=true` and no actor/action/target/detail.
Owner/Administrator may inspect unclassified history. JSON embedded in the
detail cannot establish scope or provenance. `structured` is set only for rows
with trusted stored scope, and is omitted/false on restricted placeholders.

Both transports retain the native policy/metadata lease through bounded delivery.
Query requires exactly one `data` argument, rejects unknown JSON fields and does
not accept an actor identity. gRPC uses typed request/response fields and the
same protected socket boundary. Legacy `auditlog` remains closed in role mode.

## Runtime configuration changes

Query/SSH `serverconfigset data=<escaped JSON>` and gRPC `SetServerConfig` replace
the six runtime settings exposed by `serverconfig`/`GetServerConfig`. Both require
current global ManageServer and canonical integration admission. The fields are
`max_clients` (0–100000; zero is unlimited), `client_timeout_seconds` (30–86400),
`opus_bitrate` (6000–510000), and booleans `opus_fec`, `opus_dtx`, `opus_stereo`.
This is a full replacement, not a patch: omitted booleans become false and omitted
`max_clients` becomes zero; the timeout and bitrate must be valid. Unknown Query
JSON fields and additional arguments are rejected. The actor comes from credentials.

The native and integration save paths share validation, atomic persistence of all
six settings, serialized runtime publication and canonical audit. The reply
contains the values that this request saved, not a later configuration read.
Concurrent successful saves use the same order for persistence and runtime
publication. This operation does not change the authorization-policy revision.

The integration operation has a ten-second context. A queued save can cancel
before persistence. A confirmed commit is published even if the operation context
has just expired, and receives a separate five-second audit attempt. Query/SSH
uses the existing separate committed-reply window; gRPC remains subject to the
original RPC lifetime. Native acknowledgements have their own five-second window,
including in legacy mode. An error or lost reply is not confirmation of rollback;
check current state before deciding whether to submit another full replacement.

### Media-limit changes

Query/SSH `medialimits` and gRPC `GetMediaLimits` return the complete video
publishing ceilings plus a process-local decimal `revision`. `medialimitsset
data=<escaped JSON>` and gRPC `SetMediaLimits` replace all three values:
`video_max_bitrate` accepts 0–100000000, and width/height must both be zero or
both be 1–16383. Zero means unlimited. Query requires every JSON field and rejects
unknown fields; the typed gRPC request always supplies all three scalar values.

Reads and writes require current global ManageServer and canonical integration
admission. Reads retain policy and admission through protected delivery. Writes
share the native coordinated commit: earlier router output drains, new codec
policy is prepared, all three settings persist atomically, runtime enforcement is
installed and the new native revision is published. The acknowledgement contains
the exact committed values and revision; it is not a later snapshot. A no-op save
persists the values without advancing the revision. Lost replies have unknown
outcome, so callers should read current state before retrying.

## Chat-filter management

Query/SSH `chatfilterquery` and gRPC `GetChatFilters` return `word_filter`,
`link_blacklist`, `link_whitelist` and `from_config`. Both reads and writes require
current global ManageChatFilters, separately from ManageServer, with current
canonical integration admission. Reads retain policy/metadata through protected
delivery. `from_config=true` means no runtime document exists; false may be omitted
in JSON. A storage failure or malformed stored document returns an error.

`chatfilterset data=<escaped JSON>` and gRPC `SetChatFilters` apply partial edits:
an omitted list is retained, while a present empty string clears it. At least one
list must be supplied. Each supplied list is capped at 4096 UTF-8 bytes before
normalization; comma-separated entries are trimmed and empty entries removed,
preserving case. Query/SSH also retains its independent configured command-line
bound (4096 bytes by default, including JSON/escaping). Oversized lines close the
text session before command dispatch; the full per-list range is available through
gRPC or a suitably configured text listener.

Native and integration partial saves read authoritative stored lists, stopping on
read failure instead of overwriting omitted lists with defaults. Persistence,
runtime cache publication and canonical audit share a writer lock with legacy
raw filter writes. The reply contains the normalized committed lists, with
`from_config=false`. A confirmed save publishes its cache and attempts audit using
a separate five-second context even when the operation has just expired. Query/SSH
uses its committed-reply window and gRPC the original RPC lifetime. Moderation's
existing fallback policy on a later uncached read failure remains unchanged.

## Complaint management

Query/SSH `complaintquery data=<escaped JSON>` and gRPC `ListComplaints` require
current global BanMembers, matching native complaint review. `after_id` is
nonnegative (zero starts at the oldest row); `limit` is 0–100, with zero defaulting
to 50. Results contain `entries` and `next_after_id`. Pass a nonzero cursor as
`after_id`; zero or absent means the end. Entries carry stable IDs, target/reporter
unique IDs and nicknames, reason and Unix-second creation time. Pages are current
reads, not one database snapshot; later insertions can appear on subsequent pages.
Each read retains current admission, policy and metadata through bounded delivery.

`complaintclear data=<escaped JSON>` and gRPC `ClearComplaints` require the same
capability and current admission. `target_unique_id` is required; optional
`from_unique_id` restricts deletion to that reporter. The response is only a
`deleted` count, including a successful zero when nothing matches. Refresh the
list separately. A completed delete gets a canonical actor audit attempt using
an independent five-second context, even when its ten-second operation context
has just expired. Query/SSH then uses the separate bounded committed-reply window;
gRPC replies remain subject to the original RPC lifetime.

A lost or uncertain acknowledgement requires a fresh read before retrying:
although repeating an unchanged clear is harmless, newly submitted matching
complaints can be deleted by a later retry. Legacy single-ID/global deletion
commands remain closed and are not aliases for target clearing. Query rejects
unknown fields and extra arguments; neither transport accepts an actor field.

## Rules inspection

Query/SSH `rulesquery` (without arguments) and gRPC `GetServerRules` return
`text`, its content `hash`, and `accepted_clients`. This is a management read
requiring current global ManageServer because it includes acceptance metadata.
It revalidates integration admission and retains policy/metadata through the
existing bounded protected-delivery path. Legacy administrator status grants no
access. Native joining clients continue to receive the published rules normally.

The acceptance count is for exactly the returned hash, even if the wording is
edited during the read. An empty or whitespace-only wording has an empty hash
and zero acceptances. Reading does not accept rules or alter any native session's
pending-rules gate. Legacy `serverrules` remains closed in role mode.

## Server text changes

Query/SSH `servertextset data=<escaped JSON>` and gRPC `SetServerText` require
current global ManageServer and canonical integration admission. `key` must be
`server_name`, `motd`, `announcement` or `server_rules`. `value` must be present;
omitted/null values are invalid, while an empty string clears the setting.
Text must be valid UTF-8 without NUL. Names are limited to 256 bytes and cannot
contain CR/LF; the other settings allow 65536 bytes. Query/SSH retains its
independent command-line bound (4096 bytes by default, including JSON/escaping);
oversized lines close the text session. The full text range is available through
gRPC or a suitably configured text listener.

MOTD and announcements are sealed under the persistent global chat key before
storage. A nonempty announcement also queues the existing encrypted native
broadcast; recipient access is checked at delivery. Clearing it stores empty text
without broadcasting. Saving acknowledges persistence, not receipt by every
client. Names and rules remain plaintext settings. Server-info reads honor the
saved name; clearing it restores the configured fallback. Editing rules changes
the wording/hash checked by the existing join/acceptance flow; it does not force
already admitted sessions through a new acceptance gate.

The response contains only `key` and `content_hash`, the SHA-256 of the exact
submitted UTF-8 bytes (including empty text). This acknowledgement hash differs
from rules inspection's empty hash for empty/whitespace-only wording. No protected
read follows a mutation. Saves share a cancellation-aware lock with join-time
re-seal write-back so older text cannot overwrite a newer edit. A successful save
gets an independent five-second canonical audit attempt recording the key, byte
count and clear flag, without the submitted text. Query/SSH uses its existing
committed-reply window; gRPC replies remain subject to the original RPC lifetime.
Inspect current state before retrying an uncertain announcement acknowledgement,
because repeating a nonempty write queues another broadcast.

## Custom annotations

Query/SSH `customquery data=<escaped JSON>` and gRPC `ListCustomMetadata` read
management annotations. `customchange` and `ChangeCustomMetadata` set or remove
one exact subject/key pair. Both operations require current global ManageServer
and canonical integration admission; legacy admin status and subject ownership
grant no access. This data is private management metadata, not public member
profile data, and keys such as `role` never grant permissions.

`unique_id` remains an opaque annotation subject. Existing subjects without a
current account are retained and manageable; reading them does not reveal or
validate an account, live session, channel or nickname. Existing annotation rows
are preserved. Subjects and keys must be nonempty valid UTF-8, contain no control
characters, and fit within 128 bytes. Values are valid UTF-8 without NUL and may
contain newlines, up to 4096 bytes. Query's separate command-line limit also
applies, so a full 4096-byte value requires gRPC or a suitably configured listener.

Reads accept `unique_id`, optional `after_key` and `limit` (default 50, maximum
100). They return `unique_id`, `entries` containing key/value pairs, and
`next_after_key` when another page exists. The cursor is exclusive and follows
database key ordering. Pages are current reads, not a frozen multi-page snapshot:
restart pagination to see concurrent inserts before an earlier cursor. SQL caps
key/value transfer before allocation; an oversized legacy field fails the page
instead of silently truncating its value or changing stored data.

Changes require `unique_id`, `key`, and exactly one of a present `value` or
`delete: true`. Empty text is a stored value; missing/null value is not deletion.
Deletion is idempotent and touches only the named pair. The acknowledgement echoes
only `unique_id`, `key` and `deleted`, with no subsequent protected read. Writes
use the existing last-writer-wins annotation semantics. Inspect current state
before retrying an uncertain result, especially a delete that could otherwise
remove a newer replacement value.

Reads retain policy/metadata through bounded transport delivery. Writes retain
current authority through storage and receive an independent bounded audit
attempt after success, even if the operation context has expired. Audit records
use the canonical acting identity and subject, key hash and value byte count;
neither annotation values nor raw keys are copied into the audit detail. Legacy
`customset`, `customdel` and `custominfo` remain closed in role mode.

## Still required

- Remaining integration operations listed in [the operation inventory](integration-operation-inventory.md), including explicit complaint single-ID/global deletion, log disclosure and operator lifecycle decisions.
- Broader bundled E2E transport scenarios and operator reset/activation rehearsal.

The shared legacy authentication callback still rejects role mode. The filtered
WebSocket handler and gRPC integration allowlist use canonical integration
admission separately; raw event and legacy administration paths remain closed.
