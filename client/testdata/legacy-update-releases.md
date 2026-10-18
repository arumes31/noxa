# Authenticated legacy Windows updater releases

The companion JSON preserves the original Ed25519-signed release manifests and detached signatures for every published release from v0.5.24 through v0.5.88, inclusive. The original v0.5.24–v0.5.64 cohort was recorded on 2026-10-14; the remaining signed manifests and tag sources were checked on 2026-10-18. On 2026-10-14 the repository had no earlier published release to authenticate. These are public release metadata, not user profiles or logs.

The fixed verification key is independently pinned from the previously trusted release binary (SHA-256 of the raw key: `86e42bdb94a45f704f24405d76abc873415d5ce20b9d417e2e1e41db7abe1e3e`). Tests verify each signature, version header, client digest, exact admission label, modified-digest rejection, and rejection of a different authenticated release as the held parent.

The first 41 tagged commits share the following Git blobs:

- `client/main.go`: `966b71b7c977b9454da0ba9a4e235341d49a9680`
- `client/updater_startup.go`: `f841faa64ad1c36b900eb045c4161284a20f22db`
- `client/updater_recovery.go`: `a27a6bb6f08d09620718130e28906413a01e9901`
- `client/third_party/go-webview2/pkg/edge/chromium.go`: `1b58635d4f17f5afbb2c0b09fd2774d136963756`

All 65 releases use Wails v2.16.0 and go-webview2 v1.0.23 replaced by the bundled fork, with no Wails replacement. All leave native Wails browser options unset. The fork produces four distinct policies: v0.5.24–v0.5.64 uses `--disable-features=msSmartScreenProtection`; v0.5.65–v0.5.66 prepends a separate `--disable-features=ForceSoftwareForRtcLowResolutions` switch; v0.5.67–v0.5.87 merges these as `--disable-features=msSmartScreenProtection,ForceSoftwareForRtcLowResolutions`. v0.5.88 additionally disables `MediaFoundationUseSWBRCForH264Camera`, frozen as its own historical policy. The compatibility stage must reproduce the exact authenticated policy, including the historical duplicate switches. The updater retains its old process until the new frontend acknowledges a fresh native challenge; its backup uses the `.noxa-previous-*.exe` name in the target directory.

A normally configured replacement cannot share that still-running browser after hardware-policy arguments change. The bridge starts a genuine temporary frontend with the authenticated old policy, retains an independent verified rollback snapshot, and arms the existing supervisor before acknowledging readiness. After the exact old parent and shared browser exit, the supervisor starts the normal hardware-policy frontend. It never kills a shared browser or persists legacy browser settings. The temporary frontend blocks normal application actions.

Admission is a compiled digest table, not a runtime version range or downloaded allowlist. Do not add an entry based on version text alone: verify the signed original client digest and tag source, dependencies, browser arguments, readiness protocol and backup ownership contract. Releases v0.5.65–v0.5.87 use the same main, readiness protocol and dependencies. Their launch/rollback code is identical; a later staging fix adds safe reservation cleanup and commit retries. Their Chromium implementation has only the two audited policy changes above. Release v0.5.88 uses the current hardware arguments, but its exact policy is frozen independently so a future policy change cannot strand it. v0.5.89 introduces the supervisor handoff. Fixture commit IDs record the inspected source; the historical checksum signature covers the version and asset digest, not those commit fields.

These fixture tests and process integration tests do not replace an actual native update rehearsal. In particular, a reported generic child-exit error alone does not prove its underlying WebView HRESULT. The official v0.5.66 → v0.5.126 native upgrade was reproduced on 2026-10-18: WebView2 controller creation failed with 0x8007139f, and the old signed executable was restored byte-for-byte.

## Compatibility contract for every historical release

Every released client must upgrade directly to the latest signed client while preserving its profile; users must not need intermediate versions. Do not remove historical admission entries or reinterpret a frozen browser policy when changing the current policy.

`update-compatibility-audit.json` inventories all 103 historical release tags v0.5.24–v0.5.126 inspected on 2026-10-18, including archived draft assets that were previously distributed. No earlier release assets were present. Versions through v0.5.88 use the authenticated shared-profile bridge above. For every tag v0.5.89–v0.5.126, the supervisor sender, plan structure and process-identity structure were compared with v0.5.89 and were identical; each uses `launchClientUpdateAfterExit`. They need no binary allowlist because the old process exits before the new browser opens.

The Windows integration suite runs the current supervisor against a frozen v1 sender/JSON schema and checks genuine readiness, parent/browser exit ordering and profile preservation. Keep that fixture historical; add new protocol fixtures instead of updating it to match production. The readiness tests also freeze the original 40-byte nonce/PID exchange. A future protocol must keep accepting v1. Current-to-next tests alone are insufficient.
