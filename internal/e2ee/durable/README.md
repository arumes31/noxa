# Durable local Olm state (Task 1b-ii)

This package and `mobile/src/core/ratchet-store.js` implement candidate-state
persistence. They do not activate live ratchet messaging, authorize directory
keys, change the Olm protocol, or replace the existing legacy history reader.
The authenticated-directory and core README contracts still apply.

## Storage and protection

Native `Open(dir, Namespace{Server, Principal, Device}, Protection, timeout)`
opens one SHA-256-named bbolt file per authenticated server/principal/device.
One exclusive OS lock covers every account record in that device database.
Default lock timeout is 500 ms; accepted positive overrides are at most 5 s.
`NoSync` is never enabled. The wrapped random 32-byte master key is created in
that same locked database transaction, not a separate racing sidecar file.
A valid bbolt file with no committed buckets may retry first initialization
following a failed key-protection call or interrupted first transaction. Any
unexpected bucket, missing key/payload bucket in populated state, unreadable
master, or missing control record is corruption; initialization never clears
existing buckets. Complete malicious erasure/rollback is outside this guarantee.
AES-256-GCM authenticates namespace, record kind, ID and revision for every
value, including control counters, readable archives, trust and outboxes.
Keys/record IDs and storage sizes are metadata, not hidden by this scheme.

`client.openDurableRatchets` requires working user-bound DPAPI on Windows.
Other platforms explicitly use `IdentityProtection(identitySecret)`; fewer
than 32 secret bytes fails unavailable. This fallback derives wrapping material
from the existing identity secret with a domain-separated SHA-256 derivation.
It is **not** an OS vault: current non-Windows identity files may themselves
lack OS encryption. Neither path writes a new unprotected random master key.
The caller owns lifecycle cancellation and closes the store after work drains.

The PWA upgrades IndexedDB `noxa` from version 7 to 8 by adding `ratchet`, without
rewriting legacy stores. Its non-extractable AES-GCM wrapping key has atomic
first-tab winner selection. Native/browser disk formats are intentionally local,
not an interchange or backup format. JavaScript on the origin can use the key;
non-extractability does not protect against an origin compromise.

Browser crypto runs outside transactions. Each mutation then rechecks every
read revision and the persistent identity fence and writes all changes in one
strict-durability IndexedDB transaction. The encrypted namespace control record
also serializes counter updates; unrelated browser operations may therefore
conflict and must retry. Native bbolt serializes writers and CAS checks all
explicit reads. No transport callback is accepted by either adapter.

## Common operation sequence

1. Independently verify server binding, device chain, account pin, block policy
   and membership under the existing trust APIs. This store cannot authenticate
   caller-supplied candidates, session contexts or a newly signed publication.
2. Read **every** affected trust, account, session and OTK obligation record in
   one snapshot. Absent records are represented by revision zero. Do not mutate
   snapshots/candidates concurrently with an awaited operation.
3. Run the shared core using `CoreKey()` / `coreKey(snapshot)`. Account record
   bytes are the core's protected account string. Session record bytes decode
   to `SessionState`: protected `snapshot`, immutable `context`, `created_at`
   and `retired`. Native byte fields use ordinary Go JSON base64; browser
   snapshot/archive byte fields use arrays. Decode before giving the core its
   protected string. Never send the local core key.
4. Submit the entire logical operation with its operation-specific helper.
   Errors return no publishable result. On conflict, discard **all** candidates,
   reread, and recompute; never reuse an old send snapshot under new plaintext.
5. Publish only the committed exact envelopes. Immediately before delayed retry,
   use `RetrySend` / `retrySend`, which rechecks the identity and stored peer
   trust revisions. Higher-level session cancellation still prevents a result
   already returned before logout/revocation from being sent afterward; a store
   cannot retract bytes already handed to transport.

## Native consumer API

- `Read(refs ...Ref) (Snapshot,error)` returns namespace/fence and revisioned
  protected-readable bytes. `Ref{Kind, ID}` kinds are account, session, trust,
  outbox, inbox, publication, claim and obligation. Missing is revision zero.
- `SetTrust(snapshot, id, verifiedValue, revoked)` advances trust revision.
  Caller must already have verified/approved the value. Revocation is sticky
  for this ID; use a distinct verified device/account ID, never un-revoke it.
- `CommitPublication(snapshot, id, account, exactSignedBytes, obligations)`
  defaults to account ID `local`. `CommitPublicationForAccount(snapshot,
  accountID, id, account, signed, obligations)` supports explicit versioned
  account IDs in the SAME database and device fence. It atomically stores the
  candidate account, immutable publication record `{account_id,payload}`, and
  OTK obligations. Retries return the original signed bytes; changing account
  ID under an existing publication ID conflicts. IDs must include the intended
  account epoch where appropriate; the store does not invent server epochs.
- `ReserveClaim(snapshot,id,trustID,exactRequest)` stores request bytes together
  with peer/trust revision. Retry returns those bytes, never a new deadline or
  target. New targets need new IDs. The caller enforces the directory deadline.
- `CommitSend(snapshot,id,[]SendCandidate,readableArchive) (SendResult,error)`
  commits every candidate session and one exact complete fanout together.
  Candidate fields: `SessionID`, `TrustID`, `State`, `Envelope`, `Context`,
  optional `Replaces`. Context pins peer principal/device/account identity,
  `Scope` = `DevicePairScope` (`"device-pair"`) and protocol `noxa-olm-v1`; it
  cannot change under a session ID. **Sessions are scoped per device pair**: one
  session per local account/epoch and peer device/epoch/identity serves every
  direct and group conversation. The conversation is NOT part of the session;
  the core authenticates each message's conversation binding (`ExpectedBinding`)
  and a binding mismatch still fails closed. `CommitAttempt` requires every
  candidate context to carry the device-pair scope.
- Migration rule: session records written before this rule carried a
  per-conversation `conversation` value. They fail `validContext`/context
  equality, so they are never reused for sending, never advanced by a receive
  (`ErrConflict`) and never mixed with device-pair sessions. They only existed
  in pre-activation test namespaces; new sessions are established by a fresh
  signed claim (the 2-per-device-pair daily claim budget is unchanged).
  `Replaces` requires the old session in the snapshot, marks it retired for
  new sends and retains it for delayed receives in the SAME commit.
- `SendResult{Data,Reused}` contains a protected-local operation JSON snapshot.
  `Operation()` decodes `OutboundRecord{Packets,Status,Archive}`. Packet
  envelopes are the exact bytes to transport. A reused logical ID returns the
  ORIGINAL operation and readable archive even if the caller passed a changed
  draft, different candidate randomness or changed recipients. `Reused=true`
  is mandatory UI information: never display the new draft as sent. The caller
  must deliberate a new operation ID to send different content. Stored trust
  revision changes/revocation forbid transport retry of affected old packets.
- `RetrySend(fence,id)` returns the original operation with `Reused=true` after
  current identity/trust checks. `Ack(snapshot,id)` moves status pending→sent
  only; it never changes sessions, envelopes or archive. A sent operation is
  not to be resent. Reject/disconnect must never restore a prior send state.
- `CommitReceive(snapshot,id,ReceiveCandidate) ([]byte,error)` commits session,
  optional account candidate, optional consumed OTK, dedup and readable archive
  together. `AccountID` defaults to local and must match the consumed obligation.
  `SessionID`, `TrustID`, `Context`, `Session`, `Envelope`, `Archive` are required;
  `Account` and `ConsumedOTK` are required together for initial-key consumption.
  Only use successfully authenticated core candidates. Duplicate ID + exact
  envelope digest returns the original archive without advancing state; changed
  ciphertext under the ID conflicts. Historical duplicates remain readable
  after peer revocation, but never across local logout.
- `PendingRefs(kind,afterID,limit)` enumerates at most 200 references for recovery;
  it is not permission to send. Read and use the operation-specific retry helper.
- `Logout(fence)` clears this device database's protected account data, sessions,
  archives and queues and advances its persistent inactive fence in one commit.
  It covers ALL account IDs. This namespace cannot reactivate through this API.
  A higher-level authenticated recovery/onboarding lifecycle remains Task 1c.

Native errors are `ErrConflict`, `ErrFenced`, `ErrRevoked`, `ErrCorrupt`,
`ErrExhausted`, `ErrInvalid`, `ErrInUse`, `ErrUnavailable` or a backend error.
Errors do not contain plaintext, snapshots, private keys or ciphertext bodies.

## Browser consumer API

`await openRatchetStore({server,principal,device})` returns the corresponding
adapter. Methods are `read([[kind,id],...])`, `coreKey(snapshot)`, `setTrust`,
`commitSend`, `retrySend(snapshot,id)`, `ack`, `commitReceive`,
`commitPublication`, `reserveClaim`, `pendingRefs`. The mutating primitive is
private. Snapshot records are keyed by `JSON.stringify([kind,id])` and values
are `{revision,data:Uint8Array}`. Arguments parallel native API with camelCase
candidate names; context fields use `peer_principal`, `peer_device`,
`peer_identity`, `scope` (must be `DEVICE_PAIR_SCOPE` = `"device-pair"`; a
legacy `conversation` field is rejected), `protocol`. Publication's optional final
`{accountID}` selects the account ID, default local. OTK metadata uses
`publication_id`, `expires_at`, `account_id`, `consumed`.

`commitSend`/`retrySend` return `{data:Uint8Array,reused:boolean}`. Decode JSON to
obtain original `archive` bytes, `status`, and `packets[].envelope` (standard
base64 exact ciphertext bytes). All returns occur after transaction completion.
Other candidate/response byte arguments use Uint8Array. Typed errors have
`code` conflict/fenced/revoked/corrupt/exhausted/invalid; IndexedDB failures may
also be DOMExceptions (quota/abort/etc.). The existing global `clearIdentity()`
is the browser logout boundary, clearing account stores and advancing the
persistent fence in ONE transaction while retaining only the wrapping key.

`beginIdentity()` captures the generation at the start of user intent, BEFORE
passkey authentication, QR scanning, link approval, backup reading/import or
guest authentication. `rememberLinkedIdentity(identity,meta,fence)` requires
that original token. `saveIdentity(identity,meta,{onboarding:true,fence})`
checks it in the commit and returns the new active fence; callers pass this
returned fence to `connect({fence})`. `loadIdentity()` also returns its checked
`fence`; reconnect never grants an old in-memory identity a newer generation.
`updateMeta(patch,fence)` uses that same session generation. `clearIdentity(fence)`
can conditionally clear a revoked session without deleting a newer identity.
Stale operations fail with `code:"fenced"`.

Guest replacement uses `saveIdentity(...,{onboarding:true,fence,replaceAccount:true})`
to clear all account stores, retain the wrapping key, advance the generation
and save the new identity in ONE checked transaction. Bad authentication or a
stale fence does not clear anything. Ordinary onboarding still retains archives.
A deliberate guest flow started after logout/replacement reads a new token and
creates a fresh identity if the former guest's generation is no longer active.
The legacy `putSealed` path is also fenced across its awaited encryption.

`connect` stages its ChatSession before awaited identity validation, closes a
rejected connection, and publishes only after validation. Local attempt generations
also stop a superseded connect from replacing or closing a newer app session.
`ChatSession(...,{deferIncoming:true})` buffers up to 1024 unsolicited frames and 8 MiB of aggregate UTF-8 JSON payload
bytes (checked before retention; decoded object overhead is additional);
`activateIncoming()` flushes once, in order, after receivers are installed.
Overflow closes the connection and initialization fails; rejected sessions drop
the buffer. This is transient memory staging, not a durable transport ACK or
cross-tab notification that immediately disconnects every already-active socket.

## Bounds, retention and deferred lifecycle

160 recipient-device packets per logical send covers ten devices per account
and the default 16-member group, including own copies. 512 read refs; 2 MiB
ordinary record, 24 MiB complete outbox, 32 MiB combined mutation/read budget;
100,000 records and 128 MiB logical bytes per namespace. At most 200 enumeration
results per call. Ciphertext/storage overhead adds to physical disk usage.
These are local bounds, not a promise that a 160-device batch fits an existing
network frame; Task 1c must coordinate its immutable batch transport.

At most 100 unresolved private OTK obligations per native device store or
browser namespace. OTK metadata binds account ID, publication ID, public expiry
and authenticated local consumption. Only an actual successful inbound core
candidate + atomic receive commit marks consumption. Public expiry is NEVER a
private-key retirement proof. Consumed metadata is retained to prevent ID reuse.
There is no timer evicting a private key, session, unreadable message or archive.
All exhaustion is explicit. Current delivered DM/recording 30-day and audit
180-day user retention policies are unchanged; pending messages remain pending.
No new local pruning policy is activated in this infrastructure-only task.

**Not implemented:** full orphaned-OTK recovery, replenishment against 100
claimed-without-message obligations, authoritative submit/delivery receipts,
server-committed initial-message obligations, explicit signed account reset and
atomic old-account send retirement/new-account activation. Task 1c needs that
coordinated server/client lifecycle. Socket writes, public expiry, server grace,
logout loops across separate files and account regeneration are not proofs of
safe key retirement. Versioned account/publication/claim/session record IDs and
one stable device fence accommodate the later single-transaction transition.
Do not use account-ID support as authorization to rotate an account today.

## Backup and endpoint limits

No local database can detect an attacker restoring its entire old database,
wrapped key and monotonic counters together without an external trusted anchor.
Neither bbolt nor IndexedDB provides malicious full-backup rollback resistance.
Do not restore such backups as active ratchet sessions. A future import must
quarantine old state for archive/decrypt-only recovery, authenticate an explicit
new account/session lifecycle, and preserve old trust/history as appropriate.
This task exposes no active-session import API. Local malware, same-origin
script, memory copies, page crashes, storage eviction and lost wrapping keys are
not solved by encrypting records. GC memory is not reliably erased.

## Validation and dependency provenance

bbolt v1.5.0, upstream etcd-io/bbolt tag commit
`e7a8b2dd498494a3766ba24dd94d3509e5588485`, Go minimum 1.25, MIT.
Go module checksums pin it in both module graphs. Full MIT notice is in
`BBOLT_LICENSE.txt` and `client/frontend/public/ratchet-storage-licenses.txt`,
which Vite copies into the embedded desktop assets. No new browser crypto
library was added. The shared core now exposes maintained upstream prekey retirement; its canonical build/hash are checked separately.

Run `go test ./internal/e2ee/durable`, `go test -C client ./...`,
`npm --prefix mobile run test:unit`, `npm --prefix mobile run build`, and
`npm --prefix mobile run test:storage`. The ratchet workflow runs native durable
and real Chromium multi-tab checks. Native tests exercise real subprocess
locks, process kills before/after commit, bbolt allocation failure, corruption,
actual shared-core snapshots, CAS/revocation, duplicate receive, immutable
retry, session retirement and a 160-device large-envelope bound. Browser tests
use two actual pages sharing real IndexedDB, version-7 upgrade, first-key race,
core-key winner, real-core CAS, abort, identity/meta/ratchet logout races,
original retry/archive, duplicate receive and corrupted ciphertext. Allocation
failure is a real bbolt MaxSize error, not a claim of tested physical disk loss.
Passing tests are not an independent security audit or live messaging proof.

## Task 1c-i extensions

See [admission contract](../admission/README.md) for `CommitAttempt`, immutable
metadata resolution/receipt retirement, active account fencing and signed reset
preparation/promotion, OTK closure and shared native handles. The matching browser
methods live in `mobile/src/core/ratchet-store.js`. These adapters do not transmit
local archives, approve trust pins, or activate live messaging.
