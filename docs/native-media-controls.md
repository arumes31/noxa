# Native media controls

Priority speaker, whisper routing, screen-share declarations and received-video
quality have expected-tab native methods. They capture the initiating connection
once and reject empty, missing, inactive or offline tabs. Compatibility methods
use the same control implementation after resolving their connection.

In role mode, requests carry optional `ack_requested` and wait up to ten seconds
for `MediaControlSaved` (162). The reply identifies the operation, authenticated
client and exact request values. All seven fields are explicit; omitted, null,
malformed or mismatched fields are rejected, including false/zero/empty values.
The existing native per-reply gate serializes these controls. A timeout closes
the original transport so a late reply cannot satisfy another request. There is
no automatic retry or guarantee that a lost reply means the action was rejected.

The server confirms inside the protected effect path after applying session
priority/sharing state, whisper backend configuration or the video-quality
backend call. Rejected authorization, missing sessions and backend errors never
produce success replies. Requested confirmations on legacy servers fail before
the effect; unrequested legacy behavior remains unchanged. Replies use the
bounded committed-reply writer. Confirmation means an accepted in-memory control
effect, not persistence, media delivery or receipt of a broadcast. Disabling
whisper clears routing; its echoed target fields identify the accepted request
and do not assert that disabled targets remain installed.

The frontend waits for priority/whisper results and contains bridge rejection.
Role priority display follows authoritative events and snapshots, including
revocation. Repeated pending hotkeys are suppressed; a failed whisper restore
keeps the previous armed state. The confirmed whisper target is stored separately
from the most recent incoming whisper, so later arrivals cannot mislabel routing.
Settings and hotkeys share completion ownership. An open settings dialog keeps
its original server scope. Local preference persistence is separate from live
routing: failed updates remain retryable within the dialog, and Apply on the
whisper page explicitly resubmits, including after closing and reopening it.
Unrelated settings saves do not require whisper permission.

Screen confirmation failure stops display capture while preserving the camera.
Teardown carries the original voice tab even after the frontend active tab has
changed; it cannot send a stop declaration to the replacement server. Obsolete
quality failures cannot alter replacement state or show feedback. Local mute
and push-to-talk bridge methods emit UI events only, not protocol frames; delayed
PTT release additionally checks session/channel scope and voice epoch.

Validation includes native held-reply and exact-field tests, stale-tab and
original-connection completion tests, real TCP held-effect/denial/backend-error/
legacy cases, server/client race checks, and 70 browser workflows covering media
and settings. Full root/client Go suites, frontend units, native/frontend lint,
generated bindings and frontend/audio/native builds pass. These tests use
controlled media and in-page peers, not physical devices. Physical capture,
revocation, ICE recovery and full permission cutover remain release work.
