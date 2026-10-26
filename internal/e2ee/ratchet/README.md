# Shared DM ratchet core (Task 1a)

This package is a **protocol adapter, not the completed DM migration**. No live
DM send/read path imports it yet. Task 1b must add authenticated device/prekey
routing and transactional storage; Task 1c must switch fanout, edits, attachments,
archive behavior and version enforcement. Existing identity pins, passkey proofs,
legacy archives and production sends are unchanged by this package.

## Dependency and protocol

The same committed `core.wasm` runs in native Go through
`github.com/tetratelabs/wazero v1.12.0` (Apache-2.0) and in a disposable browser
worker. It embeds **vodozemac 0.11.1** (Apache-2.0), built from the crates.io source
and exact transitive checksums in `rust/Cargo.lock` with Rust 1.96.0. Its Olm
protocol supplies asynchronous 3DH establishment and the complete DH/symmetric
Double Ratchet. The existing `internal/e2ee/crypto.go` ratchet is not used.

This is Olm version 1, **not Signal's wire format, X3DH, PQXDH or a post-quantum
protocol**. Olm v1 uses its standard truncated 8-byte MAC. The wrapper does not
substitute cryptographic primitives or use experimental vodozemac session
configuration. In the pinned source, `src/olm/session_config.rs` gates both V2
and `SessionConfig::version_2()` behind `experimental-session-config`; the
0.10.0 changelog records that deliberate move behind the experimental flag.
Both endpoints could use full-MAC V2 technically, but doing so needs an explicit
protocol review before live integration. Protocol version is `noxa-olm-v1`.

[vodozemac documentation](https://docs.rs/vodozemac/0.11.1/vodozemac/olm/index.html)
and [source/release](https://github.com/matrix-org/vodozemac/releases/tag/0.11.1).
Upstream reports a
[Least Authority audit](https://matrix.org/media/Least%20Authority%20-%20Matrix%20vodozemac%20Final%20Audit%20Report.pdf).
That audit is not an audit of this wrapper, the current release, wazero, noXa's
identity model or the eventual persistence integration. Tests are interoperability
and regression evidence, not an audit.

`THIRD_PARTY_LICENSES.txt` includes source notices for the Rust runtime and
build-time dependency closure. noXa's wrapper remains MIT. The full upstream
sources are retrievable from the pinned Cargo registry entries/checksums; the
root Go module pins wazero and its checksum. `WAZERO_LICENSE.txt` preserves its
Apache license; the PWA publishes the Rust notices as `/app/ratchet-licenses.txt`.

## Public API

Native: `New(ctx) (*Engine, error)`, `Engine.Apply(ctx, Request) (Response, error)`,
`Engine.Close(ctx) error`. Browser:
`createRatchetCore({ bytes?, signal? }) -> { apply(request, { signal? }), close() }`
from `mobile/src/core/ratchet-core.js`. `bytes` must match the committed core hash;
it cannot select a different implementation. Ordinary builds consume the
committed artifact and require neither Rust nor Docker.

The browser request follows the Go JSON names below, except `key` is a 32-byte
`Uint8Array`; Go uses `[]byte`, encoded as base64 at the internal WASM boundary.
The key is a **local storage secret**, never a peer or server field. Account and
session values are opaque encrypted vodozemac pickles under that key. Integrators
must scope keys and database namespaces to local identity + authenticated server
identity + device, and must never log requests or responses.

| Operation | Additional input | Successful output |
| --- | --- | --- |
| `new_account` | `count` (1–100) | protected `account`, unsigned `bundle` |
| `prekeys` | `account`, `count` (1–100) | next protected `account`, unsigned `bundle` |
| `outbound` | `account`, authenticated `peer_identity`, authenticated `one_time_key`, `binding`, `plaintext` | next protected `session`, `session_id`, `envelope` |
| `inbound` | `account`, authenticated `peer_identity`, independently expected `binding`, `envelope` | next protected `account` and `session`, `session_id`, `plaintext` |
| `encrypt` | `session`, `binding`, `plaintext` | next protected `session`, `session_id`, `envelope` |
| `decrypt` | `session`, independently expected `binding`, `envelope` | next protected `session`, `session_id`, `plaintext` |

A bundle contains `identity` (Olm Curve25519), `signing` (Olm Ed25519), and
`one_time_keys: [{ id, key }]` (base64). These are **unsigned material, never a
trusted directory entry**. Task 1b must bind all of them, including protocol,
server, principal, device, key IDs and validity, with the existing noXa device
Ed25519 identity and validate the existing pinned/delegated device trust chain.
Creating an Olm account must not replace a contact pin or bypass explicit identity
change confirmation. Passkey authorization still goes through `deviceproof.Verify`.

A `binding` contains nonempty UTF-8 strings (at most 256 bytes each, no control
characters): `server`, `conversation`, `sender`, `sender_device`, `recipient`,
`recipient_device`, `message_id`; `purpose` is `message`, `edit` or `media-key`.
Use canonical server and conversation identifiers, not display names. The caller
must verify conversation membership, sender identity and authorized device before
calling the core. Copying an untrusted outer binding into expected input does not
establish those facts. `media-key` reserves a payload type; authenticated media
key transport is not implemented here.

Wire envelope:

```json
{"version":"noxa-olm-v1","session_id":"<Olm session ID>","binding":{"server":"...","conversation":"...","sender":"...","sender_device":"...","recipient":"...","recipient_device":"...","message_id":"...","purpose":"message"},"message":{"type":0,"body":"<Olm base64 ciphertext>"}}
```

Olm message type 0 is prekey, type 1 is normal. Olm's own authenticated binary
message carries DH ratchet keys and counters. The encrypted application payload
repeats version, session ID and every binding field. Receiving compares all those
values after Olm authentication, before returning any candidate state or
plaintext. Altering both visible binding and caller expectation still fails the
inner comparison. `session_id` is determined by Olm, not by the transport.

## Storage and lifecycle contract for follow-on work

- Each operation loads a supplied protected snapshot into a fresh instance and
  returns a **candidate** next snapshot. It has no persistence side effects.
  Failure returns no candidate state. Do not publish decrypted data on failure.
- Atomically commit send state **and the exact envelope/outbox packet before
  transport**. Retry the saved envelope. Repeating `encrypt` against a previous
  snapshot can reuse a message key; this pure adapter cannot prevent rollback.
- Atomically commit receive state, consumed prekeys, deduplication and the locally
  protected readable message/archive. Commit before exposing/acknowledging data.
- Account creation/replenishment marks returned prekeys published in its snapshot.
  Commit that snapshot with the exact publication outbox. Retry that bundle if
  publication fails; do not regenerate to imitate a retry. Refuse creation if an
  account already exists. Account state and session creation must share a storage
  transaction so concurrent initiations cannot consume one key twice.
- A server must atomically hand out each signed one-time key once. The receiver
  also removes a consumed prekey on successful inbound creation. An already-known
  session uses `decrypt`, including delayed prekey messages for that session.
- Serialize per-server/device session updates using a native transaction and an
  IndexedDB transaction/CAS across tabs/processes. The core's two-worker limit is
  a resource limit, **not storage serialization**. Do not fall back to in-memory
  sessions if durable storage fails.
- Persist exact snapshots without rolling them back. Revocation, time expiry,
  session-count limits, retention, outbox acknowledgments and protected history
  recovery/transfer belong to Task 1b/1c. Ratchet keys do not replace an archive.
- Existing sessions do not authenticate a current device directory or revocation
  status by themselves. Reverify authorization before distribution/fanout.

## Bounds and cleanup

Each request is at most 1 MiB, output at most 2 MiB, plaintext at most 64 KiB.
Each instance has a 64 MiB memory maximum and a five-second execution deadline;
there are at most two concurrent operations per Engine/browser core. The browser
runs operations in disposable workers and terminates timed-out/aborted workers.
Native execution uses wazero context cancellation. Excess work fails explicitly.
No JS `Math.random` or Go `math/rand` is used; the only WASM import supplies entropy
from browser `crypto.getRandomValues` or Go `crypto/rand`.

The wrapper keeps at most 100 unconsumed one-time keys, refusing replenishment
rather than evicting one. Upstream vodozemac caps the forward message gap at 2000,
retains 40 skipped keys per receiving chain, and retains five receiving chains.
A gap above 2000 is rejected without returning candidate state. Older keys and
chains are evicted by upstream bounds, so those delayed messages are rejected.
Capacity, memory, execution, authentication and malformed-input failures expose
no candidate state. Task 1b must add wall-clock expiration and per-device/session-count bounds. A
message beyond retained keys is permanently unavailable unless already archived.

Rust zeroizes its temporary input/key/plaintext buffers where possible. Hosts
clear WASM memory and dispose of the instance after operations. Garbage-collected
Go/JS strings and runtime copies cannot provide a guaranteed whole-process secret
erasure property; no such guarantee is claimed.

## Rebuild and validation

Canonical rebuild uses a digest-pinned Linux image to avoid OS-specific panic
location strings. Docker is required on Windows. `--container` forces the same
builder elsewhere; an installed Linux Rust 1.96.0 toolchain is also supported.
The canonical image is
`rust:1.96.0-slim@sha256:c37af730be4fd8104cbf9aedbd6ab259e51ca2d5437817a0f8680edf66ac6c28`.
Cargo uses `--locked`, the source mount is read-only, and `--check` compares the
WASM bytes, hash, JS integrity constant and license notices with committed files.

```sh
node scripts/build-ratchet-core.mjs --container
node scripts/build-ratchet-core.mjs --container --check
go test -count=1 ./internal/e2ee/ratchet
npm --prefix mobile ci
npm --prefix mobile run build
npm --prefix mobile run test:ratchet
node --test mobile/unit/ratchet-core.test.mjs
```

`GO_EXE` optionally selects the Go executable for the browser interoperability
runner. That test starts a native Go process and real Chromium, loads the built
PWA entry and worker, and uses the same committed WASM bytes on both sides. It
checks fixed vectors, alternating DH steps, delayed/out-of-order messages,
replays, tampering, all routing bindings, identity substitution and consumed
prekeys. Every operation reconstructs state from its protected snapshot.
`testdata/interop-vector.json` contains only synthetic test keys; regeneration
requires explicit `NOXA_UPDATE_RATCHET_VECTORS=1` for the native test and review of
any change. The native tests also check storage-key errors, entropy failure,
cancellation and prekey capacity. CI rebuilds/checks artifacts and runs the
packaged cross-runtime test. This does not test real iOS/Android devices or durable
crash/multitab behavior; those remain mandatory later validation.
