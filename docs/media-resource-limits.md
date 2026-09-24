# Media resource limits during the permission replacement

Publishing authority and operational limits are separate. ShareCamera,
ShareScreen, Speak and RecordChannel decide which actions are allowed in the
current channel. Owner/Administrator still observes configured resource limits.
The server now requires an active roles-v1 policy. Production activation and
physical-device validation remain pending.

## Relayed video bitrate

`video_max_bitrate` in server configuration, or `NOXA_VIDEO_MAX_BITRATE`, sets an
optional per-publisher ceiling in bits per second. It accepts 0–100000000, with
zero meaning unlimited (the default). For example, `3000000` allows 3 Mbit/s
across one publisher's camera, screen share and all simulcast layers together.
It is independent of the six-field runtime server-configuration form. A server
that advertises media-limit management exposes a separate complete replacement
form and applies the new ceiling live; startup configuration remains the fallback.

The router counts complete RTP packet bytes, including RTP headers and padding,
once before recipient fan-out and recorder taps. It uses a one-second token
bucket burst to accommodate keyframes; an interval of duration T can relay at
most the configured rate times (T + one second). Idle publishers cannot accumulate
a larger burst. Excess packets are dropped, which can temporarily degrade video;
this is not transcoding or a client encoder quality adjustment. Audio is separate.
Each publisher has its own budget. Channel moves and ICE peer rebuilds preserve
it; final voice-session teardown releases it. Unknown/unjoined publishers cannot
allocate a budget when the ceiling is enabled.

The limit constrains relayed media, not bytes already received from the network.
It excludes UDP/IP, DTLS/SRTP and retransmission transport overhead; aggregate
server egress also depends on the number of recipients. Admission and network
rate protection remain independent. Very small nonzero limits that cannot fit
one RTP packet effectively suppress video.

## Encoded resolution

`video_max_width` and `video_max_height` (or their `NOXA_` environment equivalents)
set independent maximum encoded dimensions for every camera/screen video layer.
Both default to zero, meaning unlimited. To enable the ceiling, set both to
1–16383; for example, 1920 and 1080. A partial, negative or out-of-range pair is
rejected at startup and by every management transport.

With bounds enabled, the engine negotiates only VP8 video, even if AV1 is enabled
elsewhere in the configuration. It advertises a VP8 `max-fs` receiver preference;
the packet checks remain authoritative. Unbounded servers retain their existing
codec configuration. This codec restriction matches the inspected format and the
router's VP8 output tracks; H.264, VP9 and AV1 are not silently accepted through
the dimension checker.

The inbound track checks its current negotiated codec after each RTP read,
including payload-type changes. Each track/layer has separate dimension state.
An in-bounds VP8 keyframe establishes known dimensions; following frames are
admitted only while their frame starts and sequence continuity remain valid.
Oversized/zero/scaled dimensions, malformed descriptors, missing starts and
sequence gaps invalidate that state. The router requests a fresh keyframe through
its existing rate-limited PLI path. A new track starts with unknown dimensions.
Rejected packets reach neither viewers nor recording taps.

The checker reads the uncompressed keyframe header rather than allocating or
decoding an image. It requires all ten keyframe header bytes in the first packet;
truncated headers fail closed. Packet loss/reordering can therefore temporarily
freeze video until a new acceptable keyframe arrives. Width/height and RTP frame
boundaries follow [RFC 6386 section 9.1](https://www.rfc-editor.org/rfc/rfc6386.html#section-9.1)
and [RFC 7741](https://www.rfc-editor.org/rfc/rfc7741.html).

## Native client capture and feedback

Successful authentication advertises the three startup ceilings in `media_limits`.
The native client validates the ranges and paired dimensions before publishing
its encryption key, retains a value copy on that connection and clears it on
disconnect. Older servers that omit the field retain unlimited behavior. The
frontend reads limits for each new voice session and discards stale responses
after a server/session change; bridge failures stop setup with a retry message.

Camera and screen capture request bounded dimensions, reducing the preferred
size while preserving its aspect ratio. Before publication the client checks the
track's actual dimensions; an oversized or unverifiable bounded track is stopped
with an explanation. The camera tooltip and share dialog describe the ceiling
in English/German. Screen quality announcements use the actual capture height.
Subsequent source changes remain subject to the server's encoded packet checks.

The encoder budget reserves 15% of the server ceiling for RTP overhead and divides
the rest equally between live camera/screen sources, then their active simulcast
layers. Screen presets remain additional ceilings. Low-bandwidth mode divides
its 150 kbit/s ceiling between both sources and uses one active layer per source;
CPU-pressure mode similarly divides its 500 kbit/s ceiling. Stopping a source
restores budget to the remaining source. Camera layer scales remain 1/2/4.
Track attachment, cap setup and offers share the peer negotiation queue, so a
competing offer cannot publish a candidate whose cap setup has not succeeded.
Pending captures belong to session teardown even while negotiation is waiting.

These are cooperative browser controls, not an exact network bandwidth promise.
Capture constraints follow the [Screen Capture specification](https://www.w3.org/TR/screen-capture/)
and encoding controls follow [WebRTC](https://www.w3.org/TR/webrtc/).
The independent server packet ceilings remain authoritative.

## Native live-update transport

The wire protocol reserves `MediaLimitsChanged` (163) for a complete set of
effective limits plus a positive `revision` encoded as a decimal string. Revisions
increase within a server process; `AuthResponse.media_limits_revision` establishes
the connection's baseline. Absent/zero authentication revisions retain legacy
compatibility. Positive authentication revisions require an explicit, non-null
`media_limits` object containing all three limit fields before key publication.

The native connection manager accepts an update only from its installed
transport and connection epoch, and only above its retained revision. Validation,
source checking and cache installation cannot straddle replacement of that
connection. Missing/null fields, invalid ranges, zero revisions and malformed
updates terminate the originating live transport and release pending requests.
Older/equal valid revisions are ignored. Identical values advance the revision
without an unnecessary UI notification. Disconnect clears both values and revision.

Changed values emit a payload-free `media_limits_changed` invalidation. The
frontend fetches current limits through `GetMediaLimitsForTab` and rechecks its
tab/voice-session identity after awaiting the bridge. It coalesces invalidations,
discards reads invalidated while pending and reconciles events arriving during
promise cleanup. Background tabs retain their native cache without replaying
obsolete invalidations. The transport is tested over TLS, and the frontend
capture/rebuild handler is connected.

The native management contract uses separate `MediaLimitsSet` (164) and
`MediaLimitsSaved` (165) messages. All three request fields are required, so an
older or partial client cannot clear limits by omission. The acknowledgement
contains the exact committed values and decimal revision. The desktop shows the
editor only when `serverconfig` advertises `media_limits_management`; this keeps
new clients compatible with older servers. Saving requires current global
ManageServer under roles-v1; legacy administrator state is not accepted. The form is
tab-bound and keeps its draft when validation or the save fails.

## Frontend capture-update primitive

`applyVideoLimits` implements the capture/encoder phase for the current peer.
Its caller supplies the validated native snapshot and an optional voice-session
relevance check. It installs the new limits immediately so captures still in a
picker or waiting for negotiation check the latest dimensions before publication.
The actual update runs in the peer's offer queue. Identical settled values are a
no-op; superseded or obsolete operations return null without reporting old errors
or changing replacement-session tracks.

Bitrate changes redistribute the existing camera/screen budget without another
capture prompt or offer. Dimension changes pause owned video immediately, apply
constraints using the original camera preference or screen preset, check actual
settings, and restore each live track's previous enabled state only after caps
succeed. Overlapping updates retain that original state and only the newest
operation may restore it. Removing bounds restores the original preferred size.
A current constraint/cap failure stops owned video and display audio, preserves
the microphone, and rejects; the caller must then reset the voice session.

The event consumer wraps this primitive with a ten-second deadline covering
native reads, startup completion, the offer queue, constraints and signaling.
Only dimension changes trigger an ICE-restart offer to rebuild the server peer
with current codecs. Current failures/timeouts reset that exact voice session;
late work cannot affect a replacement. Channel movement on the same peer does
not abandon its paused video. The initial limits read has its own ten-second
deadline and refetches invalidated snapshots; events arriving while its helper
returns are reconciled after startup. Reset immediately releases startup
ownership so a hung old operation cannot block replacement startup.

The server preserves confirmed whisper intent and receive quality across these
transport rebuilds. There is no client reapply window in which a private whisper
becomes ordinary channel audio. Final voice teardown clears those controls.
Current publisher and per-packet authorization guards still apply. Tests cover real
negotiated Chromium sender caps, controlled constraint/encoder delays, superseding
updates, replacement sessions, failed/ignored constraints, preset restoration,
disabled tracks and pending camera/screen publication.

## Server publication and authentication ordering

The internal `saveMediaLimits` operation serializes with other configuration
saves, checks validity and revision exhaustion before effects, and uses a
five-second context for acquiring gates and persisting the three values in one
transaction. `Voice.CommitVideoLimits` drains earlier router writes, prepares
codec configuration and retains the engine/output gates through persistence
and installation. Failed preparation or persistence leaves runtime policy and
publication unchanged. Confirmed persistence installs and publishes the exact
values even if the request context has since been canceled. Audit uses a separate
bounded context. An identical save persists values without advancing revision.
Native, Query/SSH and gRPC management paths authorize before calling this helper
and acknowledge its returned revision rather than performing a later state read.

Startup reads all persisted runtime settings in one database snapshot before
constructing media. Media keys (`video_max_bitrate`, `video_max_width`,
`video_max_height`) must all be present or all absent. Explicit zero restores
unlimited behavior; absent keys preserve YAML/environment values. Partial,
empty, invalid or unexpectedly sealed settings reject the load without partially
modifying configuration. A forced failure on the final PostgreSQL row verifies
transaction rollback, unchanged publication and unchanged codec negotiation.

Successful authentication now takes the connection's write lock before reading
its limits/revision. The server therefore cannot send a newer update before an
older authentication baseline. Updates before the response starts are included
in that baseline; subsequent updates use the already-registered outbound queue.
Configuration locks are released before network writes.

The queue carries an internal refresh marker. Its writer reads the current
limits, so backlog does not replay obsolete configuration snapshots. Queue
failure closes the affected connection. A five-second pending-delivery timer
starts before enqueue and covers earlier blocked broadcasts as well as the
notification write. Later updates retain the earliest deadline; writing the
latest pending revision, including through authentication, clears it. Generation
checks make obsolete timer callbacks harmless. Disconnect cleanup stops timers.
Pre-response logins, unauthenticated connections and revoked sessions are not
notification targets. Ordinary legacy broadcast writes are now also bounded and
close their connection on failure; role writes retain their authorization checks
and use the operation context while waiting for the writer.

This deadline proves a completed socket write or connection closure, not that a
client processed the notification or adjusted capture. Independent server packet
enforcement remains necessary. Tests cover actual TLS delivery and new-login
baselines in legacy/role modes, deterministic snapshot/write ordering, full or
missing queues, pre-response logins, no-op/invalid/exhausted revisions, earlier
blocked broadcasts, stale timers and completed-delivery cleanup.

## Existing quality controls and remaining work

The router now has an internal atomic `SetVideoLimits` operation for bitrate and
dimensions. Each output write holds the router's current limit revision; updates
wait for earlier writes and discard packets inspected under an older revision.
The lock is acquired inside the existing media authorization callback, keeping
server authority and membership ahead of the output lock. Output writers must
not call limit setters recursively. `CommitVideoLimits` lets a deadline abandon
the wait for stalled output, queued updates or the engine lock without leaving
an asynchronous lock waiter or changing policy. Queued writers prevent new
readers from continually extending the drain. Canceling the save wait does not
itself interrupt a write. Independently, production UDP/TCP/TLS media socket
writes have a 250 ms budget, including contention for the socket write gate;
scoped recording writes have a 25 ms deadline. A terminal RTP write error retires
that output and invalidates its tickets after authorization/policy/watch locks
unwind. Guard denials alone leave healthy output active. Retirement is logged;
the affected track stays silent until a normal track/peer rebuild or reconnect.
Custom synchronous writers must provide their own bounded write contract.

Existing VP8 tracks detect bounds generations on every packet and require a new
in-bounds keyframe after a dimension change, including disabling and re-enabling
the same dimensions between two packets. Bitrate-only and identical updates keep
valid dimension reference state. Invalid compound updates leave limits and
publisher budgets untouched. Existing non-VP8/unknown-codec tracks stop when
bounds become active; direct uninspected forwarding cannot bypass enabled bounds.

The terminal interceptor rechecks authorization, scope and video revision after
congestion pacing and on NACK/RTX output. Old queued packets are rejected at
that boundary; physical clearing of every Pion buffer is unnecessary for this
guarantee. Tickets expire after one second and are bounded per stream. Packets
already transmitted cannot be revoked. The internal `Voice.SetVideoLimits`
operation now validates the whole bitrate/dimension pair before changing either
component and serializes competing updates. It uses the same commit operation
without persistence and with an unbounded context; runtime management must use
the context-aware commit API. Both settings are installed while new peer creation
and video output are gated. Invalid values and an already
closed engine leave both configurations unchanged; identical values preserve
the API, policy revision and publisher budgets. Callers using the voice facade
must use this coordinated method instead of independent engine/router setters.

New peers created after the update returns use the current codec bounds; peers
created during an update can use the preceding codec configuration. Existing
peers keep their codec configuration until a normal `HandleOffer` rebuild. The
engine retains its certificate, shared UDP listener, ICE settings and original
AV1 preference across updates, restoring the configured video codecs when bounds
are removed. A rebuild preserves channel membership and the publisher budget,
and retains current whisper/receive-quality intent. Recreated whisper tracks
obey the current publisher guard, affected subscribers receive renegotiation,
and per-packet permission checks still protect delivery. Concurrent control
changes are kept in place rather than restored from a pre-rebuild snapshot.
Final teardown still clears both controls.

Bounded peers explicitly install local VP8 codec preferences before generating
answers and subsequent offers, because Pion otherwise takes the answer's fmtp
from the remote offer. The local `max-fs` preference survives omitted or different
remote values and payload-type remapping. Codec entries are copied before
modification because Pion can return shared MediaEngine entries. This is a
receiver hint, not proof of encoded dimensions; packet checks remain authoritative
([RFC 7741](https://www.rfc-editor.org/rfc/rfc7741.html#section-6.2.2)).

Complete native-device and sustained live-stream/recording rehearsal remains
separate from these deterministic deadline and retransmission tests. The desktop runtime editor and the
role-aware Query/SSH and gRPC contracts are available when the server advertises
the capability. Startup controls remain available for offline configuration.

- Channel Opus bitrate/FEC/DTX/stereo are audio settings, not permission powers.
- Receive quality (`high`, `mid`, `low`) selects an available simulcast layer with
  fallback. A layer name is not proof of its encoded resolution or bitrate.
- Client screen presets request 720p or 1080p capture and set sender bitrate
  preferences. Low-bandwidth mode applies an additional local cap. These are
  cooperative encoder controls.
- The retired `b_client_issue_screenshare_1080p` switch checked only the
  client's declared capture height. ShareScreen now authorizes publication,
  and the independent bounds above enforce encoded VP8 dimensions.
  The native client now applies the advertised capture/encoder preferences above.
  The manageable runtime settings editor applies these ceilings without restart.
- Recording remains separately disabled by default and bounded by the configured
  concurrent-session limit. Current RecordChannel authority and recorder-owner
  revocation protect taps. Native capture/FFmpeg output, source switching, live
  revocation and ICE recovery still need the complete end-to-end rehearsal.

Current tests cover deterministic burst/refill bounds, RTP-header accounting,
independent publishers/concurrent layers, camera/screen aggregation, viewer and
recorder fan-out, moves/rebuilds and final cleanup. Dimension tests cover actual
SDP codec preferences, encoded header limits, malformed packets, reference and
track resets, mid-track codec changes and bounded keyframe requests. The packet
inspector also passed a 20-second fuzz run. Engine/voice tests additionally cover
real offer/answer exchanges across codec changes, later server offers, shared
UDP candidates, stable certificate identity, AV1 preferences, concurrent updates
and peer creation, rejected updates and preserved publisher budgets. These pass
with the race detector. Client tests cover actual TLS auth,
connection isolation, invalid/legacy limits and teardown; browser tests use
synthetic canvas/audio tracks and real or fixture peers to cover capture bounds,
combined budgets, cap failures, concurrent publication, stale responses and
immediate capture cleanup. These do not replace native capture
or actual FFmpeg output verification; neither executable is currently on PATH in
the development shell.
