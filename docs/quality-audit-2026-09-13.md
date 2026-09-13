# Quality and security audit — 2026-09-13

Status: local audit and repairs complete, with validation limits recorded below.
This records measured results, not a production-readiness certification.

## Scope and starting state

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
