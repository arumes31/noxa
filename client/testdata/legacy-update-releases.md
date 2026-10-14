# Authenticated legacy Windows updater releases

The companion JSON preserves the original Ed25519-signed release manifests and detached signatures for every published release from v0.5.24 through v0.5.64, inclusive. On 2026-10-14 the repository had no earlier published release to authenticate. These are public release metadata, not user profiles or logs.

The fixed verification key is independently pinned from the previously trusted release binary (SHA-256 of the raw key: `86e42bdb94a45f704f24405d76abc873415d5ce20b9d417e2e1e41db7abe1e3e`). Tests verify each signature, version header, client digest, exact admission label, modified-digest rejection, and rejection of a different authenticated release as the held parent.

All 41 tagged commits share the following Git blobs:

- `client/main.go`: `966b71b7c977b9454da0ba9a4e235341d49a9680`
- `client/updater_startup.go`: `f841faa64ad1c36b900eb045c4161284a20f22db`
- `client/updater_recovery.go`: `a27a6bb6f08d09620718130e28906413a01e9901`
- `client/third_party/go-webview2/pkg/edge/chromium.go`: `1b58635d4f17f5afbb2c0b09fd2774d136963756`

All use Wails v2.16.0 and go-webview2 v1.0.23 replaced by the bundled fork, with no Wails replacement. Their default Wails options produce exactly `--disable-features=msSmartScreenProtection`. The updater retains its old process until the new frontend acknowledges a fresh native challenge; its backup uses the `.noxa-previous-*.exe` name in the target directory.

A normally configured replacement cannot share that still-running browser after hardware-policy arguments change. The bridge starts a genuine temporary frontend with the authenticated old policy, retains an independent verified rollback snapshot, and arms the existing supervisor before acknowledging readiness. After the exact old parent and shared browser exit, the supervisor starts the normal hardware-policy frontend. It never kills a shared browser or persists legacy browser settings. The temporary frontend blocks normal application actions.

Admission is a compiled digest table, not a runtime version range or downloaded allowlist. Do not add an entry based on version text alone: verify the signed original client digest and tag source, dependencies, browser arguments, readiness protocol and backup ownership contract. Releases 0.5.65 onward are not part of this default-policy cohort. Fixture commit IDs record the inspected source; the historical checksum signature covers the version and asset digest, not those commit fields.

These fixture tests and process integration tests do not replace an actual native update rehearsal. In particular, a reported generic child-exit error alone does not prove its underlying WebView HRESULT.
