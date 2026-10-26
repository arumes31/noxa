# Authenticated Olm device directory (Task 1b-i)

This is the directory boundary only. Live DM sends, durable account/session
storage, outbox transactions, ratchet receive processing, media and Studio are
not activated here. The Olm core remains the reviewed `noxa-olm-v1` implementation
and its existing protocol/security limitations still apply.

## Trust and server binding

`internal/devicetrust.Verify(principal, pinnedEd25519, authenticatedRootX25519,
currentDevices)` and the PWA `trustedDevices` return verified Ed25519 **and**
X25519 keys. Expected root keys come from the existing identity pin and signed
keyrecord, never from the candidate list. Duplicate/empty IDs, multiple/missing
roots, a revoked root, root key mismatch or more than 64 records refuse the
list. Unapproved, revoked, malformed, dangling and cyclic children are excluded.
Existing passkey proof verification and automatic endorsement remain unchanged;
a passkey-created device still needs that existing completed endorsement chain.
Historical group verification remains separate from the live list and never
reactivates a sender. Both runtimes now require the authenticated root encryption
key; a changed list cannot select another root X25519 key.

Directory publication signatures use the existing authenticated noXa device's
Ed25519 key. The Olm account's separate Ed25519 key cannot authorize itself.
The server derives principal and device from the authenticated session, compares
its actual signing key with the active stored device, and verifies again against
the locked stored row. Registered users and signed/key-derived linked guests
(`UserID=0`) are supported. Unsigned anonymous guests fail `authority-required`.

`ServerBinding` / `serverBinding` accept the already authenticated control-server
certificate SHA256 fingerprint, either 64 hexadecimal characters or exactly 32
colon-separated hex byte pairs. They emit `tls-sha256:<64 lowercase hex>`.
Native callers must first authenticate/pin that certificate. Browser callers
must obtain the fingerprint through authenticated HTTPS/WSS and preserve the
server trust namespace. An arbitrary AuthResponse, peer-supplied fingerprint,
display hostname, or self-signed bundle is not a trust anchor. NoTLS/empty binding
servers do not advertise this capability and return `unavailable`.

Certificate renewal changes this namespace. After authenticating the new
certificate, publish a new signed generation under its binding with fresh OTK
IDs. Old-binding publications cannot shadow new-binding ones in claims. Their
private-key obligations and pending traffic must remain in the old namespace;
existing sessions/outboxes cannot silently switch bindings. This can temporarily
exhaust the bounded pool until the old lifecycle is resolved. No hostname fallback
or automatic certificate/account pin replacement is implemented.

Static signed device approvals prove authorization under a root; they do not
prove a server has supplied the newest roster. In particular, a malicious server
could replay an older approved device to a first-time contact after revocation.
There is no revocation transparency/freshness claim. Consumers must preserve
known revocations/generation fences and obtain current trust before new fanout.

## Signed format and verification API

Bundle JSON fields, all required:

- `version`: `noxa-olm-directory-v1`; `protocol`: `noxa-olm-v1`.
- `server`, `principal`, `device_id`: independently expected identities.
- `generation`: integer 1..9007199254740991; initial 1, then previous+1.
- `issued_at`, `expires_at`: safe positive integer Unix seconds, at most 7 days
  apart. Future issuance tolerance 300 seconds; fresh claims require unexpired data.
- `identity`, `signing`: Olm Curve25519/Ed25519 public keys, canonical **unpadded**
  standard base64, as returned by the pinned core.
- `one_time_keys`: 1..100 ordered `{id,key}` entries. IDs are the core's canonical
  unpadded big-endian eight-byte base64 counters, bounded to signed int64 and
  handled as `BigInt` in JS, never coerced through Number. IDs/public keys must
  be unique; OTKs cannot equal the account Curve25519 key.
- `signature`: the existing noXa device's 64-byte signature, padded standard
  base64 in JSON (Go `[]byte`).

The signed transcript is UTF-8 concatenated byte-length-prefixed fields
`<decimal byte length>:<field>`, in exactly the field order above, excluding
signature. After the signing public-key field comes decimal key count, then each ordered ID and key.
`Bundle.Transcript()` and `bundleTranscript()` share fixed UTF-8 fixtures. JSON
field ordering/escaping is irrelevant. X25519 canonical encodings and low-order
points are checked with Go `crypto/ecdh` and pinned `@noble/curves` 2.4.0; Ed25519
points use `filippo.io/edwards25519` and noble. No custom curve arithmetic or
alternative session protocol was added. The PWA distributes both noble MIT
notices in `olm-directory-licenses.txt`, verified in the packaged browser test.

Native `Bundle.Sign(existingNoXaPrivateKey)` and browser
`signBundle(bundle, existingNoXaSecret)` create a candidate signed publication.
They do not persist or publish. Commit the core's candidate account snapshot and
exact signed publication outbox atomically in Task 1b-ii before transport.

Native client `verifyOlmClaim(...)` and browser `verifyOlmClaim(bundle, expected)`
first validate the current device chain, then signature, context, lifetime,
independently pinned Olm identity (if previously approved), and claimed ID.
They return only the signed selected `{id,key}`. `Bundle.VerifyClaim` is the
lower-level native operation for an already independently verified signing key.
An empty Olm pin is allowed only for deliberate first-account adoption under
that authenticated noXa chain. These helpers never modify root or account pins.
They are fresh outbound-claim helpers, not an instruction to discard delayed
inbound messages after expiry.

## Wire and consumer sequence

Additive frames 200/201: `MsgOlmDirectoryRequest` / `MsgOlmDirectoryResult`;
capability `noxa-olm-directory-v1` advertises **directory only**. The old prekey
and legacy message formats remain unchanged. The PWA build exports a standalone
`directory-*.js` entry alongside the existing ratchet entry.

Publish request:

```json
{"request_id":"unique correlation token","action":"publish","bundle":{"...":"signed bundle fields"}}
```

Claim request: one target device only, no batch draining interface:

```json
{"request_id":"persisted unique token","action":"claim","principal":"recipient","device_id":"recipient-device","claim_until":1800003600}
```

`claim_until` is a fixed positive Unix-second deadline no more than 1 hour ahead of
the server. Persist and retry the **identical request** before that deadline;
never extend its deadline. An exact retry after its deadline fails `exhausted`,
even if expired public rows have been swept. This prevents old retry IDs from
consuming fresh keys after cleanup. The 1 hour is a claim transaction retry window,
not the lifetime of already committed initial-message ciphertext.

Results echo `request_id`, `action` and claim deadline. Successful publication
has no bundle; a successful claim has the ORIGINAL complete signed `bundle` and
selected `key_id`. Consuming a key never rewrites the signed list. Select the
matching entry only after local chain/bundle verification. Errors are explicit:
`authority-required`, `unavailable`, `rate-limit`, `conflict`,
`account-change-required`, `exhausted`, `limit`, `invalid`. No old-crypto fallback is allowed.
Malformed requests fail the existing protocol malformed error before processing.
Correlated valid-request authorization/backend errors use the directory result.

Request parsing is capped 24 KiB and rejects unknown fields, duplicate JSON member
names (including nested objects), trailing JSON and excessive nesting. Field
names must use canonical lowercase ASCII snake_case; case-folded aliases are
rejected before Go JSON field matching. Strings
are at most 128 UTF-8 bytes with no controls. Persist the generation with the
exact publication. A retained older publication's exact retry also succeeds;
conflicting same generation or recycled OTK IDs fails. Root identity changes
continue to require existing explicit noXa trust approval.

The server currently has no additional per-contact/block ACL for unique-ID DMs;
this handler follows existing authenticated role-session/rules gates. Local
blocks must still be applied by the future caller before claiming. The current
device is mandatory; changing arbitrary `device_id` inside a publication cannot
select another session device. Future DM/group fanout must also check membership
and avoid claims for locally blocked contacts. No general public directory listing
or account-wide OTK replacement endpoint is added.

## Transactions, quotas and revocation

Migration 061 adds immutable publication/key rows and one state watermark per
existing device. It changes no existing identity, device, legacy prekey or message
rows. Generation/account identity/OTK high-watermark survive payload cleanup.
Olm identity or signing-key replacement is explicitly rejected with the typed
`ErrOlmAccountChanged`/`account-change-required`, even if signed by the same noXa
device. This is a staged restriction: Task 1b-ii/1c must add a deliberate versioned
lost/restored-account reset with authenticated approval, fresh sessions and
preserved readable history/pins. It must not silently regenerate the account.

Publications and claims lock active device rows, serializing with all existing
revocation/sign-out/reset updates. Claims lock source and target in deterministic
ID order, then select/update atomically. Bulk reset, idle revocation and idle
warning updates prelock their complete selected sets in that same order before
updating only those captured rows; revocation and dependent cleanup remain atomic.
Claims also take a principal advisory lock to serialize quota checks and retry lookup across reconnects and sender devices. Claim identity
binds requester principal AND device, target device, deadline, and the immutable
publication generation via its foreign key. A reused ID with another target,
device or deadline fails. Exact retries do not spend quota or consume another key.

Revocation is linearized at that row update: work after it commits fails even
when retrying an old claim. Work that committed before revocation can still have
an in-flight network response. Consumers must recheck current trust before
using it. Revoked publication data remains inaccessible until bounded expiry
cleanup; retaining claim receipts preserves quotas and never reactivates devices.

Limits:

| Resource | Bound |
| --- | --- |
| Retained OTK obligations per recipient device | 100, including claimed keys until public expiry+grace |
| Fresh source-device → target-device claims | 2 per rolling 24 hours |
| Fresh sender-principal → recipient-principal claims | 2 × active endorsed sender devices × active endorsed recipient devices, each count capped 100 |
| Fresh claims per sender principal | 4096 per rolling 24 hours across all targets |
| Request work | existing principal limiter plus session bucket burst 12/refill 2 per second; five-second DB operation deadline |
| Publication lifetime | at most 7 days |
| Public retention grace | 24 hours after publication expiry |
| Background cleanup work | at most 256 publications per hourly sweep; at most 100 keys per legitimate publication |
| Device trust chain | at most 64 entries |

The pair quota supports full default 10×10 device-pair startup (100 unique claims)
and the 4096 total bound covers default 16-member × 10 × 10 initialization. Changing
request IDs cannot let one source device drain an entire normal recipient pool.
Many independently authorized malicious principals remain a denial-of-service
risk; there is no Sybil-resistance claim. Quota failures are explicit, never
silently skipped fanout copies. Configuration beyond supported fanout/chain
limits must fail explicitly in the eventual send path.

Expiry forbids NEW claims; cleanup removes only public directory material. The
24 hour grace is **not** a promise that existing pending offline messages expire:
the current spool retains undelivered messages. Do not use this cleanup as a
command to delete claimed private keys or readable archives. Task 1b-ii/1c must
retain private keys while supported delivery obligations remain, or explicitly
surface unsupported late delivery; it must solve bounded exhaustion and recovery
without deleting old history. No private key/session/account storage is implemented
by this migration. Even successfully delivered private keys cannot free this
conservative directory capacity early until a later authenticated lifecycle API
is designed.

## Validation

Set `NOXA_TEST_DATABASE_URL` to the disposable PostgreSQL test database. Directory
store/integration tests intentionally fail instead of silently skipping when it
is missing. Run:

```text
go test ./internal/store -run TestOlmDirectory -count=1
go test -tags integration ./internal/server -run TestIntegrationOlmDirectory -count=1
go test ./internal/e2ee/olmdir ./internal/netproto -count=1
go test -C client -run 'TestNativeOlmDirectory|TestVerifyDMDevices' -count=1
npm --prefix mobile run test:unit
npm --prefix mobile run build
npm --prefix mobile run test:directory
```

`testdata/directory-v1.json` is shared native/browser synthetic material;
`scripts/generate-olm-directory-fixtures.mjs` deterministically regenerates it.
Tests cover real PostgreSQL spent-key concurrency, exact/altered retries,
replenishment, authority including signed guests, revocation lock contention,
crossed claims, quota scope and default 10×10 fanout, expiry/bounded cleanup,
watermarks, TLS rotation and shared malformed trust/bundle vectors. Packaged
Chromium runs the actual built verifier under an isolated document CSP
(`script-src 'self'; object-src 'none'`),
with an inline-script probe that proves enforcement. This is not validation of
the production application's complete CSP.
Passing tests are not an independent cryptographic audit or live-send validation.

## Final inactive epoch contract

`account_epoch` is signed immediately after device ID. `VerifyReservedClaim`
preserves exact reserved evidence until the attempt deadline; `VerifyEvidence`
only authenticates retained admitted evidence after public expiry. Neither
approves a changed pin. Signed `Reset` requires cached old pins and existing noXa
authority. See [admission lifecycle](../admission/README.md).
