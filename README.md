<div align="center">

<img src="client/frontend/public/branding/logo.png" alt="noXa logo" width="160" height="160" />

# noXa

**Next-Generation High-Performance Real-Time Communication Platform**

*Ultra-low latency SFU voice & video engine, zero-trust E2EE chat messaging, PostgreSQL multi-tenant state persistence, and named roles, role hierarchy, and channel access overrides.*

[![CI](https://github.com/arumes31/noxa/actions/workflows/ci.yml/badge.svg)](https://github.com/arumes31/noxa/actions/workflows/ci.yml)
[![golangci-lint](https://github.com/arumes31/noxa/actions/workflows/golangci-lint.yml/badge.svg)](https://github.com/arumes31/noxa/actions/workflows/golangci-lint.yml)
[![Security Analysis](https://github.com/arumes31/noxa/actions/workflows/security.yml/badge.svg)](https://github.com/arumes31/noxa/actions/workflows/security.yml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/arumes31/noxa)](https://go.dev/)
[![Docker Image](https://img.shields.io/docker/v/arumes31/noxa?label=ghcr.io&logo=docker)](https://github.com/arumes31/noxa/pkgs/container/noxa)
[![License](https://img.shields.io/github/license/arumes31/noxa)](LICENSE)

[Architecture](#️-system-architecture) • [Features](#-key-features) • [Quick Start](#-quick-start) • [Server Setup](#new-server-setup) • [Permissions](#️-roles-and-channel-access) • [ServerQuery API](#-serverquery-admin-protocol) • [Configuration](#️-configuration-reference)

---

<img width="1280" height="800" alt="grafik" src="https://github.com/user-attachments/assets/d3ed3136-b468-476c-8e99-2de600d3c0f5" />


</div>

## 🌟 Overview

**noXa** is an enterprise-grade, self-hosted real-time communication platform written in Go. Designed for high concurrency and operational clarity, noXa couples a lightweight binary control protocol with a **Pion WebRTC SFU engine** for sub-100ms multi-party audio/video fan-out, end-to-end encrypted messaging, and granular administrative control.

> [!NOTE]
> **Zero-Trust Security**: Direct messages are fully E2EE using X25519 Double-Ratchet key agreements. The server stores only channel history under persisted scope keys; direct message bodies never hit the server database in plaintext or unwrapped ciphertext.

---

## 🏗️ System Architecture

```mermaid
graph TD
    subgraph Clients["Clients"]
        Wails["Wails Desktop Application\n(Windows / Linux / macOS)"]
        WebUI["Web Browser Client\n(HTML5 / WebRTC / ES6)"]
        Bot["ServerQuery Bot / CLI\n(TCP Telnet / SSH)"]
    end

    subgraph CoreServer["noXa Server Core"]
        Control["TCP Control Listener\n:12333 (TLS / TOFU)"]
        Keepalive["UDP Keepalive Worker Pool\n:12334"]
        WebRTC["Pion WebRTC SFU Engine\n(DTLS-SRTP / ICE / Opus)"]
        Query["ServerQuery Admin Protocol\n127.0.0.1:12335 (Raw) / :12339 (SSH opt-in)"]
        FileXfer["File Transfer Service\n:12336 (TLS 1.3 / Token Authorized)"]
        Health["Health & Metrics Service\n:12337 (/healthz, /readyz)"]
    end

    subgraph DataStore["Persistence & Messaging"]
        Postgres[(PostgreSQL 16\nStore, State, Audit Logs)]
        Redis[(Redis 7\nPub/Sub & Rate Limiting)]
    end

    Wails <-->|TLS Control JSON| Control
    Wails <-->|UDP WebRTC Media| WebRTC
    WebUI <-->|WebSockets / WebRTC| WebRTC
    Bot <-->|TCP Text Commands| Query
    Wails <-->|Token Upload/Download| FileXfer

    Control --> Postgres
    Control --> Redis
    Query --> Postgres
    WebRTC --> Control
```

### Protocol Specifications

| Protocol Port | Service Component | Wire Format / Transport | Security & Auth |
| :--- | :--- | :--- | :--- |
| **`TCP :12333`** | Control Engine | Length-prefixed JSON frames over TLS 1.3 | Ed25519 Challenge / Argon2id / TOFU Pinning |
| **`UDP :12334`** | Connection Probes | Datagram Ping/Pong Keepalive | Session Token Verification |
| **`UDP Dynamic`** | WebRTC SFU Engine | DTLS-SRTP (Opus audio, H.264/VP8 video) | ICE candidate negotiation & SRTP encryption |
| **`TCP 127.0.0.1:12335`** | ServerQuery Protocol | Line-based ASCII / UTF-8 plaintext stream | Loopback by default; remote binding requires explicit opt-in, and SSH is preferred |
| **`TCP :12336`** | File Transfer Engine | Binary frames over TLS 1.3 | TOFU-pinned certificate plus an ephemeral single-use token |
| **`TCP :12337`** | Health & Prometheus | HTTP GET (`/healthz`, `/readyz`, `/metrics`) | Liveness/readiness follow the listener bind; metrics are loopback-only unless explicitly enabled; pprof is disabled by default and loopback-only |

---

## ✨ Key Features

### 🎙️ Sub-100ms Voice & Video SFU
* **Pion WebRTC SFU**: Zero-copy packet fan-out supporting hundreds of concurrent speakers.
* **Opus Codec Optimization**: Dynamic SDP fmtp line rewriting per channel for variable bitrate (16–128 kbps), Forward Error Correction (FEC), and Discontinuous Transmission (DTX).
* **Simulcast Video**: Dynamic quality tier selection (`high`, `mid`, `low` RID layers) based on subscriber network conditions.
* **Screen-share audio choices**: In channel shares and private calls, choose **No audio**, **Shared application**, or **System audio**. Application audio requires a window and a supported capture runtime; enable audio in the system picker. It can include other windows of the same application. If application-only capture is unavailable or cannot be confirmed, video still starts without audio and a short notification explains the fallback. System audio is only shared when explicitly selected.
* **Screen-share resolution**: The channel share dialog offers 720p, 1080p, 1440p, 4K, **Original source resolution**, and **Custom** dimensions (160–8192 pixels, 15/30/60 fps). The encoder has 50 Mbps of headroom instead of Chromium's implicit ~2.5 Mbps default; this is a ceiling, not a target or minimum. Normal presets preserve resolution, while 60 fps presets balance resolution and frame rate. WebRTC adapts to actual network and encoder capacity without an additional system-CPU-triggered 500 kbps cap. Server limits and explicit Low bandwidth mode still apply.
* **Priority Commander**: Automatic audio ducking (−12 dB attenuation) across non-priority channels when a Priority Speaker talks.
* **Whisper Routing**: Point-to-point and cross-channel targeted voice transmission bypasses standard channel boundaries.
* **Microphone recovery**: If the selected microphone disconnects, receiving audio and video continues. Choose and apply a device in **Capture** settings, then select **Retry microphone**. A replacement microphone never starts automatically.
* **Network echo test**: In **Capture** settings, explicitly join the server's echo channel to hear your microphone through the normal voice connection. Only you hear your microphone: other participants cannot hear you and you cannot hear them. Echo media is excluded from whispers, cross-participant video, and channel recordings. Mute and push-to-talk still apply; wear headphones. The return button restores your previous channel while the test remains active on that server tab. Each server automatically creates `Echo Test` on startup after role setup, granting admitted users permission to view, join and speak only in that channel. Existing channels with the configured name retain their access rules, custom metadata and history; startup updates only the known old system-created echo topic. Set Docker environment variable `NOXA_ECHO_CHANNEL_ENABLED=false` and recreate the server container to disable creation and loopback; existing channels and history are retained. Configure the name with `NOXA_ECHO_CHANNEL_NAME` or `echo_channel_name` in YAML.

### 💬 End-to-End Encrypted & Scope-Keyed Messaging
* **True E2EE Direct Messaging**: Signal-style X25519 prekey bundles with Double-Ratchet forward secrecy.
* **Channel Scope Key Rotation**: Channel message bodies are sealed with scope keys; server stores ciphertext and manages scope key generations.
* **Rich Messaging Controls**: Channel history search, pinned messages, emoji reactions, typing indicators, read receipts, and `@mention` notifications.
* **Automated Moderation**: Regex link whitelisting/blacklisting, duplicate message suppression, rate limiting, and word filtering.

### 🛡️ Roles and Channel Access

* **Named roles**: `@everyone` supplies the baseline; a member's role grants combine. Role order controls delegated management, moderation targets, and appearance.
* **Channel access**: Channels either sync with their parent or use custom overrides. Overrides use Inherit / Allow / Deny for roles and individual registered members.
* **Predictable resolution**: Start with server role grants, apply the channel's `@everyone` overrides, combine role overrides (allow wins at this stage), then apply member-specific overrides. Capability prerequisites and moderation restrictions still apply.
* **Protected ownership**: Owner and Administrator bypass configurable channel overrides, but not account bans, admission checks, ownership protection, hierarchy checks, or operational limits. Only the owner can grant Administrator or transfer ownership.
* **Explainable changes**: Check access shows the effective decision and its source. Revision checks prevent stale saves; role/access mutations are audited.

The current server and client require the coordinated `roles-v1` model and a fresh database. Existing legacy databases are not automatically migrated. See [fresh setup](docs/role-setup-preflight.md) and [the authorization contract](docs/capability-enforcement.md).

---

## ⚡ Quick Start

> [!TIP]
> The fastest way to run noXa is using **Docker Compose**.

### Option 1: Docker Compose (Recommended)

1. Clone the repository:
   ```bash
   git clone https://github.com/arumes31/noxa.git
   cd noxa
   ```

2. Create an explicit local-development environment, build the server image,
   and start its database and Redis dependencies:
   ```bash
   cp .env.example .env
   docker compose build noxa
   docker compose up -d postgres redis
   ```

   The sample environment is for host-local development. Before exposing a
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

3. Follow [New server setup](#new-server-setup) below to create the owner,
   activate roles, and start the server. Admin privilege tokens have been retired.

### Option 2: Building from Source

Successful `main` builds publish signed stable releases, starting with `v0.4.3`,
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
* **Go**: `>= 1.27.1` (both Go modules declare this minimum)
* **Node.js**: `>= 24`
* **Wails CLI**: `go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0` (match `client/go.mod`)
* **PostgreSQL**: `>= 16`

#### Build Backend Server
```bash
# Build the standalone server with automatic Git version metadata
make build

# Inspect the exact version embedded by this source state
make version

# After creating the owner and activating roles (see New server setup below),
# start a local-development server. Use your configured database URL.
NOXA_DEV_MODE=true NOXA_DATABASE_URL="postgres://noxa:noxa@localhost:5432/noxa?sslmode=disable" ./bin/noxa-server
```

On Windows PowerShell, use the equivalent native wrapper:

```powershell
./scripts/build.ps1 version
./scripts/build.ps1 server
```

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
[configuration reference](#️-configuration-reference) for listener settings.

Back up PostgreSQL together with the matching chat/PII keys, uploaded files,
recordings, TLS identity and configuration. Verify a restore before relying on
the installation; see [backup and restore](docs/operations/backup-restore.md).

---

## ⚙️ Configuration Reference

noXa can be configured via environment variables or a YAML configuration file (`config.yaml`).

| Environment Variable | Default Value | Description |
| :--- | :--- | :--- |
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
| `NOXA_DEFAULT_GROUPS_ENABLED` | `true` | Auto-create and assign the built-in Guest and Member groups |
| `NOXA_TURN_CREDENTIALS_TTL` | `24h` | TURN credential lifetime; must be positive and at most 30 days |
| `NOXA_REDIS_DIAL_TIMEOUT` / `READ_TIMEOUT` / `WRITE_TIMEOUT` | `5s` / `3s` / `3s` | Redis client timeouts when Redis is enabled |
| `NOXA_REDIS_TLS_ENABLED` | `false` | Enable verified Redis TLS (TLS 1.2+); optional server name and CA file use `NOXA_REDIS_TLS_SERVER_NAME` / `NOXA_REDIS_TLS_CA_FILE` |

`NOXA_CHAT_MASTER_KEY` takes precedence over `NOXA_CHAT_MASTER_KEY_FILE`.
Use secret injection for the override; it accepts either a single base64 32-byte
key or a newline-separated `id:base64` key ring for key rotation.

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

## 📂 Project Structure

```
noxa/
├── client/                     # Desktop Client (Wails v2 / Go + ES6 UI)
│   ├── desktop_windows.go      # Windows COM thread affinity & tray setup
│   ├── frontend/               # Single-page UI (Vite / ES6 / Modular CSS)
│   ├── hotkeys.go              # Global hotkey registration engine
│   └── ptt_windows.go          # Win32 Virtual Key low-level PTT observer
├── cmd/
│   ├── server/                 # Standalone noXa Server entrypoint
│   └── migrate/                # Standalone DB migration utility
├── internal/
│   ├── auth/                   # Ed25519 challenge & Argon2id authentication
│   ├── broadcast/              # Outbound message fanout & snapshot engine
│   ├── channels/               # Active channel tree manager
│   ├── e2ee/                   # Signal-style X25519 Double-Ratchet crypto
│   ├── filetransfer/           # Token-authenticated file pipeline
│   ├── netproto/               # Binary frame codec & JSON message definitions
│   ├── authorization/          # Named roles and channel access evaluation
│   ├── query/                  # ServerQuery line-based admin protocol
│   ├── store/                  # PostgreSQL data access layer & migrations
│   └── webrtc/                 # Pion WebRTC SFU engine & Opus mixer
├── .github/
│   └── workflows/              # GitHub Actions (CI, Security, Docker, Lint)
├── Dockerfile                  # Multi-stage production container image
├── docker-compose.yml          # Production Docker stack
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

---

## 🔒 Security & Vulnerability Reporting

noXa is engineered around a strict security posture:
- **Challenge Authentication**: Public key cryptography prevents password sniffing over untrusted networks.
- **Strict TOFU Certificate Pinning**: Clients pin self-signed TLS certificates on first connect.
- **PII Storage Protection**: Sensitive user metadata columns are encrypted at rest with AES-256-GCM authenticated data.
- **Delegated Role Management**: Managers can manage only lower roles and cannot grant capabilities they do not hold. Only the owner can grant Administrator.

---

## 📄 License

This project is licensed under the **MIT License**. See the [LICENSE](LICENSE) file for complete details.
