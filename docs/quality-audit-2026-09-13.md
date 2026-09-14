# Quality and security audit — 2026-09-13

Status: local audit, follow-up repairs and Go 1.27 upgrade; measured results and
validation limits are recorded below, including the follow-up section.
This records measured results, not a production-readiness certification.

## Scope and starting state

This section records the initial pass. The follow-up below starts from the user's
subsequent commit of that work and supersedes the old toolchain and probe status.

- Start: `codex/dev5`, `5c3486a7a878a843f71bedc7ab7027ef96576ac7`.
- Work branch: `codex/quality-audit-20260913`.
- Delivery reference: uncommitted working-tree diff on that branch against the
  starting commit. No commit, push, release or image publication was performed.
- Preserved pre-existing untracked files: `internal/query/pentest_probe_test.go`,
  `internal/server/preauth_idle_never_timeout_poc_test.go`,
  `internal/server/preauth_slowconn_poc_test.go`. These are investigative probes,
  including expectations that vulnerable behavior continues; they are not new regression tests.
- No matching Claude project memory directory was present. No repository AGENTS.md
  or contribution guide was found; the user-supplied instructions apply.
- Windows amd64, Go 1.27.0 initially available (manifests require 1.26.6),
  Node 24.19.0, npm 11.17.0, Docker Desktop 4.90.0 / Engine 29.7.2.
- Raw command evidence is kept in ignored `temp/audit-20260913/`.
- Disposable PostgreSQL 16 and Redis 7 containers are named
  `voicx-audit-20260913-postgres` and `voicx-audit-20260913-redis`, using
  loopback ports 55483 and 56383. Existing deployment configuration and data
  are not inputs to the audit.
- Disposable server, PostgreSQL and Redis processes were stopped at completion;
  their local data and audit evidence were retained.

## Feature-to-test matrix

| Feature / entry points | Automated evidence available | Validation boundary |
| --- | --- | --- |
| Control listener / auth: `cmd/server`, `internal/server/tcp.go`, `handlers.go`, `internal/auth` | TCP/TLS handler tests, database auth tests, race suite, fourteen preauth regressions | Absolute deadline verified with virtual time and stalled backends |
| Permissions / administration: `internal/permissions`, server groups, `internal/query`, `internal/grpcserver` | Table-driven engine tests, handler/query/gRPC tests | Database-backed loaders require explicit disposable DSN |
| Voice/video: `internal/webrtc`, server `voice.go`, frontend `main.js`/`audio.js`/`video.js` | Router/peer/lifecycle tests and real three-client synthetic RTP delivery | Hardware and NAT/TURN need separate evidence |
| Messaging / encryption: `internal/e2ee`, `internal/chatcrypto`, server chat, client encryption | Ratchet/prekey, tampering, rotation and plaintext canaries | Channel encryption is server-managed; DMs have a distinct E2EE model |
| Transfers / assets: `internal/filetransfer`, server files/assets, client file transfer | Token, traversal, integrity and filesystem tests | OS-specific ACL/symlink semantics differ |
| Persistence: `internal/store`, migrations, Redis broadcast/state | Scratch-DB migrations, store and backend integration tests | Skipped DB tests do not validate PostgreSQL |
| Desktop: `client/` separate Go module and Wails bindings | Go tests and Windows compilation | Native tray, hotkeys, WebView and hardware not proven by browser mocks |
| Frontend: `client/frontend/src`, `unit`, `tests` | Node behavior/coverage, Playwright workflows and a11y | Vite tests use mocked Wails bindings |
| Deployment / updates: Docker/Compose, scripts, version/signrelease, client updater | Version/signing tests, workflow gates, backup tooling | No releases, image publication or production changes authorized |

## Findings ledger

### Q01 — High: unauthenticated connections retained admission slots indefinitely

`internal/server/tcp.go`: initial activity was zero and the inactivity loop ignored
zero timestamps. There was no absolute preauth deadline; pings also renewed activity.
Independent virtual-time tests reproduced idle, incomplete TLS and repeated-ping
connections retaining their slots. The fix applies an absolute 10-second deadline
(or a smaller positive inactivity limit) to reads, writes, admission queries and
authentication context. Successful authentication restores ordinary inactivity policy.
Second review found that socket deadlines alone did not cancel stalled backends;
four additional backend regressions failed before the context correction. Fourteen
cases in `tcp_preauth_test.go` now pass with race detection, including blocked replies
and successful authentication. Timeout changes require no protocol migration.

### Q02 — High: microphone metadata bypassed authorization; live media ignored revocation

`internal/webrtc/router.go`: microphone authorization depended on optional,
publisher-controlled audio-level metadata and speech transitions; video was checked
only at track creation. Tests forwarded forbidden packets without metadata, with
forged silence, and after live audio/video revocation. Every packet now checks the
current permission callback. Existing music-channel behavior is preserved.
`media_authorization_test.go` reproduced the failures and passes with the broader
WebRTC race suite. Real three-client receiver counters separately verify media delivery.

### Q03 — High: ServerQuery accumulated unbounded lines before checking their size

`internal/query/server.go`: `ReadString` accumulated attacker input before the
configured limit was checked. A 32-byte-limit test consumed 65,536 bytes without a
newline. The bounded reader rejects overlong input immediately and terminates the
session. `bounded_line_test.go` covers limits, LF/CRLF and incomplete EOF; query race
tests pass. Oversized sessions now close rather than attempting to continue.

### Q04 — High: Windows protected identities exposed their encryption private key

`client/identity.go`: DPAPI wrapped Ed25519 signing material but left X25519 private
material in plaintext in the same protected file. The regression detected that exact
secret on disk. Both fields are now protected, and existing signing-only DPAPI files
migrate without changing keys. Corrupt protected data fails closed without overwrite.
Windows DPAPI and migration regressions in `identity_corruption_test.go` pass.
The pre-existing non-Windows/plaintext-fallback policy is unchanged.

### Q05 — Medium: corrupted identity files silently replaced account/key material

An existing zero-byte file was treated as first use, generating and overwriting a
new identity. Separately, losing only one X25519 field caused replacement of the
surviving key pair. Tests reproduced both destructive behaviors. Empty/malformed
existing files and incomplete pairs now fail closed. Legitimate old files with both
encryption fields absent still upgrade while preserving signing identity.
`identity_corruption_test.go` and `identity_partial_corruption_test.go` pass with race
detection. Users with corrupt files must restore their original identity backup.

### Q06 — High: concurrent permission reads republished revoked grants

`internal/permissions/loader.go`: a database query crossing `Invalidate` or
`InvalidateAll` could return and cache its pre-revocation result. Deterministic SQL
driver tests reproduced scoped and global cases. Generation checks now discard such
results and reload. Guest permission reads also gain a bounded, invalidated TTL cache:
the same 50-check workload issues one SQL query instead of 50, avoiding a per-packet
database regression from Q02. Tests cover TTL, capacity, cancellation, errors and
recovery. Existing five-second TTL and mutation invalidation remain the contract;
no external dependency or permission threshold was changed.

### Q07 — High: guest permission backend failures became permissive defaults

`internal/server/groups.go`, `perms.go`, `subscriptions.go`: errors loading the
configured Guest group were indistinguishable from no configured group. Default
talk/video policy therefore allowed transmission during a backend failure. A direct
regression reproduced allowed audio/video and missing error propagation. Errors now
propagate and media/subscriptions deny. Unconfigured guest defaults remain compatible.
`guest_permission_failure_test.go` passes with race detection.

### Q08 — Medium: configured database failures silently skipped integration gates

The auth, channel and permission test helpers called `Skip` for both unavailable
explicitly configured databases and failed migrations. An unreachable loopback DSN
produced exit 0; three subprocess regressions proved the false success. Explicit
database connection failures and all migration failures now fail tests. Unconfigured
local database absence can still skip. `database_contract_test.go` in all three
packages passes; full integration coverage was measured using disposable services.

### Q09 — High: reachable SSH dependency vulnerabilities

Govulncheck confirmed three reachable advisories through `query.SSHServer.serve`:
[GO-2026-6355](https://pkg.go.dev/vuln/GO-2026-6355),
[GO-2026-6354](https://pkg.go.dev/vuln/GO-2026-6354), and
[GO-2026-6303](https://pkg.go.dev/vuln/GO-2026-6303).
Both modules now pin `golang.org/x/crypto v0.56.0`, the minimal release fixing all
three, compatible with Go 1.26.6. Q13 records the additional targeted image dependencies.
Both final reachable-vulnerability scans report none. Module-only advisories that
the application does not call are not described as reachable vulnerabilities.

### Q10 — Medium: live integration tools supplied misleading evidence

`cmd/e2e` never initialized guest encryption material, causing a real 23/24 result
and a guest key timeout. `dialGuest` now initializes/publishes its key consistently
with registered clients; a protocol regression passes and real source/restored
checklists each pass 24/24.

`cmd/loadtest` returned success with authentication 2/3 and counted only sent RTP.
It now fails incomplete/failed runs, tracks actual established peers and per-client
received RTP, responds to renegotiation offers, serializes control frames, and fixes
cumulative ramp delays. Decision tests and a real Pion renegotiation regression pass.
The original intermittent authentication rejection was not reproduced in later runs;
the harness correction does not establish a product authentication fix.

### Q11 — Medium: incomplete CI and build validation

CI did not run frontend lint, did not verify both module checksum sets, and the
client gosec job lacked required Linux native headers. These steps are added with
existing pinned tools and thresholds preserved. The backup job now seeds and compares
user/channel/group/permission relationships in addition to the migration ledger.
README prerequisites now match the Go 1.26.6 module manifests.

Direct client `go mod verify` fails even with a fresh cache because Go tries to
verify the nonexistent archive for local replacement `voicx v0.4.0`. A temporary
workspace marking both modules as local source verifies every downloaded dependency
successfully. CI uses that workspace only for checksum verification; normal builds
retain the module layout. The direct-command failure remains explicitly recorded.

### Q12 — Low: ignored temporary content was not excluded from Docker context

`.gitignore` excludes `temp`, but `.dockerignore` did not. Local dumps and test key
artifacts could consequently enter the build context/builder. `temp/` is now excluded.
Runtime-image structure checks and local non-root startup pass; no image was published.

### Q13 — High: release image contained vulnerable gRPC and OpenSSL versions

Trivy reported four HIGH findings: two gRPC advisories and the same OpenSSL
advisory in `libcrypto3` and `libssl3`. The
[gRPC receive-buffer exhaustion advisory](https://github.com/grpc/grpc-go/security/advisories/GHSA-vp52-pcj8-j9qc)
affects the ordinary server transport used here. The separate
[xDS crash advisory](https://github.com/grpc/grpc-go/security/advisories/GHSA-2v4p-qf9q-27wj)
requires an xDS server, which VoicX does not instantiate. Root now pins gRPC 1.83.2
(Go minimum 1.25), including its required x/net 0.58.0 in both module graphs.
No other unrelated dependency upgrades were made.

The runtime now requires OpenSSL libraries at least 3.5.8-r0 while retaining the
pinned Alpine base. This fixes the packaged
[OpenSSL QUIC memory-growth advisory](https://github.com/openssl/openssl/releases/tag/openssl-3.5.8);
VoicX's static Go server does not use OpenSSL QUIC, so this package finding is not
claimed as an exploitable VoicX listener. The rebuilt image's HIGH/CRITICAL scan
passes with zero findings, without exclusions. Govulncheck separately reports zero
reachable vulnerabilities; scanner databases and reachability models differ.

### Q14 — Medium: native live-test setup concealed certificate and fixture gaps

Live tests hardcoded credentials despite documented environment overrides and
constructed backends without a certificate trust store. The live helper now requires
explicit disposable credentials and certificate fingerprint, creates temporary
identities/trust stores, and verifies changed pins are rejected. Its file-management
fixture was only a 16-byte PNG header; the server correctly rejected it. The fixture
is now a complete encoded 1x1 PNG with byte-exact download verification. Nine real
live tests plus the pin regression passed under `-race`, with zero skips. Full-suite
verification also caught shared use of the backend helper by a local MOTD fixture;
live-specific pin setup is separated from the common test backend.

## Baseline

- Original Windows Go 1.27 root vet/build passed; root race baseline: 1,552 passing
  test/subtest events, 118 skips, one failure (`TestProbeConnCap`, pre-existing probe).
- Original client vet/build/race passed: 340 passing events, nine live-test skips.
  Direct client checksum verification failed as described in Q11.
- Original frontend install/lint/unit/build passed: 70 core tests and eight UI tests.
  Core line/branch/function coverage: 98.10% / 91.55% / 95.16%; selected UI scope:
  25.00% / 76.92% / 25.43%. These are scoped coverage results, not whole-frontend coverage.

## Final verification commands and measured results

Final Go runs use Go 1.26.6. The isolated source snapshot is under
`temp/audit-20260913/final-source` and includes audited source/new regressions but
does not import the three preserved pre-existing probes. No tests are excluded from
the final snapshot commands. Intermediate working-tree runs explicitly separated
those probes; the original files remain unchanged.

| Working directory | Command/check actually executed | Result |
| --- | --- | --- |
| root snapshot | `go vet ./...`; `go build ./...` | pass, exit 0 |
| root snapshot | `go test -race -count=1 -json -covermode=atomic -coverprofile=../snapshot-root-coverage.out ./internal/...` | 1,642 pass events, 14 skips, zero failures; 78.7% coverage (70% gate) |
| root snapshot | `go test -race -count=1 -json ./cmd/... ./v1` | 76 pass events, one skip, zero failures |
| client snapshot | `go vet ./...`; `go build ./...`; `go test -race -count=1 -json -covermode=atomic -coverprofile=../../snapshot-client-coverage.out ./...` | 348 pass events, nine live skips, zero failures; 66.7% coverage (50% gate) |
| frontend snapshot | `npm ci`; `npm run lint`; `npm run test:unit`; `npm run build` | pass, clean lockfile install/build |
| frontend working checkout | `npm run test:e2e -- --retries=0`; `npm run test:a11y -- --retries=0` | 69 workflows pass; accessibility selection passes; no retries |
| frontend | `npm audit --audit-level=high` | zero vulnerabilities |
| both modules | `govulncheck ./...` (v1.6.0) | zero reachable vulnerabilities |
| both modules | `gosec -quiet -exclude-generated ./...` | pass, exit 0; root used isolated source |
| both modules | `golangci-lint run ./...` (v2.12.2) | final root and client: zero issues |
| root | `actionlint`; `buf lint`; `bash scripts/verify-proto-contract.sh` | pass |
| root snapshot | `go run ./cmd/version -check` | pass |
| root | `go mod verify` | pass |
| temporary workspace, root/client | `go work init <root> <root>/client`; `go mod verify` | pass, including fresh module cache |
| snapshot | `docker build -t voicx-audit:20260913 --build-arg VOICX_UPDATE_REPO=arumes31/voicx .` | Linux amd64 image built locally; user 10001; disposable startup ready; shutdown exit 0 |
| disposable Linux container | `sh scripts/test-container-entrypoints.sh` | pass; root-only gosu cases explicitly outside this harness |
| rebuilt image | `trivy image --input audit-image-fixed.tar --scanners vuln --severity HIGH,CRITICAL --exit-code 1` | exit 0; zero HIGH/CRITICAL findings; no exclusions |
| root history and source-only copy | Gitleaks 8.30.1 `git --redact` and `dir --redact` | both exit 1: two reviewed false positives each; no suppression added |

After the final gRPC/x/net changes, the full root command
`go test -race -count=1 -json ./...` passed with **1,718 passing events, 15 skips,
zero failures**. After separating the shared client helpers, its complete command
passed with **349 passing events, nine live-environment skips, zero failures**.
These final runs include all packages in each respective module. Earlier coverage
numbers above were measured before the final dependency patches and test-helper
split; no newer coverage figure is inferred. Post-update vet/build, lint, both
reachable-vulnerability scans, checksum verification and tidy-diff checks also passed.
Exact counts are in `post-dependency-test-summary.json`.

The final image `voicx-audit:20260913-fixed` has ID
`sha256:fccfa9f51375842a43d523be9655f1a22142682310eae329cadfed48fa42d7fd`.
It contains OpenSSL 3.5.8-r0 and gRPC 1.83.2, reached `/readyz` as UID/GID 10001,
and stopped with exit 0. Build/scan/runtime evidence is in `docker-build-fixed.log`,
`trivy-image-fixed.json` and `docker-fixed-runtime.log`. This local rebuild used
the Dockerfile defaults; the earlier build separately exercised the update-repo argument.

Go counts include subtests and fuzz seed executions, not only declarations. The
summary with exact skipped test names is `temp/audit-20260913/final-test-summary.json`.
Root skips are Windows symlink/POSIX permission/inherited-FD limitations plus two
existing tests whose sole purpose is demonstrating no-database skips. The command
skip is a Windows symlink test. No real database validation is inferred from a skip.

Bounded fuzzing used two workers, ten-second budgets and the existing targets:
`FuzzReadFrame` (138,825 executions), `FuzzParseCommand` (44,307), `FuzzParseRing`
(9,193), and client `FuzzNormalizeFingerprint` (67,888), all passing. These are bounded
runs, not exhaustive parser guarantees. A mistaken `FuzzParseLine` invocation found
no target and is not counted as fuzz validation.

The CI benchmark smoke command used `-run '^$' '-bench=.' '-benchtime=1x'` for
netproto/chatcrypto/permissions/filetransfer and passed. Raw timing/allocation samples
are in `benchmark-smoke-final.log`; one iteration is not a latency distribution or
an optimization claim. No before/after performance percentage is claimed.

### Real integration and recovery

The real TLS server and three accounts passed 24/24 checklist scenarios. Three
anonymous clients over 20 seconds with a three-second ramp established all peers,
sent 2,756 synthetic RTP packets, and received 5,176 across all receivers
(1,731 / 1,730 / 1,715). There were zero session failures; authentication latency
histogram had one below 10 ms and two below 50 ms. This is a small local workload,
not a capacity, WAN-latency, hardware-audio or TURN benchmark.

`pg_dump -Fc` and `pg_restore --exit-on-error` restored populated test data. Ordered
complete-row digests matched for users (3), groups (2), channels (2), messages (5),
files (1), and scope keys (4). Matching KEK/PII/TLS/file material was restored too.
The restored server reached readiness, decrypted three original messages with the
same plaintext hashes, and passed 24/24 checklist scenarios. Source and restored
servers stopped gracefully through authenticated administration. Exact commands and
artifact hashes are in `temp/audit-20260913/runtime/RESULTS.md`.

All nine native client live integration tests, plus the isolated certificate-pin
regression, passed under `go test -race -run '^TestLive' -count=1 -timeout=3m -v .`
against the restored disposable server. This includes authentication, chat, presence,
permission denial, group management and file operations. It is backend integration
evidence, not physical-device or native GUI validation.

The exact new CI populated fixture was independently executed against two additional
disposable databases. All six inserts, custom-format dump/restore, the relational
comparison and all 26 migration checksum comparisons passed. See
`temp/audit-20260913/ci-fixture-independent-review.txt`.

### Failures and explicit boundaries

- Original probes remain preserved. Two preauth probes intentionally expect the
  vulnerable behavior and fail after the fix; their expectations were not rewritten.
- Early coverage/benchmark commands had PowerShell argument-tokenization mistakes;
  quoted corrected runs above replace them. A run collided with an active log file
  and did not execute. These are tooling failures, not product test passes.
- Early client coverage ran while a new identity RED test was present; the later
  complete clean snapshot is the final result.
- The first root gosec attempt was stopped after audit caches grew beneath the
  working tree; the isolated-source scan completed with exit 0.
- A later root `go mod tidy` encountered the audit module cache as source. Tidy
  completed in the isolated snapshot, with only intended manifest/checksum changes
  copied back; the ignored audit directory now has its own module boundary.
- The first post-dependency root command included nonexistent `./gen/...`; that
  setup failure is retained, and the corrected command is `go test -race -count=1
  -json ./...`. The generated protocol package is `./v1`.
- The first container harness invocation omitted its Dockerfile fixture; rerunning
  with that fixture passed. A `--version` image smoke invocation started the server
  instead and failed without a database; actual disposable readiness/shutdown was
  then checked successfully.
- An initial patched-image startup reused a populated audit database without its
  previous container's master key and correctly refused to start. A fresh separate
  audit database was used for the final readiness/shutdown smoke; the populated
  key-preserving recovery exercise above remains the recovery evidence.
- GitHub GraphQL issue/PR listing returned HTTP 401. Public REST access and run
  logs worked; prior audit PR #6 and recent failing security runs were inspected
  as context, not applied wholesale or treated as current-checkout proof.
- Native tray/hotkeys, physical microphone/camera changes, suspend/resume, macOS,
  Linux desktop WebView, real TURN/NAT failure, large load/chaos, production backup
  scheduling/off-site recovery and point-in-time recovery are not validated here.
- The initial small-load authentication rejection remains an unreproduced anomaly.
  Capture client/server rejection reasons under the original workload before
  attributing it to a product defect or declaring it resolved.
- Gitleaks scanned all 93 available commits and a copy of tracked/untracked source
  (including the preserved probes, excluding ignored runtime/cache artifacts).
  Both scans reported the RFC WebSocket example nonce in
  `internal/eventbus/ws_security_test.go` and the assertion text "sealed keys must
  differ per member" in `internal/server/e2e_test.go`. Source and historical context
  confirm these are a public handshake fixture and a failure message, not credentials.
  Scanner exit 1 remains recorded; no allowlist, blanket exclusion, history rewrite
  or credential rotation was used. Release ZIP SHA-256 was checked against GitHub's
  release asset digest before running the downloaded scanner.

### Remaining findings and next actions

No confirmed high-severity defect from this pass remains without an implemented
fix. The intermittent initial load authentication rejection is **unclassified**:
its cause and security impact are not established. Re-run the exact three-client,
15-second, one-second-ramp workload with correlated rejection reasons before assigning
severity. Do not interpret later successful runs as proof it cannot recur.

Platform, hardware, TURN/WAN, capacity and disaster-recovery limits above are
**validation gaps**, not assigned defect severities. Before a production-readiness
decision, run the native platform/device matrix, controlled TURN/network failures,
bounded capacity measurements and off-site/key-preserving recovery on authorized
infrastructure. Existing scoped coverage thresholds remain unchanged.

## Reproducing disposable tests

Use the PostgreSQL/Redis image digests from `.github/workflows/ci.yml`, distinct
container names and loopback host ports. Set `VOICX_TEST_DATABASE_URL` to an audit-only
database and `VOICX_REDIS_ADDR` to the audit Redis instance. Never use `.env` or
default local endpoints as implicit authorization to mutate an existing deployment.
Windows Go race tests require the compatible MinGW gcc toolchain; Linux desktop
tests additionally need the native packages and virtual display listed in CI.
Playwright installation is `npx playwright install chromium` on Windows and
`npx playwright install --with-deps chromium` on CI Linux.

For checksum verification of the locally replaced modules in PowerShell:

```powershell
$env:GOWORK = Join-Path $env:TEMP 'voicx-verify.work' # choose a new path
go work init (Get-Location).Path (Join-Path (Get-Location).Path 'client')
go mod verify
Push-Location client
go mod verify
Pop-Location
Remove-Item Env:GOWORK
```

The workspace changes only which local modules are source; downloaded dependencies
are still verified normally. No checked-in go.work or migration changes are needed.

## Operational notes

Both Go modules must be built/tested separately. Build frontend assets before
compiling `client/`. Preserve existing thresholds: root internal coverage 70%,
client 50%; frontend core 90% lines/functions and 80% branches, selected UI scope
25% lines/functions and 70% branches. These frontend scopes do not measure all UI code.

Do not run the repository's Compose chaos targets against an existing deployment:
the Compose file has fixed container, network and volume names. Inspect scripts
and use independently named disposable infrastructure.

## Follow-up: Go 1.27 and remaining live-test findings

The follow-up started with a clean tree on `codex/quality-audit-20260913` at
`38e119de87195059afc80d06a39555549bfeafd1`, which includes the earlier audit and
the three formerly untracked probes. Delivery is the uncommitted diff against
that revision. No push, release, image publication or production operation was
performed. Raw follow-up evidence is in `temp/audit-20260913/followup/`.

### Toolchain and dependency compatibility

Both real Go modules now require **Go 1.27.1**. The Docker builder uses the same
version with a verified manifest digest, and README prerequisites match. Existing
CI jobs read the appropriate `go.mod`, so they inherit the upgrade. Nested
`cmd/fuzzdiscover/testdata` manifests remain discovery fixtures, not application
toolchains. The installed default was 1.27.0; verification explicitly selected
`GOTOOLCHAIN=go1.27.1`.

The version was checked against the [official Go downloads](https://go.dev/dl/)
and [Go 1.27 release notes](https://go.dev/doc/go1.27). Explicit compatible module
pins were updated and both modules tidied: 22 existing required pins in the root
module and nine in the client module, with resulting transitive graph changes.
Major import paths and local replacements are preserved. Notable versions are
Pion WebRTC 4.2.20, Redis 9.22.0, Wails 2.15.0, hotkey 0.6.1, x/crypto 0.57.0,
x/net 0.59.0 and protobuf 1.36.12. Frontend package versions were not changed.

### Additional findings and regressions

| ID / severity | Reproduction, cause and repair | Verification |
| --- | --- | --- |
| Q15 / Medium: obsolete probes prevented a truthful full root run | The now-tracked preauth probes expected vulnerable connections to survive, and the query cap probe expected a greeting after admission was rejected. Baseline failures are retained. Replaced those expectations with deadline, bounded admission and recovery assertions; query probes now assert authentication, escaping, lockout, parsing and privilege behavior. All socket lifetimes are bounded. | `internal/server/preauth_*_poc_test.go`, `internal/query/pentest_probe_test.go`: race tests and 20 lifecycle repetitions pass. No probe is deleted or excluded from the full suite. |
| Q16 / Medium: query load returned success on rejection or no completed work | The real unknown-command workload recorded zero successes and nine failures but exited 0. Results now fail on any rejected request, no completed work, or parent cancellation. Normal duration shutdown separately counts canceled queued/active requests. Setup cancellation closes already-open workers. | `cmd/queryload/outcome_test.go`, `adversarial_outcome_test.go` and updated cancellation tests pass under race detection. The identical real rejection workload exits 1. |
| Q17 / Medium: load authentication discarded server rejections | `readOfType` ignored `MsgError`, obscuring the stage and reason for rejected authentication. It now returns a typed numeric server error and records bounded stage/category/code diagnostics. Peer error text, credentials and raw transport errors are not logged. | `cmd/loadtest/main_auth_test.go`: deterministic protocol RED/green tests and full package race checks. The earlier intermittent authentication anomaly remains unclassified. |
| Q18 / Medium: PostgreSQL chaos inherited an expired read deadline and accepted invalid outage results | The first real drill failed 24/25 after recovery. A channel key arriving before the move acknowledgment left a deadline on the connection. `readEvent` now clears deadlines on every exit. During a fully stopped database, the harness requires backend-unavailable code 5; successful history or permission denial cannot count as passing. Removed the existing post-readiness authentication retries. | `cmd/e2e/read_deadline_test.go` and `chaos_gate_test.go` fail before correction and pass afterward; the actual Go 1.27 drill passes 25/25 with first-attempt authentication after recovery. |
| Q19 / Medium: load publisher dropped an early renegotiation offer | A ten-client run had one receiver at 951 packets while peers received about 7,970. Server timestamps showed a renegotiation offer preceding the initial answer. The publisher now buffers at most one offer, applies the initial answer and queued ICE, then answers the offer. Duplicate early offers fail promptly. | `cmd/loadtest/early_offer_test.go` uses real Pion negotiation and reproduces the dropped offer; `early_offer_limits_test.go` checks the bound. Both pass under race detection. |
| Q20 / Medium: voice renegotiation lost changes while an offer was outstanding | A late publisher scheduled another offer while signaling was unstable; failed offer creation consumed the pending change. The service retains pending work until a valid answer, reserves in-flight offer state, and guards callbacks against replaced/closed peer state. | `internal/webrtc/reneg_pending_test.go` covers late publishers, invalid answers, timer bounds, stale callbacks and overlapping offer creation. Deterministic RED evidence is retained. Final verification is recorded below. |
| Q21 / Low: channel lifecycle assertions raced automatic cleanup | The final root run failed when the valid 50ms temporary-channel cleanup completed before `TestCreateChannel_AllTypes` performed explicit deletion. The equivalent transition test had the same timing assumption. Those two tests now control cleanup timer delivery through a test-only fixture; production cleanup behavior, lifecycle assertions and actual cleanup tests remain intact. | The complete failing run is retained as `final-root-channel-timer-red.jsonl`. Final corrected results are recorded below. |
| Q22 / Low, unresolved upstream: pacer drains queued writes after close | The live run logged 3,357 pacing write failures over eight seconds after synchronized disconnect. A deterministic standalone probe blocks the first write, queues three packets, closes the pacer, then releases the writer with a closed-pipe error: all three writes are attempted instead of only the already-active one. Pion's inner drain loop does not check its close channel, and failed zero-byte writes do not consume the pacing budget. | `followup/pacer_close_probe_test.go` and its RED log preserve the reproduction. The affected implementation is identical in interceptor 0.1.47 and 0.1.48; this is not attributed to the upgrade. No locally forked dependency, custom congestion-control replacement or error suppression was introduced. Next action: carry this minimized case into an upstream fix, then verify shutdown and active media using the fixed compatible release. |

### Measured Go 1.27 validation

Both modules passed `go mod verify`, `go mod tidy -diff`, `go vet ./...`, and
`go build ./...`. Checksum verification used an ignored temporary workspace with
both local modules; no dependency verification bypass or checked-in workspace was
introduced. Frontend assets were built before compiling the client.

The client full race/coverage run passed 349 test events with nine environment-gated
live tests skipped. All nine live scenarios plus the certificate-pin regression
were then run against the real TLS-pinned disposable server: ten passed, zero
skipped, in 20.499 seconds. Client statement coverage was 65.5% (3,468/5,295).
The final root and artifact results follow.

The corrected complete root run passed **1,772 test events across 37 packages**,
with zero failures and 15 skips. Internal statement coverage was **80.9%**
(13,818/17,089), above the unchanged 70% gate. Skips cover Windows symlink/Unix
permission and Linux-specific behavior, plus two tests that explicitly
demonstrate missing-database skips; real PostgreSQL tests executed with the
configured disposable DSN. Counts include named subtests and fuzz seeds and are
not counts of distinct end-user workflows.

The exact full-suite commands, each with `GOTOOLCHAIN=go1.27.1`, were:

```powershell
# Repository root; VOICX_TEST_DATABASE_URL and VOICX_REDIS_ADDR point to audit services.
go test -race -count=1 -json -covermode=atomic '-coverprofile=temp/audit-20260913/followup/final-root-coverage.out' ./...
# From client/ after building frontend assets:
go test -race -count=1 -json -covermode=atomic '-coverprofile=../temp/audit-20260913/followup/go127-client-coverage.out' ./...
go test -race -run '^TestLive' -count=1 -timeout=3m -v .
```

The first final root run failed on Q21 (1,770 pass events, two failing events
including the parent test, 15 skips). The corrected run follows the deterministic
test fix, not a blanket retry. Both affected lifecycle tests also passed ten
race-enabled repetitions and the complete channels package. The final WebRTC
race suite and ten repetitions of its new concurrency regressions passed, and
independent review covered both timing fixes. Final root vet, build, lint and
gosec passed; gosec scanned 107 native source files with zero issues and no new
suppressions. The later channel change is test-only and its package lint passed.

Fresh `npm ci` reported zero vulnerabilities. Frontend lint, unit tests and build
passed; Playwright passed 69 tests with `--retries=0`, and accessibility passed
one test. Core coverage was 98.10% lines, 91.55% branches and 95.16% functions;
the selected UI scope was 25% lines, 76.92% branches and 25.43% functions. All
existing coverage thresholds and measured scopes remain unchanged.

Both modules passed golangci-lint 2.12.2 and gosec 2.28.0 with zero issues, and
govulncheck 1.6.0 found zero reachable or imported-package vulnerabilities. The
tools were rebuilt with Go 1.27.1 after older Go 1.26-built analyzers failed to
analyze the new standard library. One module-only advisory remains:
[GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932), concerning unmaintained
`x/crypto/openpgp`; the scans show that package is neither imported nor called,
and the advisory has no fixed version. No suppression was added. Protocol lint,
the generated protocol contract, actionlint and version-contract checks passed.

The benchmark smoke command was `go test -run '^$' -bench=. -benchtime=1x`
for `internal/netproto`, `internal/chatcrypto`, `internal/permissions` and
`internal/filetransfer`. It passed; one iteration is execution evidence, not a
latency distribution or a measured performance improvement.

The final Linux amd64 image built successfully with the pinned Go 1.27.1 builder.
It reached readiness 200 against a newly created disposable database as user
`10001:10001`, then stopped with exit 0. Trivy 0.74.0 scanned that image's saved
archive with HIGH/CRITICAL severity and returned exit 0. Evidence is
`final-docker-build.log`, `final-trivy.json`, `final-container-smoke.json` and
`final-container-stop.log`.

### Real local load and dependency interruption

Three repetitions of the original three-client, 15-second, one-second-ramp
workload all authenticated three clients, activated all three receivers and had
zero session failures. Received RTP counts were 4,168, 4,025 and 4,197. This does
not establish the cause of the earlier one-off authentication rejection; the
new diagnostics make any recurrence actionable.

The identical ten-client comparison used anonymous identities, 20 seconds,
a two-second ramp, UDP and synthetic Opus. Before the signaling fixes: 9,223
RTP packets sent, 71,541 received, all ten receivers active, but one received
only 951. After the fixes: 9,366 sent, 76,988 received, ten authenticated/active
receivers and zero session failures. Receiver counts were 8,003 / 8,089 / 4,720 /
8,064 / 8,054 / 8,043 / 8,002 / 8,025 / 8,015 / 7,973. The earlier starvation did
not recur in the same receiver; the remaining imbalance is not treated as proof
of uniform or complete media delivery.

The final workload's 39 samples measured 14.234375 CPU seconds, peak working set
94,445,568 bytes, private memory 123,539,456 bytes, Go heap 27,866,680 bytes and
683 goroutines. UDP queue depth and database pool wait count stayed zero. After
cleanup, goroutines returned to the baseline 33, with zero clients and peers.
These separate process runs are local workload measurements; they do not isolate
a dependency-performance effect. Source logs retain closed-pipe pacing errors
after the synchronized client disconnects.

The lower-count receiver completed its renegotiation answer at 10:19:31.598,
alongside peers, and stayed connected until the common shutdown. Its roughly
3,310-packet shortfall is close to the 3,357 queued-write errors, suggesting
queued egress, but those errors have no peer IDs. Per-peer bandwidth estimates,
queue age and loss measurements were not captured, so this does not establish
the cause of throttling or prove complete all-publisher delivery. Q22 separately
records the confirmed shutdown defect; pacing policy during the session remains
an unclassified performance question.

The successful query measurement used four connections, target rate 40/s and five
seconds: 183 completed, zero failed/canceled, achieved 37/s, p95 664.4 microseconds
and p99 1.0605 milliseconds. Reported p50 was zero at the local timer resolution.
These are local measurements, not a production capacity claim.

The real PostgreSQL stop/start drill passed 25/25 on Go 1.27.1: liveness remained
200, readiness returned 503 during the outage, five operations returned the
required backend-unavailable code, and 40/40 pings succeeded. After readiness
returned, the first authentication succeeded and a new persisted chat message
was readable from history. The Redis interruption run kept three authenticated
clients with zero session failures; liveness and readiness both remained 200,
consistent with Redis being optional. Redis restarted successfully. The scripts
check audit container labels and address only named disposable services.

### Compatibility and remaining limits

There are no protocol, persisted-data or migration changes in this follow-up.
Build hosts need Go 1.27.1 or automatic toolchain download access. Windows client
compilation, unit tests and real backend integration passed with Wails 2.15.0;
native GUI, tray/hotkeys, physical devices, Linux/macOS desktop runtime and actual
TURN/WAN behavior remain unverified. Ten local synthetic peers do not establish
production capacity. Earlier populated backup/restore evidence remains valid as
an earlier-pass result; no claim is made of repeating off-site or point-in-time
recovery. The original authentication anomaly remains unclassified, with no
confirmed severity. Q22 remains a low-severity upstream lifecycle finding; the
ten-peer pacing imbalance needs per-peer queue/bandwidth evidence before a
congestion-control change. No production-readiness certification is implied.

The native server stopped gracefully and its audit listeners were confirmed
closed; the final image stopped with exit 0. Labeled PostgreSQL and Redis containers
were then stopped; their data, extra fixture databases and raw evidence remain
local for review. A PowerShell status check initially tried to read a file that
an empty pipeline had not created; explicit Docker inspection subsequently
verified all three audit containers stopped. No unrelated process was stopped.
