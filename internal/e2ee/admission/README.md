# Admission contract (`noxa-olm-admission-v1`)

This is the `noxa-olm-admission-v1` contract used by every direct/group message,
edit/delete and media-key control. Directory signatures also bind
`account_epoch`; there is no reader for intermediate unshipped Olm signatures.
TCP request/result frames are 202/203. Admission is always enabled on servers
that have their database store and control-channel TLS, and they advertise
`CapabilityOlmAdmission` at sign-in; a server without either does not advertise
it, and updated clients then refuse to send direct or group messages. Updated
clients never send legacy NaCl direct messages; the server still accepts them
from older clients (see the top-level README's upgrade notes).

## Send and resolution

1. Authenticate the control-channel certificate, current noXa device chain and
   independent peer pins. Read active local account authority and a server
   `snapshot` (rosters, target device identities/epochs, group revision/epoch).
2. Persist claims before requesting them. Claim requests include the source
   account epoch and immutable `claim_until` (at most one hour from fresh reservation). Verify the exact
   signed retained bundle and selected key, with independently expected identity,
   device, epoch, server and current noXa authority. A claim is needed only when
   no session exists for that **device pair**: Olm sessions are scoped per local
   account/epoch and peer device/epoch/identity and are reused by every direct
   and group conversation (`OlmDevicePairClaimsPerDay` = 2 is unchanged). The
   conversation is authenticated per message by `ExpectedBinding`, never by the
   session. A claim reserved by a writer that lost a local commit race is reused
   on retry rather than spending more claim budget.
3. For every target except the one local archive target, put `SealPayload` inside
   the core plaintext using `ExpectedBinding`. The inner payload binds attempt,
   routing, action/reference, sender/recipient epochs and group revision/epoch.
   After authenticated decryption, `OpenPayload` must succeed before display or
   mutation. Copying public routing fields is not independent authentication.
   The outer payload and context have strict fields; the application body is
   opaque JSON within the core's 64 KiB plaintext bound, including arbitrary keys.
4. `Freeze` produces at most eight 512 KiB parts, at most 4 MiB including manifest,
   with exactly complete current fanout (up to 160 targets). `CommitAttempt`
   atomically commits every ratchet candidate, immutable manifest/parts, readable
   local archive and next sequence. Parts and archive are distinct; transport only
   `AttemptWire`, NEVER the whole local outbound record.
5. `begin`, `put_part`, `finalize` admit all recipients in one transaction. An
   incomplete upload delivers nothing. Terminal results are immutable. Initial
   claims, sessions, message logical-ID fence and recipient copies commit together.
   Reference edits/deletes must belong to the same sender and conversation.
6. After uncertainty, `status` is metadata only. `absent` does not prove a send
   failed. `abort` with the original manifest races finalization atomically and can
   fence an absent attempt. Commit only exact authenticated accepted/rejected/
   aborted receipts with `ApplyAttemptResult`. A rejected/aborted logical intent
   may be resealed with NEW ratchet state and next attempt sequence; never rewind.
7. Only after committing that terminal receipt locally, obtain
   `AttemptRetirementRequest` and send `retire_receipt`. Acknowledged contiguous
   detail compacts into a persistent retired-through watermark. Older sequences
   can never be admitted again. Missing/unfinished gaps cause backpressure. A
   `retired` reply is not a replacement acceptance receipt. Clients then record
   local progress with `NoteRetired` (browser `noteRetired`); the current
   epoch's watermark is returned as `AccountAuthority.Retired` (browser
   `accountAuthority().retired`), so a later session never repeats it.

Sequences are positive canonical JS-safe decimal integers, per source account.
Each source device has four open attempts and 10,000 retained details; admission has 1,024
open attempts globally and a 128 MiB manifest budget. Recipient pending queues
are bounded to 4,096 copies/64 MiB and globally 1 GiB. Pending data is never evicted
for capacity. Account reset does not bypass device-wide limits. Expired staging
is terminally rejected in bounded maintenance; accepted pending data has no TTL.

## Receive, evidence and account lifecycle

`deliveries` returns `{deliveries, after, has_more}` with at most two scanned rows
and an encoded page ceiling of 768 KiB. The cursor advances across filtered group
packets without revealing/removing their contents. Continue while `has_more`,
even when `deliveries` is empty. UUID cursors are for one drain only: restart from
empty on every polling cycle because newly inserted UUIDs may sort earlier.
Consumer deduplication and exact ACKs make revisiting safe.

Each delivered copy carries the original signed source/target bundle evidence
stored with session admission. Public directory expiry cannot remove it. Verify
that evidence with independently trusted noXa keys and expected identity/epoch;
`VerifyEvidence` / `verifyBundleEvidence` is exclusively for already admitted
packets, not fresh claims. Then check envelope/inner bindings, authenticate with
the core, and commit `CommitReceive` (account/OTK/session/inbox/archive) before
`receive_ack`. Socket write success never ACKs. ACK names exact server, recipient
device/account epoch, delivery ID and copy digest; only its initial admitted
packet releases that OTK obligation. Removed or rejoined group members cannot
receive old-epoch group packets; retained records remain pending.

Public advertisements and OTK obligations are separate. `close_keys` returns
exact authenticated `closed` lifecycle records only for keys that cannot still
be admitted. Its `proof` is a canonical-record digest, not a signature or an
independent trust source. Run the upstream core `retire_prekey` for those exact
public keys, then atomically commit candidate account plus closure receipts.
Never delete private OTKs based on time, missing public data or a socket ACK.

`noxa-olm-reset-v1` is signed by the existing noXa identity over cached old epoch/
identity/signing, new epoch/keys, monotonic publication generation and publication
digest. `PrepareAccountTransition` persists exact intent/new protected account and
blocks old sending. Send `reset`; only the exact receipt can `PromoteAccount`.
Old accounts, sessions, obligations, pending deliveries and metadata resolution
remain. Peer promotion requires verifying the reset chain from independently
cached pins and the current trusted noXa device chain. `reset_chain` is bounded
at 32 transitions per query; advance from the last verified new epoch.

Native `OpenShared` is reference-counted within one process and rechecks key
protection on each acquisition; another process gets explicit in-use. Each window
must cancel/await operations before releasing its handle. Browser IndexedDB CAS
and identity fences coordinate tabs. Check account authority immediately before
network handoff; the server also fences old epochs.

## Integration notes

The native and PWA clients route every direct/group send, edit/delete,
attachment key, history, read and control path through this contract, with
mandatory capability negotiation, durable receive-before-ACK, background
retry/draining and account-reset transitions. ACKed server ciphertext is
retained for 30 days; pending ciphertext is retained until delivered. Automatic link
previews are disabled now in native/PWA encrypted direct/group bodies, provider
cards, hover and shared activity/search renderers. Renderers pass explicit channel
context to permit server metadata requests; unknown destinations stay private.
Explicit links retain their existing external navigation. Channel previews accept
only server-transcoded/proxied raster bytes, with no direct HTTP fallback. 
Short-lived targeted control/archive-import traffic needs a separate authenticated
purpose/schema and authorization path. It may not relax complete chat fanout.
There is no chat `media-key` escape hatch in this manifest.

## Reproducible checks

Set `NOXA_TEST_DATABASE_URL` to a disposable PostgreSQL database. Store tests create
fresh scratch databases and apply the entire migration chain. The additive 062
migration preserves seeded actual legacy pending/history data; 061 is unchanged.

```
go test -count=1 ./internal/e2ee/... ./internal/netproto ./internal/store ./internal/server
npm --prefix mobile run build
npm --prefix mobile run test:storage
NOXA_BROWSER_INTEGRATION=1 go test -tags integration -count=1 -run '^TestIntegrationOlmAdmissionBrowser$' ./internal/server -v
node scripts/build-ratchet-core.mjs --container --check
```

The browser/PG integration harness uses actual framed server handlers, PostgreSQL,
Go/WASM+bbolt and packaged Chromium/WASM+IndexedDB, including bidirectional
messages, uncertain response recovery, delayed initial delivery after public
cleanup, tamper rejection, receipt/ACK ordering and signed reset promotion. It
uses synthetic noXa identities and a test-only localhost HTTP bridge. It does not
claim testing of physical iOS/Android hardware or production deployments.

## Media controls (purpose `control`, 1c-iii-b item 3)

A control is one short-lived, explicitly targeted operation for exactly one
participating device (Task 2: encrypted channel/call media key distribution).
It uses the same claims, device-pair sessions, atomic admission and durable
receive/ACK as chat. Wire rules (`control.go`, JS mirror in
`mobile/src/core/olm-admission.js`):

- Manifest: `purpose: "control"`, `scope: "direct"`, no `reference`, exactly two
  targets: the local archive target and ONE device whose principal is `peer`
  (the sender's own other device is allowed: `peer == sender`). The core
  binding purpose is `media-key`.
- Server: admits the control only if that one target equals its current
  directory state and the sender's roster versions are current (no complete
  fanout is required, and none is possible); deadline at most `MaxControlTTL`
  (300 s); at most `MaxPendingControls` (64) unacknowledged controls per
  sender device and target device, counted apart from messages, so one
  sender can only block its own controls (control copies still count
  towards the recipient's shared pending queue of 4,096 copies / 64 MiB). A control copy still pending
  `ControlPendingTTL` (24 h) after its manifest deadline is cleared by server
  maintenance (`ExpirePendingOlmControls`), unless it opened a device-pair
  session (its initial-session obligation is kept). Edits/deletes can never reference a
  control. The only delivery notification for Olm copies is the
  content-free `olm_pending` doorbell (see the Task 2 consumer section).
- Body (inside the ciphertext): `{kind, scope: {type, id}, epoch, expires_at,
  data?}` with lowercase word `kind`/`scope.type`, exactly these
  (case-sensitive, unique) member names, `epoch`/`expires_at` plain JSON
  integers (JS-safe), `data` at most 4096 compact bytes, `expires_at` within
  300 s of sending. Receivers judge the exact authenticated bytes; the Go and
  JS clients share `testdata/control-vectors.json`.
- Receive: authenticated, consumed and committed like any copy; only the
  disposition is kept (`delivered`, `expired`, or `invalid`, which includes an
  expiry further ahead than 300 s + 300 s skew), then the exact ACK. **Data is
  never kept at rest** on either side: archives keep kind/scope/epoch/expiry
  only. A delivered control from another device is handed to the handlers from
  memory, after the drain step (never inside it, so a handler may reply),
  strictly one control after another in commit order on both clients (Go: one
  queue goroutine; JS: one promise chain per consumer). Delivery is **at most once**: a control committed while no handler is
  registered, or before a crash, is not handed out again. Replays, own sends
  and expired/invalid controls are never handed out (an old key is never
  installed). Unreadable controls stay unacknowledged, are never shown as
  placeholders, and are retried after an explicit trust reset.
- Clients never project controls into chat, activity, search, history
  transfer or export, and never reseal one after rejection/abort.

API for Task 2:

| | Desktop (Go, `client/ratchet_control.go`) | PWA (`RatchetConsumer`) |
|---|---|---|
| send | `connManager.SendRatchetControl(RatchetControlIntent{LogicalID, Principal, Device, Body admission.ControlBody}) (RatchetOutcome, error)` | `consumer.sendControl({logicalID, principal, device, body})` -> `{record, receipt}` |
| receive | `connManager.OnRatchetControl(func(RatchetControl))` (register before connecting; survives reconnects) | `onSecureControl(fn)` in `app.js` (app-level, wired to every consumer before its first drain; `consumer.onControl(fn)` per session) |
| delivered | `RatchetControl{Sender, SenderDevice, LogicalID, Kind, Scope, Epoch, ExpiresAt, Data, Delivery}` | `{sender, senderDevice, logicalID, kind, scope, epoch, expiresAt, data, delivery}` |

`receipt.state === "accepted"` means the server admitted the control for that
device. Errors are the usual coded ratchet errors, plus `invalid` (bad body,
own device, expiry window) and `device_unavailable` (the device is not a
current, published device of that principal). Task 2 checks `scope`/`epoch`
against the current call/channel state before using `data`.

### Task 2 consumer: channel media keys

Encrypted channel media (`noxa-media-e2ee-v1`, client `media-keys.js`, shared
by desktop and PWA) sends one control per recipient device and epoch:
`kind: "media-key"`, `scope: {type: "channel-media", id: "<channel id>"}`,
`epoch` = a per-device monotonic millisecond counter, `expires_at` = now +
120 s, `data: {v: 1, client, session, kid, members, n, keys: {mic, cam,
screen, screenaudio}}` (32-byte keys, base64). Each control has a fresh logical
ID (a retry of the same epoch included). Receivers use a control only if its
scope is the current voice channel, the roster names `data.client` with that
principal and device, and its epoch is newer than every epoch already seen
from that device; nothing older ever installs a key. Desktop relays delivered
controls to the frontend as the tab's non-journaled `media_key` event
(`client/media_e2ee.go`); the PWA routes `onSecureControl` to the voice
session (`app.js`). After a successful `finalize` the server rings a
content-free `olm_pending` event on every live session of each non-local
target device (`internal/server/olm_rate.go`); clients drain on it, with the
slow poll as fallback. Control begins, other admission actions and drains
have per-device budgets separate from the chat-send bucket.
