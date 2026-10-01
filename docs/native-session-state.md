# Native session state

`SessionInfoForTab(tabID)` returns client ID, legacy admin flag, guest flag,
connected status and transport-security text from one captured connection.
The bridge rejects empty, missing, inactive and mismatched-manager tabs.
It releases the tab activation lock before taking the connection lock, then
copies all fields in one critical section. An offline manager returns an
empty client ID, no admin flag, guest status and `offline` security text.

Login and reconnect use the tab ID returned by their connect call. Tab replay
uses the reset event's tab ID. These paths no longer assemble session state
through separate active-connection getters. Existing public getters remain
for compatibility and unrelated callers.

Frontend completions must still belong to the same server generation and
disconnect generation. This rejects A-to-B-to-A completions and connected
snapshots delivered after a disconnect. Offline snapshots cannot announce
successful recovery or reset the retry counter. If a disconnect schedules
recovery during finalization, the older attempt leaves that timer in charge.

Native tests cover tab rejection, offline defaults and coherent concurrent
snapshots. Browser tests cover activation before reset, repeated activation,
offline login/reconnect, disconnect during pending responses, the five-attempt
limit and single retry ownership. These are local session-state guarantees;
server permission enforcement, committed operation acknowledgements and the
broader authorization cutover remain separate work.

## Rules, subscriptions and avatar reads

`AcceptServerRulesForTab`, `SubscribeChannelsForTab` and `GetAvatarForTab`
capture the displayed native tab with the same expected-tab check. Existing
unscoped methods remain compatible. Avatar requests retain that captured
connection while waiting; frontend generation checks discard old responses.

Rules acceptance sends the displayed hash. An empty `server_rules` event,
not completion of the bridge write, closes the gate. Updated rules replace
the prompt. Decline calls `DisconnectTab` with the original tab ID, so a native
activation before the frontend reset cannot disconnect a different server.
Rejected bridge calls restore the current prompt's controls.

Main menu and tray disconnect also pass their captured source tab ID to
`DisconnectTab`. Reconnect intent is cancelled synchronously, and the existing
native `intentional_disconnect` event still owns the notification before any
replacement tab replay. Delayed errors are shown only for the original scope.

Subscriptions continue to use the full authoritative `subscriptions` event.
A successful native return means submission only. Late bridge failures from
another server cannot show feedback or clear its pending channel selection;
request ownership also protects a newer request for the same channel on the
same server. No new acknowledgement protocol or automatic retry is introduced.

Tests cover native stale-tab rejection, original-connection avatar completion,
rules hash/event behavior, decline routing, bridge rejection and overlapping
subscription requests. The remaining native inventory is tracked in
`docs/native-api-inventory.md`.

Presence uses the same tab/session ownership with explicit role-mode confirmation;
see `docs/native-presence.md` for idle timers, invisible eligibility and overlapping
manual/automatic requests.

## Authorization model and manual tray recovery

`SessionInfoForTab` also copies the negotiated authorization model under its
connection lock. Login and reconnect install it before token redemption or
permission/group refresh. Tab reset enters a local pending state and clears
privileges; legacy queries and grant events remain disabled until the guarded
session lookup completes. A late result cannot replace another activation's
model, including an A-to-B-to-A switch. Role-mode own-role inspection follows
current self snapshots. Retired token, group, grid, appearance and copy dialogs
cannot submit through a role-mode or pending session; audit, bans, filters and
complaints remain separate retained tools.

Tray reconnect captures the source tab, server/session/reconnect generations and
the successful connection record before awaiting `ListTabs`. It proceeds only
when the native active tab still matches and is offline; lookup failure does not
fall back to visual status. Bookmark/recent fallback destinations are copied
before the lookup too. Obsolete failure completions cannot schedule a retry or
open login over a replacement tab. Explicit disconnect cancels both the normal
and guest fallback paths; a late successful fallback tab is closed by its returned
ID. Ordinary quick-connect still uses the latest bookmark/recent when invoked.

These frontend guards do not complete the legacy-to-legacy native group/permission
API inventory or activate server role enforcement. See `native-api-inventory.md`.
