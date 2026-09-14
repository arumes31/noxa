# Client updater validation — 2026-09-13

## Outcome

The original client checked GitHub only after server login and displayed a
toast pointing to Help. It did not provide an update button at application
startup. The corrected client checks once after loading settings and the
login screen, before server connection, and opens the existing update dialog
with Update now when a newer release exists. Automatic checks respect the
user setting and stay quiet while offline or up to date.

The dialog displays progress, prevents duplicate UI downloads and dismissal
while applying, reports retryable download and restart failures, and retains
Restart now after closing and reopening. A confirmed backend defect was
fixed: Wails programmatic Quit invokes OnBeforeClose, so close-to-tray could
keep the old process alive. A transient restart flag now allows it to exit
without changing the user's saved preference.

## Evidence

- Startup browser regression reproduced the missing startup dialog before
  implementation, then passed.
- Full frontend browser suite: 78 passed. The expanded updater suite alone:
  12 passed, including manual checks, disabled auto-check, offline behavior,
  progress, duplicate prevention, retry, and retained restart state.
- Frontend lint, unit tests and production build passed.
- Full client race suite including integration tests passed:
  `go test -race -tags=integration -count=1 ./...` (84.937 seconds).
- Eleven signed fake-GitHub integration scenarios exercise the real updater
  in disposable executable copies. Valid updates replace and launch the
  binary while the old process still runs; substituted manifests,
  signatures and binaries are rejected. Tests also cover missing trust,
  failed downloads, changed/withdrawn metadata, caller URL revalidation,
  untouched user-data fixtures and temporary-file cleanup.
- Isolated release checkout with original pinned dependencies:
  `go test -tags=integration -count=1 ./...` passed (31.223 seconds).
- Version, signed-manifest, release-signer and version-command tests passed.
- Native Windows production build with Wails 2.13.0 succeeded (54.739 seconds).
  This local build is a build-validation artifact, not a signed release.

## Release preparation

Isolated checkout: `temp/updater-release`, branch
`codex/client-startup-update`, head `fe1eb61`. Commits:

- `aeb53e4`: startup updater, restart fix and regression tests; Windows CI
  now includes the integration test build tag.
- `4dadf5f`: add missing final newlines in three pre-existing test files
  that blocked the previous CI run.
- `fe1eb61`: prepare 0.4.1 version declarations and changelog; remove
  hard-coded old-release expectations from fallback-version tests.

Branch pushed to GitHub. CI run:
https://github.com/arumes31/voicx/actions/runs/34748687080
Status at handoff: queued; no GitHub test result claimed.

## Deployment completed after explicit user approval

The user approved creation and storage of the initial release-signing key.
The private key was generated in a temporary file outside the repository,
uploaded directly into the repository's VOICX_UPDATE_SIGNING_KEY Actions
secret, and the temporary private-key file was removed. Public key:
`V2gJha/+dbSOAz0F8EBCX6358VJJD0JDtD+wKZeemFg=`.

CI follow-up fixed stale probes that expected already-prevented vulnerabilities,
Windows golden-fixture newlines, test connection cleanup, and missing Linux
build dependencies in CodeQL. Security and coverage gates were retained.
Root race coverage measured 71.6%; root and client linters reported zero issues
with the release's Go 1.26.6 toolchain. Automatic approval review held the tag
push until the container build passed; the full branch CI then succeeded.

Release `v0.4.1` was published at 2026-09-13 13:08:46 UTC from commit
`47257f3afed3310084ea3b0d20a67351e95055a1`:
https://github.com/arumes31/voicx/releases/tag/v0.4.1

Tagged CI (including build, signing, publication, and container image) succeeded:
https://github.com/arumes31/voicx/actions/runs/34758605091
Tagged lint run `34758604979` also succeeded.

Published Windows client: 15,374,336 bytes, SHA-256
`52c44dcc785d01c456243007749eedd7e12152278f611cbcffed731c596da796`.
Published Linux server SHA-256:
`b4c71eecd43df340fb3293d9f68ceaa3a3381663002965b8a3579929300da87b`.
Both published checksums and the detached manifest signature were independently
verified after downloading. The installed Windows update passed GitHub build
provenance verification restricted to this repository's CI workflow.

The opt-in live test used the production CheckForUpdate and DownloadAndApply
methods with current version 0.4.0 against the actual GitHub API and published
release assets. It passed in 12.64 seconds: trusted signature verified,
disposable executable replaced with exactly the published Windows binary,
fixture user data preserved, and temporary downloads cleaned up. It did not
launch the downloaded native client.

Rerun script:
`temp/live-github-updater-validation/run-live-update.ps1`.
Evidence:
`temp/live-github-updater-validation/runs/github-update-2306127245/verified-update.json`
and `provenance.json`. Independently downloaded release assets are in
`temp/release-0.4.1-verification/`.

## Earlier native UI access limitation (resolved by coordinated audit below)

The startup button, progress, retry and restart states passed browser tests;
real Windows self-replacement and launching a replacement while the old
process runs passed integration tests. A complete native GUI click-through
from startup through Update now and Restart now is still unverified.
Computer Use state capture for the disposable VoicX-Update-E2E.exe instance
returned `Computer Use app approval timed out`. The user was asked to approve
that app access; no reply had arrived at this verification point. The separate
ALPHA/BRAVO/CHARLIE native audit instances were not touched. Our disposable
client process was stopped after verifying its executable path.

This is the first signed release. Older development clients without an embedded
trusted signing key require a manual install of 0.4.1; they cannot securely
bootstrap this new trust root through their own updater.

Original unrelated workspace changes were preserved. The updater source
fixes are also present in the original workspace; release version changes
and commits are in the isolated release checkout.

## Follow-up: incompatible executable rejection (0.4.2)

The separate native audit reproduced a correctly signed Linux ELF replacing its disposable Windows executable. The visible dialog reported success, then Restart now failed with Windows incompatibility. The installed production v0.4.1 asset was independently verified as valid AMD64 PE32+; this was a missing defensive check for incorrectly packaged future releases.

The patch authenticates the manifest and downloaded checksum as before, then validates the same open file's Windows AMD64 executable format, headers, alignments, section bounds, and backed entry point before replacement. It does not promise that all correctly structured signed program code will run correctly. Checks follow Microsoft's PE format specification: https://learn.microsoft.com/en-us/windows/win32/debug/pe-format.

The initial independent review found missing zero-alignment and undersized-header rejection. Those findings were reproduced, repaired, and independently closed. The final helper accepts the published v0.4.1 client and the native baseline, preserves caller file ownership and offset, and rejects the reproduced invalid fields.

Release checkout commit: 79a6032f50d920813d969d474b475a40d7761866.
Local pinned Go 1.26.6 validation:
- Full client race and integration suite passed in 163.232 seconds before the final header-only follow-up.
- Final signed updater race suite passed all 29 cases in 186.304 seconds: 11 existing signature/checksum/metadata/download/valid replacement cases plus 18 signed platform/header rejection cases.
- Integration-tagged lint: zero issues.
- Version metadata, manifest, and signer tests passed.
- Each rejection preserves the disposable installed executable and user-data fixture; valid replacements launch the version probe while the old process remains alive.

Patch branch CI: https://github.com/arumes31/voicx/actions/runs/34762743551.
Publication and post-release live verification are pending at this point.

## Native update flow verified by the coordinated audit

The ALPHA/BRAVO/CHARLIE native audit (same workspace, task 01a09973-446f-7641-985e-fa17abdea498) completed the previously missing Wails interaction using disposable installations and a test signing key:
- The rebuilt native updater rejected the correctly signed Linux ELF and preserved the baseline executable hash.
- A denied write in the disposable installation showed a recoverable native error and preserved the executable; the test restored the original ACL.
- Clicking Update now on a valid signed Windows candidate installed the candidate, and Restart now launched a new process from the expected executable path with the exact candidate hash.
- Public identity and trusted-server file hashes matched before/after. Bookmark and settings values were preserved, and private-message history remained visible.
- Updated ALPHA exchanged fresh encrypted private messages with unchanged BRAVO. CHARLIE did not receive those private messages and successfully participated in a separate global conversation.

Evidence under temp/ui-20260913-01/evidence: update-unwritable-green.json, update-native-relaunch.json, pre-update-profile.json, post-update-profile.json; broader native report: docs/native-ui-integration-2026-09-13.md. These are native fixture-server tests, distinct from the real GitHub artifact verification below.

The full patch branch CI and lint passed. Tag v0.4.2 points to 79a6032f50d920813d969d474b475a40d7761866. Tagged build/sign/publish run: https://github.com/arumes31/voicx/actions/runs/34763143636.

## Final published result: v0.4.2

Published stable/latest at 2026-09-13 14:44:43 UTC:
https://github.com/arumes31/voicx/releases/tag/v0.4.2.
All tagged CI jobs passed, including build, signing, publication and container image. Tagged lint run 34763143512 also passed.

Independently downloaded assets passed manifest signature, SHA-256, and GitHub provenance verification restricted to arumes31/voicx/.github/workflows/ci.yml:
- Windows client: 15,423,488 bytes, SHA-256 c54585c7126821626ca5857148d0e03f6895120b066f465d26189bfd68cf9684.
- Linux server: 28,074,146 bytes, SHA-256 d42eb1642f2f497c1cc0c9185676dc8ac46559e7a5c6e7c6918804847bcb8ae1.
Evidence: temp/release-0.4.2-verification/, including both provenance JSON results.

The pinned Go 1.26.6 live harness ran production CheckForUpdate/DownloadAndApply with current version 0.4.1 against actual GitHub v0.4.2. PASS in 33.55 seconds: release metadata and signature verified, disposable executable replaced with the exact published client hash, fixture data preserved, temporary downloads cleaned, and original test runner unchanged. Evidence: temp/live-github-updater-validation/runs/github-update-3530142355/verified-update.json and child-output.log. This GitHub harness does not launch the downloaded native client; native GUI update/restart and preserved profile/messaging are separately verified by the coordinated audit above.

No signing-key rotation occurred. Existing v0.4.1 release clients trust v0.4.2 and receive the startup Update now offer when automatic update checks are enabled. Development builds without a compiled trusted signing key still need a one-time manual installation of the signed release.

The isolated release checkout is clean at 79a6032f50d920813d969d474b475a40d7761866. Unrelated original-workspace edits were preserved.
