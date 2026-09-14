# Automatic versioning

`VERSION` is the minimum version on the current major/minor release line. It
contains exactly three numeric components, currently `0.4.3`. Builds never
rewrite it; synchronized package declarations use this baseline.

`go run ./cmd/version` projects that release line onto the current source:

| Source state | Canonical version |
| --- | --- |
| Exact clean tag `v0.4.3` | `0.4.3` |
| Later stable patch tag `v0.4.4` | `0.4.4` |
| Clean untagged commit | `0.4.3-dev+g996a4344dcd6` |
| Dirty tracked/untracked source | `0.4.3-dev+g996a4344dcd6.dirty.hb99ce64cf0ba` |
| Source archive without Git | `0.4.3-dev+src.h<content-hash>` |

The commit and content fragments are deterministic 12-character hashes. A
different commit or effective dirty tree produces a different identity;
reverting the tree restores its previous identity. Ignored build outputs do
not affect it.

## Build commands

Use the stamped targets for production-equivalent local artifacts:

```bash
make version
make version-check
make build
make client-build
make docker-build
make compose-up
```

Windows PowerShell uses the same calculator through the native wrapper:

```powershell
./scripts/build.ps1 version
./scripts/build.ps1 check
./scripts/build.ps1 server
./scripts/build.ps1 client
```

The calculator also supports machine-readable projections:

```bash
go run ./cmd/version -format json
go run ./cmd/version -format runtime
go run ./cmd/version -format ldflags
go run ./cmd/version -format github
go run ./cmd/version -format docker
```

Plain `go build`, `go run`, `wails dev`, and `wails build` use the VCS settings
embedded by the Go toolchain. When a direct build lacks those settings, or is
dirty, the runtime adds a fingerprint of the compiled executable. The shared
calculator is preferred for release-equivalent builds because its dirty
fingerprint is source-derived and reproducible.

A direct `docker build` or `docker compose build` cannot see `.git`; when no
version build arguments are supplied, the Dockerfile automatically embeds the
deterministic `src.h<content-hash>` archive identity instead.

## Release rules

Successful pushes to `main` publish signed stable Windows client and Linux server
releases marked **Latest**, starting with `v0.4.3`. Publication waits for lint,
protocol, frontend, Windows client, server, and security checks. The next patch is
one greater than the highest stable tag on the current release line, or `VERSION`
if that baseline has not been released yet. Prerelease tags do not affect this
sequence. A rerun of an already tagged commit reuses its stable release identity.

The build creates a local tag for consistent binary metadata; GitHub creates the
remote tag at the tested commit when verified assets are published. The existing
signing secret and public-key variable remain required; see
[update signing](update-signing.md). The stable client updater offers these releases.

Manual tags must stay on the same major/minor line as `VERSION` and may not precede
its patch. Multiple intentional tags at one commit, dirty checkouts, malformed
versions, and a newer stable release line fail validation. Historical automatic
`-main.N` tags yield to intentional stable tags at the same commit.

To start a new major/minor line, change `VERSION` and the synchronized package
declarations. `go run ./cmd/version -check` verifies that the root version, Go
fallback, npm/lock metadata, local Go-module placeholder, and source Wails product
version agree on the baseline. CI stamps the selected release patch into Windows
package resources before building; binary runtime versions and the signed manifest
use the same release tag. Direct source builds retain their development identity.
