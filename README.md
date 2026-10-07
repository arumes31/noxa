<div align="center">

<img src="client/frontend/public/branding/logo.png" alt="noXa logo" width="160" height="160" />

# noXa

**Self-hosted voice, video, screen sharing, and chat**

*Go server, Wails desktop client, Pion WebRTC media routing, PostgreSQL persistence, and named roles with channel access overrides.*

[![CI](https://github.com/arumes31/noxa/actions/workflows/ci.yml/badge.svg)](https://github.com/arumes31/noxa/actions/workflows/ci.yml)
[![golangci-lint](https://github.com/arumes31/noxa/actions/workflows/golangci-lint.yml/badge.svg)](https://github.com/arumes31/noxa/actions/workflows/golangci-lint.yml)
[![Security Analysis](https://github.com/arumes31/noxa/actions/workflows/security.yml/badge.svg)](https://github.com/arumes31/noxa/actions/workflows/security.yml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/arumes31/noxa)](https://go.dev/)
[![Container Image](https://img.shields.io/badge/container-ghcr.io-blue?logo=docker)](https://github.com/arumes31/noxa/pkgs/container/noxa)
[![License](https://img.shields.io/github/license/arumes31/noxa)](LICENSE)

[Architecture](#architecture) • [Features](#features) • [Quick Start](#quick-start) • [Server Setup](#new-server-setup) • [Permissions](#permissions) • [ServerQuery API](#serverquery) • [Configuration](#configuration) • [Development](#development-and-checks) • [Operations](#operations)

---

<img width="1280" height="800" alt="noXa desktop client" src="https://github.com/user-attachments/assets/d3ed3136-b468-476c-8e99-2de600d3c0f5" />


</div>

## 🌟 Overview

**noXa** combines voice channels, camera and screen sharing, direct and group
conversations, and channel forums in a desktop client. Its Go server uses a
framed control protocol, a **Pion WebRTC SFU**, PostgreSQL for persistent state,
and optional Redis pub/sub. Named roles control access and moderation.

The published desktop binary targets **Windows x64**. The Wails source also has
Linux/macOS platform code; CI exercises Windows and Linux, but does not publish
Linux/macOS desktop releases. The Vite frontend uses the native Go bridge: it is
not a standalone browser client. [`website/`](website/README.md) is a separate
static product/download website.

> [!NOTE]
> **Encryption boundaries:** Desktop direct-message bodies use X25519 +
> XSalsa20-Poly1305 (NaCl box); the server relays encrypted payloads, including
> offline delivery. The current desktop DM path does not use the repository's
> X3DH/Double Ratchet library, so it does not provide that library's per-message
> forward secrecy. Channel/global messages and forum bodies use server-managed
> scope keys and can be decrypted by the server. Channel audio/video uses
> WebRTC DTLS-SRTP to the SFU; channel media E2EE is
> [not yet integrated](docs/media-e2ee-integration.md).

---

<a id="architecture"></a>

## 🏗️ System Architecture

```mermaid
graph TD
    subgraph Clients["Clients"]
        Wails["Wails Desktop Client\nGo bridge + webview UI / WebRTC"]
        Bot["Integration clients / CLI\nServerQuery / gRPC / events"]
    end

    subgraph CoreServer["noXa Server Core"]
        Control["TCP Control Listener\n:12333 (TLS / TOFU)"]
        Keepalive["UDP Keepalive Worker Pool\n:12334"]
        WebRTC["Pion WebRTC SFU Engine\n(DTLS-SRTP / ICE / Opus)"]
        Query["ServerQuery Admin Protocol\n127.0.0.1:12335 (Raw) / :12339 (SSH opt-in)"]
        GRPC["gRPC Administration\n127.0.0.1:12338 (loopback only)"]
        FileXfer["File Transfer Service\n:12336 (TLS 1.3 / Token Authorized)"]
        Health["HTTP Service :12337\nHealth, metrics, events, webhooks, file links"]
    end

    subgraph DataStore["Persistence & Messaging"]
        Postgres[(PostgreSQL 16\nStore, State, Audit Logs)]
        Redis[(Redis 7\nOptional pub/sub)]
    end

    Wails <-->|TLS Control JSON| Control
    Wails <-->|UDP WebRTC Media| WebRTC
    Bot <-->|TCP Text Commands| Query
    Bot <-->|gRPC| GRPC
    Bot <-->|Authorized event WebSocket| Health
    Wails <-->|Token Upload/Download| FileXfer

    Control --> Postgres
    Control --> Redis
    Query --> Postgres
    GRPC --> Query
    WebRTC --> Control
```

### Protocol Specifications

| Protocol Port | Service Component | Wire Format / Transport | Security & Auth |
| :--- | :--- | :--- | :--- |
| **`TCP :12333`** | Control Engine | Length-prefixed JSON frames over TLS 1.3 | Ed25519 Challenge / Argon2id / TOFU Pinning |
| **`UDP :12334`** | Connection Probes | Datagram Ping/Pong Keepalive | Session Token Verification |
| **`UDP dynamic or configured port`** | WebRTC SFU Engine | DTLS-SRTP (Opus audio, negotiated video codecs) | ICE candidate negotiation & SRTP encryption; separate from the keepalive listener |
| **`TCP 127.0.0.1:12335`** | ServerQuery Protocol | Line-based ASCII / UTF-8 plaintext stream | Loopback by default; remote binding requires explicit opt-in, and SSH is preferred |
| **`TCP :12336`** | File Transfer Engine | Binary frames over TLS 1.3 | TOFU-pinned certificate plus an ephemeral single-use token |
| **`TCP :12337`** | HTTP services | Health/readiness, Prometheus, `/debug/voice`, `/debug/streams`, `/events`, `/hooks/`, `/dl/` | Media diagnostics stay loopback-only; metrics require explicit remote opt-in; pprof is opt-in and loopback-only. Remote authenticated HTTP routes need an HTTPS proxy |
| **`TCP 127.0.0.1:12338`** | gRPC administration | Plaintext gRPC | Integration account authentication and current roles; loopback binding is mandatory |
| **`TCP :12339`** | ServerQuery over SSH | SSH-wrapped command stream | Disabled by default; integration credentials and `roles-v1` negotiation |

---

<a id="features"></a>

## ✨ Key Features

### 🎙️ Voice, video, and screen sharing

* **Pion WebRTC SFU**: Routes individual publisher tracks to authorized subscribers, with separate camera, screen, microphone, and shared-audio streams. A new video receiver briefly probes the measured source rate and keeps an increase only when receiver feedback confirms it; congestion and missing feedback end that startup probe.
* **Opus controls**: Per-channel bitrate, Forward Error Correction (FEC), Discontinuous Transmission (DTX), and stereo settings. The default bitrate is 32 kbps; latency and capacity depend on the network and host.
* **One quality per stream**: Each camera or screen share uploads one encoding using the sender's selected settings. Screen sharing preserves the selected resolution while actual FPS can adapt to the sender's resources. All viewers receive the same source; a slower viewer needs the sender to lower the source settings manually. Different shares retain independent sender settings. On updated servers, video and screen-share audio upload pause without viewers or an active server recording; capture and previews remain available. Legacy publishers with multiple layers retain their compatible receive controls.
* **Screen-share audio choices**: In channel shares and private calls, choose **No audio**, **Shared application**, or **System audio**. Application audio requires a window and a supported capture runtime; enable audio in the system picker. It can include other windows of the same application. If application-only capture is unavailable or cannot be confirmed, video still starts without audio and a short notification explains the fallback. System audio is only shared when explicitly selected.
* **Screen-share resolution**: The channel share dialog offers 720p, 1080p, 1440p, 4K, **Original source resolution**, and **Custom** dimensions (160–8192 pixels, 15/30/60 fps). The encoder has 50 Mbps of headroom instead of Chromium's implicit ~2.5 Mbps default; this is a ceiling, not a target or minimum. Screen-share presets preserve resolution while actual frame rate can adapt to network and encoder capacity, without an additional system-CPU-triggered 500 kbps cap. Server limits and explicit Low bandwidth mode still apply.
* **Live share controls**: **Change quality** adjusts a running channel share's resolution and frame rate without reopening the capture picker or changing its source/audio. It updates the one encoding received by every viewer. Watched-stream details show measured resolution, decoded frames per second, codec and payload bitrate separately from the sender's selected settings.
* **Stream diagnostics for viewers**: Any authorized viewer can compare sender capture/encoding/transmission, server ingress/forwarding and local reception/decoding in **Stream details**. A plain-language health summary explains the available evidence and suggests an action for the sender or viewer; live share controls show the sender's summary too. Bitrate, retransmission share, keyframes, frame size and processing delays help locate bottlenecks. Updated senders are required for sender measurements; missing or stale data stays unknown. See [stream diagnostics](docs/stream-diagnostics.md).
* **Priority speaker**: Non-priority publishers in the current voice channel are ducked to 25% gain (about −12 dB) while a priority speaker talks.
* **Whisper Routing**: Point-to-point and cross-channel targeted voice transmission bypasses standard channel boundaries.
* **Microphone recovery**: If the selected microphone disconnects, receiving audio and video continues. Choose and apply a device in **Capture** settings, then select **Retry microphone**. A replacement microphone never starts automatically.
* **Personal audio controls**: **Personal audio…** in a member's menu opens a compact popover with separate voice and screen-share volume/mute controls, percentage and actual gain in dB. Personal volume uses 100% as unity and reaches +20 dB at the 200% endpoint, with a limiter for loud peaks. Capture settings include push-to-talk, voice activation with a 450 ms release hold, continuous transmission, and local microphone testing. Guided microphone calibration recommends thresholds in the same 0.1% steps as the manual control.
* **Private calls**: Accepted direct/group calls carry voice, camera, and screen sharing without moving participants into a voice channel.
* **Network echo test**: In **Capture** settings, explicitly join the server's echo channel to hear your microphone through the normal voice connection. Only you hear your microphone: other participants cannot hear you and you cannot hear them. Echo media is excluded from whispers, cross-participant video, and channel recordings. Mute and push-to-talk still apply; wear headphones. The return button restores your previous channel while the test remains active on that server tab. Each server automatically creates `Echo Test` on startup after role setup, granting admitted users permission to view, join and speak only in that channel. Existing channels with the configured name retain their access rules, custom metadata and history; startup updates only the known old system-created echo topic. Set Docker environment variable `NOXA_ECHO_CHANNEL_ENABLED=false` and recreate the server container to disable creation and loopback; existing channels and history are retained. Configure the name with `NOXA_ECHO_CHANNEL_NAME` or `echo_channel_name` in YAML.
* **Connection benchmark**: A separate, explicitly started test in **Capture** sends synthetic Opus packets for 20 seconds through the server's actual WebRTC endpoint and private Echo Test. It uses a temporary guest with the current server's pinned certificate, without microphone/speaker access or moving your existing voice connection. Results include round-trip timing, arrival gaps and unmatched packets after a short drain period; Cancel, tab disconnect and application shutdown clean up the guest. Guest admission and private echo support are required. This measures packet delivery, not perceived audio quality or maximum video throughput.

### 💬 Messaging and collaboration

* **Encrypted messages and attachments**: Direct messages use recipient keys; channel/global bodies use server-managed scope-key generations. Attachment keys travel inside their encrypted message bodies.
* **Message tools**: Emoji reactions, replies, pins, polls, voice messages, typing indicators, read receipts, and mentions. [Inbox, history search, and saved messages](docs/message-tools.md) are scoped to the current server; history search decrypts and matches bodies in the native client.
* **Chat navigation**: Channel and direct-message tabs stay on one row, with an **All chats** menu for overflow. Right-click to pin or reorder, or drag within the pinned/unpinned section. Arrow keys move focus, Enter opens a chat, and Ctrl+Shift+Left/Right reorders it. Order and pins are saved locally, encrypted and separated by server and identity; closing a tab removes its pin. **Recently closed** in All chats and **Ctrl+Shift+T** reopen explicitly closed chats from the current session (up to ten); channel access is checked again. This list resets when the server or identity changes.
* **Chat media**: Enlarging an attached video keeps the same player, playback position, volume, speed and pause state. Animated images and chat videos pause offscreen or after a minute without window focus; only visible media resumes, and manually paused videos stay paused.
* **Threads and forums**: Persistent posts/replies, tags, following, unread state, archive/reopen, resolved questions, and pinned posts, under the parent channel's access rules. See [threads and forums](docs/threads-and-forums.md).
* **Incoming webhooks**: Revocable, channel-scoped integration tokens; posts remain subject to the creator's current access. See [incoming webhooks](docs/incoming-webhooks.md).
* **Moderation**: Role-controlled message and thread moderation, domain allow/block lists, word filters, duplicate suppression, and rate limits. Server-side content filtering does not inspect E2EE direct-message bodies.

### 🖥️ Desktop experience

* **Connections**: Multiple server tabs, bookmarks, last-successful-connection prefilling, optional OS-protected password storage, and automatic reconnect after connection loss. Login errors explain the next step and reveal the relevant field, distinguishing account credentials, server passwords, connection failures and certificate problems while retaining technical details for support.
* **Speaking and unread indicators**: Animated avatar highlights, alphabetical channel member lists, stream indicators, and unread direct-message badges that keep pulsing until opened.
* **Windows overlay**: Individual active speakers with avatars and animation over the desktop or windowed/borderless games. Configure it in **Settings → Overlay**; local mute/deafen hides it. Exclusive-fullscreen games may cover the overlay.
* **Notifications**: Per-event controls, volume and previews, with bundled English/German spoken announcements. Notification history links to the originating conversation and message when available, with server and identity checks. Same-server navigation leaves voice membership unchanged; opening another server asks first when switching would interrupt active voice, a call or a share. Events with speech use that recording without a duplicate beep. Playback follows the selected audio output device; no network TTS service is required.
* **Diagnostics**: A playback-health badge evaluates recent voice buffering, concealment and loss separately from ping. Client Info exposes visible members' ping and reported client version; owners and administrators can inspect recent voice history and correlated sender/SFU/receiver measurements. See [Operations](#operations).

<a id="permissions"></a>

### 🛡️ Roles and Channel Access

* **Named roles**: `@everyone` supplies the baseline; a member's role grants combine. Role order controls delegated management, moderation targets, and appearance.
* **Channel access**: Channels either sync with their parent or use custom overrides. Overrides use Inherit / Allow / Deny for roles and individual registered members.
* **Predictable resolution**: Start with server role grants, apply the channel's `@everyone` overrides, combine role overrides (allow wins at this stage), then apply member-specific overrides. Capability prerequisites and moderation restrictions still apply.
* **Protected ownership**: Owner and Administrator bypass configurable channel overrides, but not account bans, admission checks, ownership protection, hierarchy checks, or operational limits. Only the owner can grant Administrator or transfer ownership.
* **Explainable changes**: Check access shows the effective decision and its source. Revision checks prevent stale saves; role/access mutations are audited.

The current server and client require the coordinated `roles-v1` model and a fresh database. Existing legacy databases are not automatically migrated. See [fresh setup](docs/role-setup-preflight.md) and [the authorization contract](docs/capability-enforcement.md).

---

<a id="quick-start"></a>

## ⚡ Quick Start

For the desktop client, download `noxa-client-windows-amd64.exe` from
[GitHub Releases](https://github.com/arumes31/noxa/releases/latest).
Releases also include `noxa-server-linux-amd64`, checksums, and a signed manifest.
Container builds publish `linux/amd64` and `linux/arm64` images to
[`ghcr.io/arumes31/noxa`](https://github.com/arumes31/noxa/pkgs/container/noxa).
See [update signing](docs/update-signing.md) for verification and signing-key setup.

> [!TIP]
> The fastest way to run noXa is using **Docker Compose**.

### Option 1: Docker Compose (Recommended)

1. Clone the repository:
   ```bash
   git clone https://github.com/arumes31/noxa.git
   cd noxa
   ```

2. Create an explicit development environment, build the server image,
   and start its database and Redis dependencies:
   ```bash
   cp .env.example .env
   docker compose build noxa
   docker compose up -d postgres redis
   ```

   The sample uses development credentials and publishes ports on all host
   interfaces. Restrict it with host bindings/firewall rules during setup. Before exposing a
   deployment, set `NOXA_DEV_MODE=false`, replace the sample PostgreSQL
   credential, and set `POSTGRES_SSLMODE` to `require`, `verify-ca`, or
   `verify-full` (or supply `NOXA_COMPOSE_DATABASE_URL` with that sslmode).
   Each Compose secret also supports an `_FILE` counterpart; configure exactly
   one non-empty source. Set an `_FILE` value to a readable host path; Compose
   mounts it read-only at `/run/secrets/...`. Prefer a path outside the
   repository—`docker/secrets/.empty` is only the checked-in empty fallback.
   On Linux, keep the source directory root-owned `0700` and each source file
   root-owned `0444`; Compose mounts individual files, never the directory.
   This permits the non-root service reader without exposing host traversal.
   Production startup rejects the sample credential and plaintext database
   transport.

   For an external PostgreSQL URL, use both Compose files on every command:
   `docker compose -f docker-compose.yml -f docker-compose.external-db.yml ...`.
   The override removes the bundled database dependency; start only `redis`
   instead of `postgres redis`, and ensure the external database is ready.

3. Follow [New server setup](#new-server-setup) below to create the owner,
   activate roles, and start the server. Admin privilege tokens have been retired.

### Option 2: Building from Source

Successful `main` builds publish signed stable releases
and mark them **Latest** for the client updater. Each new commit advances the
highest stable patch tag; rerunning a published commit reuses its tag.
`VERSION` and the package declarations set the minimum version on that major/minor
release line. CI stamps the actual release patch into the binaries, Windows
package resources, and signed manifest. Prerelease tags do not advance the stable
sequence. Change the synchronized baseline declarations to start a new release line.

The desktop updater shows download, verification, installation and restart stages.
Downloads can be cancelled before verification begins. **Restart now** keeps the
current app open until the replacement confirms startup; failed startup restores
the previous executable without rolling back settings, identity or chat history.
This automatic recovery applies to the supervised restart, not a later manual
launch after closing the app. A uniquely named `.noxa-previous-*.exe` backup is
retained beside the executable for manual recovery.

#### Prerequisites

* **Go**: `>= 1.27.1` for the root and client modules; the separately tested WebView2 fork declares its own version.
* **Node.js**: `>= 24`
* **Wails CLI**: `go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0` (match `client/go.mod`)
* **PostgreSQL**: `>= 16`
* **Desktop platform dependencies**: WebView2 on Windows; GTK 3, WebKitGTK 4.1, Ayatana AppIndicator, X11/XTest development libraries on Linux. The [CI workflow](.github/workflows/ci.yml) lists the Linux packages it installs.
* **Optional services**: Redis 7 for pub/sub, TURN for restrictive NATs, and FFmpeg for server recording (included in the Docker image).

#### Build Backend Server

```bash
# Build the standalone server with automatic Git version metadata
make build

# Inspect the exact version embedded by this source state
make version

# After creating the owner and activating roles (see New server setup below),
# start a local-development server. Use your configured database URL.
NOXA_DEV_MODE=true NOXA_DATABASE_URL="postgres://noxa:noxa@localhost:5432/noxa?sslmode=disable" ./bin/noxa
```

On Windows PowerShell, use the equivalent native wrapper:

```powershell
./scripts/build.ps1 version
./scripts/build.ps1 server
```

`make build` writes `bin/noxa`; the PowerShell server wrapper writes
`bin/noxa-server.exe`. For a source installation without Redis, also set
`NOXA_REDIS_ENABLED=false`.

#### Build Desktop Client

```bash
# From the repository root
make client-build
```

```powershell
./scripts/build.ps1 client
```

Stable versions come from `vMAJOR.MINOR.PATCH` tags. Untagged commits and
dirty trees receive deterministic commit/content metadata automatically; see
[`docs/versioning.md`](docs/versioning.md). Plain `go build` and `wails build`
also use Go's embedded VCS information, while the Make targets additionally
stamp the exact dirty-tree fingerprint into the binary.

The client build writes to `client/build/bin/`. Custom builds that need the
signed updater must embed trusted public keys using `NOXA_UPDATE_PUBLIC_KEYS`
for Make or `-UpdatePublicKeys` for the PowerShell wrapper; see
[update signing](docs/update-signing.md). The private signing key is never part
of the client build.

---

## New server setup

Use a fresh PostgreSQL database and matching current server/client versions.
These steps initialize a new installation; do not run them to reset an existing
server. The examples use Bash and the repository's Compose service name `noxa`.
Keep the development stack restricted to your machine while setting it up; the
sample Compose file publishes ports on all host interfaces.

### 1. Create the first account and make it the owner

After the Docker build and dependency startup above, keep the server stopped
while running the offline setup commands:

```bash
docker compose stop noxa
read -r -s -p 'Choose an owner account password: ' OWNER_PASSWORD; printf '\n'
docker compose run --rm --no-deps noxa /out/adduser \
  -nickname owner -password "$OWNER_PASSWORD"
unset OWNER_PASSWORD
```

Store the password securely and copy the exact `unique_id` printed by `adduser`.
The password prompt avoids saving the literal password in shell history, but the
current CLI passes it as a process argument; run this on a trusted operator host.
`adduser` initializes the fresh schema and creates an account; it does not grant
administrative rights by itself. If it reports that the account already exists,
it has not changed that account's password.

Replace the placeholder below with that exact ID, not the nickname:

```bash
docker compose run --rm --no-deps noxa /out/role-setup \
  -owner-uid '<unique_id from adduser>' \
  -activate -confirm ACTIVATE-ROLES-V1 \
  -chat-master-key-file /data/keys/chat_master.key \
  -query-timeout 30s
docker compose up -d noxa
docker compose logs --tail=50 noxa
docker compose exec noxa wget -qO- http://127.0.0.1:12337/readyz
```

Confirm the setup report says `active: true` and readiness returns `ok`.
Activation is a one-time operation and refuses to run alongside a serving
server. The selected account becomes **Server owner**, with full administrative
access. There is no first-join admin claim or privilege token.

For a source installation, build `go build -o bin/adduser ./cmd/adduser` and
`go build -o bin/role-setup ./cmd/role-setup`. With the server stopped, export
your `NOXA_DATABASE_URL` and run those binaries with the same arguments. Set
`-chat-master-key-file` to the server's actual key path (default
`./data/keys/chat_master.key`) and share the same configured key override, if any.
See [role setup and activation](docs/role-setup-preflight.md) for inspection,
failure recovery and process-lock details.

### 2. Connect as owner and grant administrators

Open the desktop client, connect to `localhost:12333` (or your server's hostname),
and enter `owner` as the **Account login / guest name** and the chosen **Account password**. A server
join password, if configured, is separate from the account password. Compare the
server certificate fingerprint with its startup log before trusting it.

To appear as `Daniel`, enter it in the optional **Display name** field. Everyone
on the server sees that name; the `owner` login, identity, and roles stay the same.
Use **Self → Change display name** to change it during a session. The login form
restores the server, account and optional display name from the last successful
connection. A new installation starts with empty fields; a bookmark's **Display
name override** applies when loading that bookmark. Names must contain 1–64 characters without control characters.
Live changes require a server version that supports display names.

Under **Optional details**, **Remember passwords for this connection** stores
account and server passwords separately from settings, protected by Windows
DPAPI for the current OS account. It is opt-in per server/account pair. Uncheck it
to delete that pair's saved passwords immediately. Failed connections do not
replace the remembered profile or passwords. Platforms without OS protection
can remember connection details but do not store passwords.

Open **Permissions → Roles**, then **Members**. Find a registered member,
select the **Administrator** role and choose **Add role** to grant admin access.
Only the owner can grant Administrator or transfer ownership. Keep this role
limited to trusted operators; use Moderator for routine moderation.

In **Permissions → Roles**, the owner can enable **Protect from deletion** for
important roles. Administrator roles start protected. To delete a protected
role, the owner must turn protection off and save first. Deletion then requires
confirmation in a modal and removes the role's assignments and channel overrides.

To provision another password account, stop the server, run `adduser` with a
different nickname/password, then start the server again. Grant its role through
the owner account. Ownership transfer is available in the Members view; it gives
the recipient ownership and removes your owner protection.

### 3. Configure roles and member defaults

The initial policy is closed: `@everyone` has no grants, the Member, Moderator
and Administrator roles are unassigned, and no default member role is selected.

1. In **Permissions → Roles**, select an existing role or **Create role**. Use
   **Start from a template** for Member or Moderator, adjust the permissions,
   and save. Roles add permissions together; a role's name alone grants nothing.
2. Order roles with **Move role up/down**. Managers can only manage roles and
   moderate members below their permitted hierarchy; a permission checkbox
   alone does not bypass that hierarchy.
3. Use **Members → Add role** for existing registered accounts. As owner, set
   **Role for new members** if future registrations should automatically receive
   Member. This saves immediately and does not change existing accounts or
   guests; Administrator cannot be assigned automatically.
   You can also right-click a member and choose **Assign roles…**; this appears
   only when your permissions and role hierarchy allow managing that member.
   This also works for guests using a saved client identity. Adding their first
   role registers that identity for persistent assignments, applies the role
   immediately and retains it on reconnect. Opening the menu changes nothing.
   Ephemeral guests without an identity key cannot retain role assignments.
   Update the server as well as the client to use guest role assignment and
   public display names (including display names for the owner account).
4. Configure `@everyone` only for access you intend every member and guest to
   have. Keep moderation and server-management permissions in dedicated roles.

### 4. Open channels and verify access

Right-click a channel and choose **Channel access**. Existing channels
start with **View channel → Deny** for `@everyone`, so assigning Member alone
does not make them visible.

**Edit channel** also opens **Channel access** and lets you keep, replace, or
remove the channel password. Access rules are edited separately; save metadata
first or confirm discarding unsaved edits when opening access settings.

Use **Preview role changes** before saving an access draft. Each role is checked
with `@everyone`. Search by name or unique ID to inspect a member's saved and
proposed permissions, including their combined roles and individual overrides.

- For a members-only channel, keep that deny and add a Member role override
  allowing **View channel**, along with the desired chat/voice permissions and
  their prerequisites.
- For a public channel, use the **Public** preset and review its `@everyone`
  permissions before saving. Presets change `@everyone`; other exceptions still
  apply.
- Use **Sync with parent** for child channels that should follow the parent's
  access policy, or **Customize this channel** for separate rules. Changes to a
  parent's policy also affect its synced children.
- Save, then use **Check access** for a regular member and a guest. It checks
  saved permissions, not unsaved edits, passwords, bans or resource limits.
  Also connect with a non-admin account to check joining, chat and voice;
  owner/admin access bypasses configurable channel overrides.

### 5. Prepare the server for other users

Before exposing it, configure production database credentials and PostgreSQL
TLS, set `NOXA_DEV_MODE=false`, and choose your server admission policy. Setting
`POSTGRES_SSLMODE` alone does not enable TLS on PostgreSQL: configure its
certificate/key or use a TLS-enabled database. In Compose, explicitly pass any
additional `NOXA_*` settings in the `noxa.environment` section; entries in `.env`
are only used where the Compose file references them.

Publish the control TCP port `12333`, file-transfer TCP port `12336`, the UDP
keepalive port configured by `NOXA_UDP_ADDR` (default UDP `12334`), and the
configured WebRTC media ports. WebRTC needs reachable ICE candidates and suitable
NAT/firewall or TURN configuration; UDP `12334` alone is not the voice transport.
Keep administration and health ports private. See the
[configuration reference](#configuration) for listener settings.

For a Docker bridge or port-forwarded host, a fixed WebRTC UDP port is easier to
publish than the default dynamic ports. For example, add the following to the
`noxa` service in a Compose override, replacing the documentation IP with your
public IPv4 address:

```yaml
services:
  noxa:
    environment:
      NOXA_WEBRTC_UDP_ADDR: ":12367"
      NOXA_WEBRTC_EXTERNAL_IPS: "203.0.113.10"
    ports:
      - "12367:12367/udp"
```

Forward/allow that same UDP port at the router and firewall. External IP
advertising requires a fixed UDP listener; it does not create a port forwarding
rule. Configure STUN/TURN for your network as needed. The bundled `turn` profile
and its relay-port settings are described in [`.env.example`](.env.example).

Back up PostgreSQL together with the matching chat/PII keys, uploaded files,
recordings, TLS identity and configuration. Verify a restore before relying on
the installation; see [backup and restore](docs/operations/backup-restore.md).

---

<a id="configuration"></a>

## ⚙️ Configuration Reference

`NOXA_*` environment variables override `config.yaml`, which is searched first
in the working directory and then in `/etc/noxa`; missing values use built-in
defaults. Nested YAML keys use underscores in environment variables, for example
`webrtc.udp_addr` becomes `NOXA_WEBRTC_UDP_ADDR`. The server does not load `.env`
itself; Docker Compose uses it for interpolation.

This table lists commonly used settings and built-in defaults. See
[`internal/config/config.go`](internal/config/config.go) for the complete schema
and validation, [`config.yaml`](config.yaml) for a development example, and
[`docker-compose.yml`](docker-compose.yml) for container-specific overrides.

| Environment Variable | Default Value | Description |
| :--- | :--- | :--- |
| `NOXA_SERVER_NAME` / `NOXA_SERVER_PASSWORD` | `noXa` / empty | Displayed server name and optional admission password |
| `NOXA_DEV_MODE` | `true` | Development logging/validation; explicitly set `false` for production |
| `NOXA_TCP_ADDR` | `:12333` | Primary control TCP listener address |
| `NOXA_UDP_ADDR` | `:12334` | UDP keepalive ping/pong listener address |
| `NOXA_GRPC_ADDR` | `127.0.0.1:12338` | Plaintext gRPC administration listener; loopback is mandatory |
| `NOXA_QUERY_ADDR` | `127.0.0.1:12335` | ServerQuery admin protocol binding address |
| `NOXA_QUERY_ALLOW_REMOTE` | `false` | Explicitly permit a non-loopback raw ServerQuery bind; prefer SSH instead |
| `NOXA_QUERY_SSH_ENABLED` | `false` | Enable the SSH-wrapped ServerQuery listener |
| `NOXA_QUERY_SSH_ADDR` | `:12339` | SSH ServerQuery listener address |
| `NOXA_FILE_ADDR` | `:12336` | File transfer upload/download listener address |
| `NOXA_HEALTH_ADDR` | `:12337` | Health/readiness HTTP listener; `/dl` bearer links on this listener are plaintext HTTP, so bind loopback or proxy it behind HTTPS |
| `NOXA_METRICS_ALLOW_REMOTE` | `false` | Permit remote `/metrics` requests; without this opt-in, only IPv4/IPv6 loopback is accepted |
| `NOXA_PPROF_ENABLED` | `false` | Enable runtime `/debug/pprof/` diagnostics; every pprof endpoint remains GET-only and direct-loopback-only |
| `NOXA_SHUTDOWN_TIMEOUT` | `30s` | Positive total grace period shared by all services during orderly shutdown |
| `NOXA_DATABASE_URL` | `postgres://...` | PostgreSQL connection URL |
| `NOXA_REDIS_ADDR` | `localhost:6379` | Optional Redis address for pub/sub fanout |
| `NOXA_REDIS_ENABLED` | `true` | Set `false` to run without Redis; the supplied Compose stack starts Redis by default |
| `NOXA_TLS_ENABLED` | `true` | Enable TLS 1.3 encryption on control port |
| `NOXA_TLS_DIR` | `./data/tls` | Directory storing the generated TLS certificate and key |
| `NOXA_TLS_CERT_FILE` / `NOXA_TLS_KEY_FILE` | empty | Custom certificate and key; both must be configured together |
| `NOXA_FILE_TLS_ENABLED` | `true` | Enable TLS 1.3 on file transfers; disabling is development-only |
| `NOXA_FILE_ROOT` | `./data/files` | Root storage path for uploaded channel files & avatars |
| `NOXA_FILE_MAX_CONNECTIONS` | `128` | Concurrent accepted file-transfer connections (1–10000) |
| `NOXA_PII_KEY_FILE` | `./data/keys/pii.key` | AES-256-GCM master key file path for PII encryption |
| `NOXA_CHANNEL_TEMP_LIFETIME_SECONDS` | `60` | Grace period before an empty temporary channel is removed |
| `NOXA_CHAT_MASTER_KEY_FILE` | `./data/keys/chat_master.key` | KEK file used to wrap persisted chat scope keys; back it up with PostgreSQL |
| `NOXA_CHAT_MASTER_KEY` | empty | Secret-injection override for the key file: one base64 32-byte key or a newline-separated `id:base64` key ring; never commit it |
| `NOXA_CHAT_LEGACY_HISTORY` | `encrypt` | One-time handling for legacy plaintext rows: `encrypt` or `purge` |
| `NOXA_CHAT_KEY_ROTATE_MIN_SECONDS` | `60` | Minimum interval used to coalesce scope-key rotations |
| `NOXA_CHAT_SEARCH_MAX_MESSAGES` | `2000` | Maximum history messages scanned by client-side search |
| `NOXA_CHAT_MAX_LENGTH` | `4096` | Maximum decrypted chat payload size in UTF-8 bytes |
| `NOXA_ECHO_CHANNEL_ENABLED` / `NOXA_ECHO_CHANNEL_NAME` | `true` / `Echo Test` | Create the network echo channel and enable isolated loopback |
| `NOXA_DEFAULT_OPUS_BITRATE` | `32000` | Default voice bitrate in bits/s |
| `NOXA_VIDEO_MAX_BITRATE` | `0` | Per-publisher video RTP ceiling in bits/s across slots/layers; `0` means unlimited |
| `NOXA_VIDEO_MAX_WIDTH` / `NOXA_VIDEO_MAX_HEIGHT` | `0` / `0` | Encoded video bounds; both zero means unlimited. Nonzero paired bounds require VP8 |
| `NOXA_WEBRTC_UDP_ADDR` | empty | Shared IPv4 WebRTC UDP listener; empty uses dynamic ports |
| `NOXA_WEBRTC_EXTERNAL_IPS` | empty | Public IPv4 addresses forwarding the fixed WebRTC UDP listener |
| `NOXA_WEBRTC_ICE_SERVERS` | `stun:stun.l.google.com:19302` | ICE server URLs for the SFU |
| `NOXA_TURN_SECRET` / `NOXA_TURN_URIS` | empty / empty | TURN shared secret and advertised relay URIs |
| `NOXA_TURN_CREDENTIALS_TTL` | `24h` | TURN credential lifetime; must be positive and at most 30 days |
| `NOXA_REDIS_DIAL_TIMEOUT` / `NOXA_REDIS_READ_TIMEOUT` / `NOXA_REDIS_WRITE_TIMEOUT` | `5s` / `3s` / `3s` | Redis client timeouts when Redis is enabled |
| `NOXA_REDIS_TLS_ENABLED` | `false` | Enable verified Redis TLS (TLS 1.2+); optional server name and CA file use `NOXA_REDIS_TLS_SERVER_NAME` / `NOXA_REDIS_TLS_CA_FILE` |
| `NOXA_RECORDING_ENABLED` / `NOXA_RECORDING_DIR` | `false` / `recordings` | Opt-in server recording; requires FFmpeg and recording permissions |
| `NOXA_RECORDING_MAX_CONCURRENT` | `4` | Combined budget for starting and active recording sessions |

`NOXA_CHAT_MASTER_KEY` takes precedence over `NOXA_CHAT_MASTER_KEY_FILE`.
Use secret injection for the override; it accepts either a single base64 32-byte
key or a newline-separated `id:base64` key ring for key rotation.

Default member assignment is managed through **Permissions → Roles** in the
active `roles-v1` policy. The retired `NOXA_DEFAULT_GROUPS_ENABLED` setting does
not configure current roles. Video ceilings can also be managed through the
authorized media-limits interface; see [media resource limits](docs/media-resource-limits.md).

### Join by hostname with DNS SRV

The desktop client accepts a hostname without a port in joins and bookmarks.
Publish an SRV record for service `noxa`, protocol `tcp`, pointing to the
server's **control TCP port** (including any externally mapped port):

```dns
_noxa._tcp.voice.example.com. 3600 IN SRV 0 5 23456 node.example.com.
node.example.com.             3600 IN A   203.0.113.10
```

Users enter `voice.example.com` to connect to `node.example.com:23456`.
The target must have an A and/or AAAA record. Lower priorities are tried first;
weights distribute the initial choice among targets with equal priority.
Unreachable targets are retried in resolver order within a 15-second connection
budget. Authentication and certificate trust failures stop the connection.

An explicit port, such as `voice.example.com:23456`, bypasses SRV. With no SRV
record, the client uses the entered hostname on port `12333`. IP literals also
use `12333` when their port is omitted. DNS failures are reported; an SRV target
of `.` declares the service unavailable. SRV lookups time out after 5 seconds.
Bookmarks retain the entered hostname and resolve it again on reconnect.
Certificate pins stay tied to that hostname (with default port `12333`), so
changing the SRV target does not bypass an existing pin.

After an unexpected connection loss or server restart, each server tab retries
automatically until the server returns. Attempts use exponential backoff with
jitter, capped at 30 seconds between attempts. The connection status shows the
next attempt; **Cancel reconnect**, **Disconnect**, or closing the tab stops
recovery. The reconnect-on-loss setting can disable it altogether. Authentication
rejections and certificate trust failures stop automatic recovery and require
manual attention; changed certificates are never automatically trusted.
Recovery keeps the same tab and requests its previous voice channel through the
normal permission checks. If that channel is unavailable or needs a password,
the client reports the failure and lets you choose a channel. Mute, deafen, and
voice activation mode are retained; cameras and screen sharing remain off.

This uses standard [DNS SRV records (RFC 2782)](https://www.rfc-editor.org/rfc/rfc2782)
and Go's [priority/weight-ordered SRV resolver](https://pkg.go.dev/net#Resolver.LookupSRV).

### Logging

Production logging uses Zap sampling: for each repeated message in a sampling
tick, it writes the first 100 entries and then every 10th entry thereafter.
Development logging keeps Zap's unsampled development configuration.

### Certificate trust and rotation

The generated certificate under `NOXA_TLS_DIR` is the server's persistent
identity. Back up that directory with the server data volume; replacing or
losing it changes the fingerprint seen by every client.

On first connection, the desktop client pins the control certificate's SHA-256
fingerprint. Later changes fail closed. For a planned rotation:

1. Generate or install the new certificate and record the fingerprint printed
   by the server at startup.
2. Verify that fingerprint with users over a separate trusted channel.
3. Reconnect. In the certificate-changed warning, compare the presented value
   with the verified value and choose **Trust new fingerprint** only when they
   match.
4. Reconnect once more and confirm the connection-security message reports the
   expected fingerprint. If verification fails, abort and restore the previous
   certificate and key; do not delete `known_servers.json` to bypass the check.

---

<a id="serverquery"></a>

## 💻 ServerQuery Admin Protocol

noXa exposes a line-based administrative text interface on `127.0.0.1:12335`
for host-local automation. The raw protocol is plaintext: a non-loopback bind is
rejected unless `NOXA_QUERY_ALLOW_REMOTE=true` is set explicitly. For remote
administration, enable the SSH transport on port `12339` instead. Docker Compose
does not publish either administration port by default; publish `12339` when
enabling Query SSH.

### Key ServerQuery Commands

| Command | Arguments | Description |
| :--- | :--- | :--- |
| `login` | `<unique_id_or_nickname> <password> authorization_model=roles-v1` | Authenticate an integration-enabled account; current roles determine access |
| `clientlist` | none | List currently visible connected clients |
| `channellist` | none | List currently visible channels |
| `channelinfo` | `cid=<channel_id>` | Inspect a visible channel |
| `rolelist` | `[cid=<channel_id>]` | Read the authorized role/access editor state and its revision |
| `rolechange` | `data=<escaped_JSON_with_expected_revision>` | Change roles, assignments or access under hierarchy checks |
| `membermove` | `data=<escaped_JSON_with_client_id_and_destination_channel_id>` | Move a member under current channel and hierarchy checks |
| `memberkick` | `data=<escaped_JSON_with_client_id>` | Remove a member from the server |
| `memberban` | `data=<escaped_JSON_with_client_id_and_duration_seconds>` | Ban a connected member |

Provision integration accounts offline with `adduser -integration`; this enables
login but grants no roles. Current capabilities and hierarchy apply to every
operation, including existing sessions. Retired numeric permissions and privilege
tokens have no compatibility fallback. Use `help` for the complete command list
and [integration access](docs/integration-role-access.md) for JSON schemas,
ServerQuery escaping and conflict handling. SSH sessions must separately negotiate
`NOXA_AUTHORIZATION_MODEL=roles-v1`; see [model negotiation](docs/authorization-model-negotiation.md).

<details>
<summary><b>Click to expand ServerQuery session example</b></summary>

```text
$ telnet 127.0.0.1 12335
NOXA ServerQuery <server_version>
type 'help' for a list of commands
login <integration_nickname> <account_password> authorization_model=roles-v1
authorization_model=roles-v1
error id=0 msg=ok
quit
error id=0 msg=ok
```

Replace the angle-bracket placeholders with the provisioned account credentials
(escape spaces as `\s`). After login, run `channellist` or `rolelist` to obtain
current IDs and revisions before constructing a mutation; examples must not
assume fixed IDs or a universal administrator account.

</details>

---

## Development and checks

Build the frontend before Go client tests so embedded assets are present:

```sh
# From the repository root
npm --prefix client/frontend ci
npm --prefix client/frontend run build
go test ./...
```

Then run client and browser checks in their respective directories:

```sh
cd client
go test -tags=integration -count=1 ./...
cd frontend
npx playwright install chromium
npm run quality
npm run test:e2e
```

On Linux, install the desktop libraries listed above and Playwright's Chromium
system dependencies (`npx playwright install --with-deps chromium`). Headless
native-client tests in CI use `xvfb-run -a go test ./...`.

| Command | Scope |
| :--- | :--- |
| `make test` | Root and client Go tests; supplies an embed placeholder, but does not run browser tests |
| `go run ./cmd/version -check` | Check synchronized baseline versions and report the current source version |
| `npm run quality` in `client/frontend` | Frontend lint, unit tests/coverage, accessibility scenarios, and production build |
| `npm run test:e2e` in `client/frontend` | Full Playwright browser regression suite |
| `go test -tags integration -run '^TestBrowserScreenSimulcast$' -count=1 ./internal/webrtc` | Real Chromium publishers/receiver through a local SFU; requires the frontend dependencies and installed Chromium |
| `go test ./...` in `client/third_party/go-webview2` | Standalone Windows WebView2 fork tests |
| `buf lint` / `make proto` | Lint protobuf contracts / regenerate committed Go stubs; CI pins Buf 1.72.0 |

Database-backed tests need a **disposable PostgreSQL database** through
`NOXA_TEST_DATABASE_URL`; some fixtures also create scratch databases and need
that privilege. Without a configured reachable test database, some cases skip.
Use a separate test instance, not a production database. CI starts PostgreSQL 16
and Redis 7, runs Go race/coverage checks (70% root-internal and 50% client
minimums), Windows client tests, browser tests, protobuf checks, lint, security
analysis, and a backup/restore drill. Passing a local subset does not replace
these gates. [`.github/workflows/ci.yml`](.github/workflows/ci.yml) contains the
exact commands; [role E2E testing](docs/role-e2e-testing.md) describes the live
protocol fixtures. Website checks are separate in [`website/README.md`](website/README.md).

The browser/SFU test checks independent single-encoding shares, upload suspension
without viewers, resumption and sender-controlled quality changes using synthetic
canvas capture. CI runs
the 720p30 profile. On an idle development machine, set
`NOXA_BROWSER_MEDIA_PERF=1` to include the more demanding 1080p60 profile; its
observed frame rate still depends on software-encoder and host capacity.

---

## 📂 Project Structure

```
noxa/
├── client/                     # Desktop Client (Wails v2 / Go + ES6 UI)
│   ├── desktop_windows.go      # Windows COM thread affinity & tray setup
│   ├── frontend/               # Single-page UI (Vite / ES6 / Modular CSS)
│   ├── hotkeys.go              # Global hotkey registration engine
│   ├── third_party/go-webview2/ # Locally maintained WebView2 Go module
│   └── ptt_windows.go          # Win32 Virtual Key low-level PTT observer
├── cmd/
│   ├── adduser/                # Offline account provisioning
│   ├── role-setup/             # Owner selection and roles-v1 activation
│   ├── server/                 # Standalone noXa Server entrypoint
│   ├── signrelease/            # Release manifest signing and verification
│   ├── version/                # Shared build/release version calculator
│   └── migrate/                # Standalone DB migration utility
├── internal/
│   ├── auth/                   # Ed25519 challenge & Argon2id authentication
│   ├── broadcast/              # Outbound message fanout & snapshot engine
│   ├── channels/               # Active channel tree manager
│   ├── e2ee/                   # Crypto primitives, ratchet library, attachment chunks
│   ├── filetransfer/           # Token-authenticated file pipeline
│   ├── netproto/               # Binary frame codec & JSON message definitions
│   ├── authorization/          # Named roles and channel access evaluation
│   ├── query/                  # ServerQuery line-based admin protocol
│   ├── store/                  # PostgreSQL data access layer & migrations
│   └── webrtc/                 # Pion WebRTC SFU, routing and RTCP diagnostics
├── docs/                       # Authorization, media, signing and operator guides
├── proto/                      # Protobuf sources and Buf generation policy
├── v1/                         # Generated Go protocol code
├── website/                    # Static product/download site and its tests
├── .github/
│   └── workflows/              # GitHub Actions (CI, Security, Docker, Lint)
├── Dockerfile                  # Multi-stage production container image
├── docker-compose.yml          # Development defaults; harden before production
├── docker-compose.external-db.yml # Override for external PostgreSQL
└── README.md                   # System documentation
```

---

## Branding assets

The transparent noXa logo is used on the login screen, in the application menu,
and at the top of this README. The original turquoise artwork is preserved;
the checkerboard background has been removed from the source JPEG.

| Asset | Location |
| :--- | :--- |
| Transparent logo (732 × 732) | [`client/frontend/public/branding/logo.png`](client/frontend/public/branding/logo.png) |
| Browser favicon (16–256 px) | [`client/frontend/public/favicon.ico`](client/frontend/public/favicon.ico) |
| PNG icons (32, 180, 192, 512 px) | [`client/frontend/public/branding/`](client/frontend/public/branding/) |
| Desktop app icon (1024 × 1024) | [`client/build/appicon.png`](client/build/appicon.png) |
| Windows app and installer icon | [`client/build/windows/icon.ico`](client/build/windows/icon.ico) |

The system tray embeds the same mark (ICO on Windows, PNG on macOS/Linux).
The frontend includes the favicon and Apple touch icon links. Vite copies the
public assets into the frontend build; Wails uses the desktop assets when the
application is rebuilt. Keep the transparent PNG as the branding source and
resize it when replacing icons, rather than converting it back to JPEG.

---

## Operations

Production operators should adopt the repository's
[service-level objectives](docs/operations/service-level-objectives.md), practice
the [backup and restore drill](docs/operations/backup-restore.md), and keep the
[incident runbook](docs/operations/incident-runbook.md) available outside the
deployment being operated. Update these documents when architecture, telemetry,
or recovery procedures change.

### Voice diagnostics

**Client Info** shows ping and reported client version for visible members.
Owners and administrators additionally see **Reception on this member's client**:
loss, received packets discarded by playout, silent/non-silent concealment,
actual/target/minimum jitter-buffer delay,
adaptive playback acceleration/deceleration, and output state as reported by that
receiver. Unsupported counters display **—**. Older clients may show an unknown version and no client report;
server-side RTCP feedback can still provide loss/jitter measurements. The discard
percentage covers the latest reporting interval; its cumulative packet count is
also included in the operator snapshot. Missing or reset counters remain unknown.
Packets can arrive and still be rejected by playout, so low network loss alone
does not prove uninterrupted audio.

The separate playback-health badge reports recent playback conditions rather
than grading ping. Unknown output, missing/truncated measurements and stale
reports cannot establish healthy playback. Selected ICE protocol and candidate
types are shown without collecting candidate addresses; these cannot identify
an underlying VPN route or relay. Owner/admin diagnostics correlate fresh
publisher SSRCs, SFU ingress/publication observations, subscriber output SSRCs
and receiver reports. Their sample windows and clocks differ, so comparisons
do not establish exact per-stage packet loss or one-way latency.

Updated receivers retain speaking transitions across the measurement interval,
instead of relying on a single instantaneous audio level. Zero RTP energy alone
does not prove silence because Chromium can report it during WebAudio playback.
Known quiet microphone intervals show a
neutral **idle** status rather than grading comfort-noise buffering or concealment
as impaired speech. Actual packet loss/discards and suspended output still surface;
missing activity measurements remain unknown. This only changes diagnostics, not VAD.
Owners/admins can inspect buffer, speech-concealment and loss timelines alongside
the accessible history table. Missing values and collection gaps remain gaps.
Voice, watched-stream, Client Info and Server Info diagnostics share peer-scoped
RTCStats collections (at most 250 ms old), retaining their own interval baselines.

When buffer delay grows, compare the target with the browser's minimum, packet
arrival timing and the selected media route. A VPN exit node or relay can affect
media even when the server has a public address. Test a different route before
changing capture settings or forcing a smaller buffer; a lower buffer preference
cannot remove delivery gaps or override the browser's network-derived minimum.

Operators can query the loopback-only health endpoint from inside the server
container. With the repository's Compose service name:

```sh
docker compose exec noxa wget -qO- 'http://127.0.0.1:12337/debug/voice?nickname=Example'
```

Use either `nickname` or `client_id`, or omit the filter for a bounded snapshot.
Reports contain counters, not audio. In addition to the latest detailed report,
the server retains up to 60 compact summary samples over five minutes in memory
per client. History is isolated by connection, channel epoch and media session;
it is cleared on the relevant lifecycle changes. Samples older than 15 seconds
are marked stale. The endpoint stays
loopback-only even if remote metrics are enabled. See
[receiver voice diagnostics](docs/voice-diagnostics.md) for interpretation and
access details.

### Stream operator diagnostics

To compare a publisher's stream across viewers without logging into a client:

```sh
docker compose exec noxa wget -qO- 'http://127.0.0.1:12337/debug/streams?nickname=Example'
```

This GET-only endpoint accepts direct loopback connections, including when remote
metrics are enabled. It exposes current publications, eligible viewers' watch and
output states, fresh sender reports, server ingress/forwarding counters and each
receiver's video pacing queue, feedback age and local packet-drop counters.
Pacing counters cover the recipient's entire video connection, including retries;
they are not losses for one source. Server frame rates count RTP timestamps, not
successfully decoded frames. No media payloads or network addresses are included.

Use either `nickname` or `publisher_id`, or omit the filter. Snapshots contain at
most 32 publications and 32 eligible viewers per publication. Filters select from
that bounded snapshot; when `truncated` or `viewers_truncated` is true, an absent
row does not establish that a stream or viewer is missing. Viewer-facing
**Stream details** keeps its existing access checks and shows only that viewer's
forwarding path.

---

## 🔒 Security

The implementation includes:

- **Authentication**: Ed25519 identity challenges and Argon2id password verification, carried over the TLS control connection.
- **Strict TOFU Certificate Pinning**: Clients pin self-signed TLS certificates on first connect.
- **PII Storage Protection**: Sensitive user metadata columns are encrypted at rest with AES-256-GCM authenticated data.
- **Delegated Role Management**: Managers can manage only lower roles and cannot grant capabilities they do not hold. Only the owner can grant Administrator.

---

## 📄 License

The project code is licensed under the **MIT License**; see [LICENSE](LICENSE).
Bundled third-party assets retain their own terms. In particular, German spoken
announcements include CC BY 4.0 attribution; see the
[speech asset notes](client/frontend/src/assets/speech/README.md) and bundled
[audio notices](client/frontend/public/noxa-audio-licenses.txt).
