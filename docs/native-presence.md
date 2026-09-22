# Native presence

Role-mode `SetStatus` and `SetStatusForTab` request `StatusSaved` (161).
The server checks the current role policy, updates the calling session's state,
verifies that session still exists with the requested status/message, and sends
the reply through the bounded committed-reply writer. Invisible status requires
owner or Administrator authority; the legacy admin flag grants no role access.
Requested acknowledgements on a legacy server are rejected before effects.
Existing unrequested legacy writes keep their asynchronous error semantics.

The reply contains the exact client ID, normalized status (empty means online)
and explicit message (empty clears it). The native caller rejects missing/null
status or message, malformed replies and mismatched fields. Configured auto-away
text is substituted before the request and acknowledgement comparison. The
request retains its captured manager across tab activation and uses the existing
shared request gate and ten-second timeout. A lost reply is an unknown outcome;
there is no automatic native retry or durable presence persistence claim.

The frontend uses `SetStatusForTab` for both the status picker and idle behavior.
Opening/timer scopes include tab, server generation, disconnect generation and
client ID. A newer presence intent owns completion and feedback. Activity while
auto-away is pending requests online once; a failed automatic restore is latched
until a new manual or idle intent, so mouse movement cannot flood retries or
warnings. Manual changes remain available immediately. Connected tab restoration
starts a fresh idle timer, and callbacks from the former scope are harmless.

Recipient-specific role snapshots expose `can_set_invisible`, evaluated using
the current owner/Administrator policy. The picker uses that flag in role mode
and the existing admin flag only in legacy mode. This is a UI hint: every setter
still checks current server authority. Self presence is restored from snapshots
and status events; tab reset/disconnect clears local eligibility and status.

Validation covers real TCP success, normalized online status, explicit clearing,
owner/role-admin invisible access, legacy-admin denial, malformed requests,
missing sessions, held state updates and unrequested/legacy behavior. Native
tests cover early-return rejection, invalid replies, custom away text, tab
capture and legacy writes. Browser tests cover wrong-tab calls, pending results,
scope replacement, snapshots, bridge errors, overlapping manual/idle intents,
retry suppression and tab restoration. Full root/client Go suites, focused race
checks, scoped root/client lint, frontend units/lint and production/native builds
are recorded under `.cache/presence-*`. This slice uses in-memory session state;
it does not claim database integration or complete permission cutover coverage.
