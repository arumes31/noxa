# noXa desktop client

The Wails v2 desktop client connects to the noXa Go server for voice, video,
screen sharing, channel chat and end-to-end encrypted direct/group messages.
Published binaries target **Windows x64**. Linux and macOS have source/platform
support; they do not currently have published desktop release binaries.

- [Website and downloads](https://noxa.wowcraft.pw/)
- Public test server: **`wowcraft.pw`**, default control port `12333`
- [Mobile / browser PWA](https://noxa.wowcraft.pw/app/)
- [Server setup and complete feature reference](../README.md)

## Current interface

The desktop uses **OLED Midnight**, with locally bundled fonts, icons and
notification audio. The top menu contains Connections, Settings, View, Tools
and Help. Windows controls share that bar. The sidebar contains channels and
the current user's voice controls.

![Actual desktop UI with fictional demo data](../website/public/assets/screenshots/app-chat.png)

- **Connections:** connect/disconnect, server information, bookmarks and
  **PWA / Mobile login**. The mobile dialog provides a QR code, a direct PWA
  link, device/passkey management and platform installation guidance. The
  **noXa Mobile** sidebar shortcut opens the same setup.
- **Settings:** application settings, microphone and playback, camera,
  notifications, hotkeys, whisper lists, identity and local storage. Change
  display name and presence from this menu. Optional remembered passwords use
  the Windows account's OS-protected credential storage.
- **Activity:** mentions, recent accessible conversations, friends, media,
  calls and notifications. Audit is available only with the required server
  permission; the server enforces that access independently of the UI.
- **Chat:** channel/direct-message tabs, replies, reactions, polls, threads,
  files, voice messages, search, Inbox and saved messages. Chat subscriptions
  and voice membership are separate.
- **Voice and video:** channel and private/group calls, explicit camera and
  screen capture, microphone recovery, per-member voice/share-audio controls,
  echo tests and stream diagnostics. Camera capture starts only on request.
- **Tools → Roles:** named roles, hierarchy, member assignments and role icon
  uploads with displayed/enforced limits. Channel access can be synced with a
  parent or customized per role/member.
- **Member menus:** profile, message/call, personal audio, Client Info and
  permitted moderation actions. Only authorized viewers receive privileged
  connection/diagnostic information.

The screenshots are captures of real components with fictional local API
fixtures, not production chats. [Capture and refresh instructions](../website/README.md#product-screenshot-provenance).

## Build and development

Use **Go 1.27.3 or later**, **Node.js 24 or later**, and Wails matching
[`go.mod`](go.mod):

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0
```

Windows requires WebView2. Linux requires the GTK/WebKitGTK and desktop
libraries installed by the [CI workflow](../.github/workflows/ci.yml).

From the repository root:

```sh
npm --prefix client/frontend ci
make client-build
```

Or in Windows PowerShell:

```powershell
./scripts/build.ps1 client
```

Build output is under `client/build/bin/`. Version metadata comes from the
source revision and release tags. Custom builds that use the signed updater
must embed trusted **public** signing keys; see the [root README](../README.md#quick-start).

For frontend/native development, configure a local server using the
[new-server setup](../README.md#new-server-setup), then run `wails dev` from
`client/`. The frontend expects the native Go bridge; opening the desktop
frontend in a browser is not a standalone client. Use `mobile/` for the PWA.

## Checks

From `client/frontend/`:

```sh
npm run quality
npm run test:e2e
```

From the repository root, `make test` runs root/client Go tests and supplies
the embed placeholder needed for source tests. Database-backed tests use a
separate test database. CI also validates Windows, the WebView2 fork, browser
scenarios, race checks and security analysis. Do not point destructive test
fixtures at a production database.

## Identity, privacy and updates

The client keeps settings, identity and logs under the OS user configuration
directory's `noxa` folder. Treat identity exports, local chat data and logs as
private. Import the same identity to recover it; changing a contact's signing
key requires explicit fingerprint confirmation.

The control and file-transfer connections use TLS and pinned server identity.
Direct-message bodies use device-specific recipient keys with X25519 and
XSalsa20-Poly1305; they do not currently use the separate Double Ratchet library
or provide its per-message forward secrecy. Channel messages use server-managed
scope keys. WebRTC channel media is protected in transit to the SFU, without
channel media E2EE. See the root README for the precise encryption boundaries.

A linked phone normally signs in with its saved device key or a passkey.
Trusted desktops automatically endorse passkey-created devices only after
verifying the signed proof. Normal phone sign-out preserves its passkeys; the
first/master device cannot be remotely signed out from another device.

The updater verifies a signed manifest and artifact hash, then supervises
restart. Failed startup restores the previous executable, without rolling back
identity, settings or message history. Keep recovery copies of important local
data independently of application updates.
