# Inactive admission contract (Task 1c-i)

This is the final unreleased `noxa-olm-admission-v1` contract. Directory signatures
also bind `account_epoch`; there is no reader for intermediate unshipped Olm
signatures. TCP request/result frames are 202/203. No capability advertises this
path. Frame 202 is disabled by default through an internal server gate, enabled
only by the integration harness. Existing live NaCl consumers remain in place
until Task 1c-ii.

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
The source has four open attempts and 10,000 retained details; admission has 1,024
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

## Deliberate next boundary

Task 1c-ii must integrate every direct/group send, edit/delete, attachment-key,
history/read/control path; mandatory capability negotiation and cutover; durable
receive-before-ACK and background retry/draining; account reset UI/peer transition
verification. Approved 30-day ACKed server ciphertext retention is already
implemented; pending ciphertext remains retained. Automatic link
previews are disabled now in native/PWA encrypted direct/group bodies, provider
cards, hover and shared activity/search renderers. Renderers pass explicit channel
context to permit server metadata requests; unknown destinations stay private.
Explicit links retain their existing external navigation. Channel previews accept
only server-transcoded/proxied raster bytes, with no direct HTTP fallback. This
privacy checkpoint does not activate admission or complete the live migration.
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
