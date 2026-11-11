<div align="center">

<img src="client/frontend/public/branding/logo.png" alt="noXa logo" width="160" height="160" />

# noXa

**Self-hosted voice, video, screen sharing, and chat**

*Go server, Windows desktop client, installable mobile/web PWA, and PostgreSQL persistence.*

[![CI](https://github.com/arumes31/noxa/actions/workflows/ci.yml/badge.svg)](https://github.com/arumes31/noxa/actions/workflows/ci.yml)
[![golangci-lint](https://github.com/arumes31/noxa/actions/workflows/golangci-lint.yml/badge.svg)](https://github.com/arumes31/noxa/actions/workflows/golangci-lint.yml)
[![Security Analysis](https://github.com/arumes31/noxa/actions/workflows/security.yml/badge.svg)](https://github.com/arumes31/noxa/actions/workflows/security.yml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/arumes31/noxa)](https://go.dev/)
[![Container Image](https://img.shields.io/badge/container-ghcr.io-blue?logo=docker)](https://github.com/arumes31/noxa/pkgs/container/noxa)
[![License](https://img.shields.io/github/license/arumes31/noxa)](LICENSE)

[Public test server](#public-test-server) • [Mobile / PWA](#mobile) • [Architecture](#architecture) • [Features](#features) • [Quick Start](#quick-start) • [Server Setup](#new-server-setup) • [Permissions](#permissions) • [ServerQuery API](#serverquery) • [Configuration](#configuration) • [Development](#development-and-checks) • [Operations](#operations)

---

<img width="1280" height="800" alt="Current noXa desktop client in OLED Midnight with fictional demo conversations" src="website/public/assets/screenshots/app-chat.png" />

*Actual client UI with fictional demo data; [capture details](website/README.md#product-screenshot-provenance).*


</div>

## 🌟 Overview

**noXa** combines voice channels, camera and screen sharing, direct and group
conversations, and channel forums in a desktop client and an installable web app. Its Go server uses a
framed control protocol, a **Pion WebRTC SFU**, PostgreSQL for persistent state,
and optional Redis pub/sub. Named roles control access and moderation.

The published desktop binary targets **Windows x64**. The Wails source also has
Linux/macOS platform code; CI exercises Windows and Linux, but does not publish
Linux/macOS desktop releases. The Vite frontend uses the native Go bridge: it is
not a standalone browser client. [`website/`](website/README.md) is a separate
static product/download website hosted at [noxa.wowcraft.pw](https://noxa.wowcraft.pw/).

<a id="public-test-server"></a>

### Try the public test server

| Open | Address |
| :--- | :--- |
| Website and downloads | [noxa.wowcraft.pw](https://noxa.wowcraft.pw/) |
| Mobile / browser client | [Launch the PWA](https://noxa.wowcraft.pw/app/) |
| Desktop server address (Server IP / hostname) | `wowcraft.pw` (default control port `12333`) |
| Windows x64 client | [Download for Windows](https://github.com/arumes31/noxa/releases/latest/download/noxa-client-windows-amd64.exe) |

This is a public **test server** for trying noXa. The Windows download redirects
to the current GitHub Release. GitHub Pages is no longer used.

Link your account through **Connections → PWA / Mobile login** in the desktop
client, or continue as a guest when the server permits it. The dialog includes a
QR code, a PWA link, and installation guidance. On Android, install from Chrome;
on iPhone/iPad, open in Safari and add to the Home Screen; on macOS, use Safari's
Add to Dock where available or Chrome's install option. Browser features and
system permissions determine the available voice, camera and notification tools.

<details>
<summary>More current interface screenshots</summary>

![Role management with fictional demo roles](website/public/assets/screenshots/app-roles.png)

<img src="website/public/assets/screenshots/pwa-chat.png" width="390" alt="Mobile PWA chat with fictional demo messages" />

</details>

> [!NOTE]
> **Encryption boundaries** (details in [Encryption](#encryption)): direct
> messages and private groups are end-to-end encrypted with Olm (vodozemac),
> and channel voice, video and screen sharing are end-to-end encrypted with a
> key shared by the channel's members. Private calls (direct and group) are
> end-to-end encrypted with per-call keys sent over Olm. Channel/global chat
> and forum bodies are **not** end-to-end encrypted yet. Upgrading? Deploy the
> server before or together with the clients: see
> [Upgrading to end-to-end encryption](#e2ee-upgrade).

<a id="encryption"></a>

### Encryption

**What is end-to-end encrypted.** Direct messages and private-group messages,
with their attachments, edits and deletes, between desktops and the mobile web
app:

- **Protocol.** Olm, the double ratchet as implemented by
  [vodozemac](https://github.com/matrix-org/vodozemac), compiled to
  WebAssembly and used by both clients (`noxa-olm-v1`). There is one Olm
  session per pair of devices; every device of the sender and of the
  recipients receives its own copy, and the conversation, sender, recipient
  device and message ID are authenticated inside every message. Keys ratchet
  forward with every message, so a session key stolen later cannot open
  earlier messages (copied history is the exception, see "New devices").
- **Primitives.** X25519 for the Olm handshake, AES-256-CBC with
  HMAC-SHA-256 for messages. The Olm MAC is **truncated to 64 bits**, as the
  Olm specification defines. Olm accounts are published as bundles signed by
  each device's Ed25519 noXa key; contacts verify devices against the
  identity they pinned (the account's first desktop and the devices it
  approved). Changed identities need explicit confirmation; account resets
  are accepted only through a signed reset chain.
- **Not claimed.** This is not the Signal protocol and is not wire-compatible
  with Signal or Matrix; it is not post-quantum.
- **Server role.** The server relays and stores ciphertext and enforces
  admission (complete device fanout, limits, membership) without being able
  to read messages. It still sees metadata: who talks to whom, when, message
  sizes and device lists.
- **Older history and older apps.** Direct messages from before this protocol
  used static X25519 + XSalsa20-Poly1305 (NaCl box) keys without forward
  secrecy. Updated apps never send that way. Older apps still can, and
  updated apps still show such messages, labelled as legacy encryption
  (readable, not editable). An updated app never falls back to the weaker
  encryption: if a contact has no end-to-end key yet, it does not send and
  says that the contact needs to update.
- **New devices.** Copying history to a newly linked device transfers
  readable records only (signed per transfer by the source device, never keys
  or sessions); copied records are marked as verified on the other device,
  not on the new one. The copy is sealed with the two devices' long-term
  X25519 keys (NaCl box) and kept by the server for up to 24 hours, so it has
  no forward secrecy: someone who stores it and later obtains one of those
  long-term keys can read the copied history.
- **Private-call signalling** still uses the static device keys; private-call
  media itself is end-to-end encrypted with per-call keys (see below).
- **Guests.** A guest that signs in with an identity key (desktop password-less
  logins, browser guests) uses the same protocol for direct messages: its key
  is its own account root and registers its device itself. A browser guest has
  no desktop to vouch for the server, so it pins the server's certificate on
  first use; if the certificate changes, secure messaging pauses until the
  guest explicitly reviews and accepts the new fingerprint. Sessions without an
  identity key cannot send direct or group messages.

**Channel voice, video and screen sharing (`noxa-media-e2ee-v1`).** On a
server that advertises this capability, every channel microphone, camera,
screen and screen-audio frame is encrypted on the sending device before it
reaches the server's media relay (SFU), and decrypted only on the receiving
device. The frame cipher is the maintained LiveKit FrameCryptor (AES-GCM,
pinned `livekit-client` 2.23.0) run in a dedicated worker on both browser
transform APIs (RTCRtpScriptTransform and the older encoded-streams API).
Each publication slot gets a fresh random key per epoch; keys travel only as
authenticated, expiring, single-device ratchet controls (the same
`noxa-olm-v1` sessions as direct messages, never stored at rest and never
shown as messages) to the devices the channel roster names. A new epoch starts
on every join, leave, kick, device change, reconnect and publication change
and every five minutes, so a newcomer cannot decrypt earlier media and a
departed device cannot decrypt later media. There is no unencrypted fallback:
an old client, a keyless guest session, a browser without encoded transforms
or a server without the device directory gets an explicit message and no
media. While media is encrypted the SFU negotiates Opus, VP8 and H.264
Baseline (profile 42001f); H.264 is used only by publishers that declare the
encrypted H.264 frame format (the desktop does, the mobile app publishes VP8),
and VP9, AV1 and HEVC are off. The server cannot start recordings (an existing
recording can still be stopped and keeps its files and retention), whisper is
unavailable, stream previews are not uploaded (a stream shows its picture once
watched), and the connection benchmark is unavailable. The echo test channel
returns your own encrypted media, decrypted with your own key.
Limits: the server still sees who is in a channel, packet sizes and timing,
the clear VP8 keyframe header (dimensions) and, for H.264, every NAL unit
before the first slice (SPS, PPS and SEI: profile, level, dimensions and any
encoder metadata); every participant who receives
the keys can record what they receive (the app has no call recorder, external
recorders cannot be prevented); a key is shared by everyone in the channel, so frames prove channel
membership, not which member sent them: with the server's help, any
participant could forge another participant's audio or video. Keys go only to
the members the voice room shows (the status line names who holds them and
warns when another member's view of the channel differs). Encrypted voice is
unavailable while you are invisible (invisible sessions are not in the other
members' roster). An encrypted voice channel holds at most 64 participants;
a join or move beyond that is refused ("this encrypted voice channel is full").
Like the channel member limit, the count includes invisible members, so a full
channel can hint that an invisible member is present. A second live session of
the same device counts as its own participant.

**What stays off while channel media is encrypted, and why.**
* *Whisper* stays unavailable. A whisper would need its own key scope for an
  authorized recipient set that is not the channel, verified like channel keys;
  until that scope is built and proven, whisper is refused rather than sent
  under the channel key or in plaintext.
* *Stream previews (thumbnails)* are made locally on each device that watches
  a stream; nothing is uploaded, so the server never holds a picture.
* *Video codecs* are VP8 and H.264 Baseline (Opus for audio).
  H.264 keeps its slice headers in the clear so the SFU can packetize and
  admit keyframes; HEVC, VP9 and AV1 are not offered.
* *The connection benchmark* stays declined: it would send unencrypted test
  media through the SFU.
* *Key controls already handed to the secure-messaging transport* are not
  cancelled when a recipient is removed. Once the server accepted a control it
  owns the delivery, so a retraction could never be enforced against the
  server, and an honest server stops routing the channel's media to a removed
  member anyway. Exposure is bounded instead. When a sender's roster drops a
  device (leave, kick, move or device revocation), the sender encrypts only
  under an epoch that device never received no later than 2.2 seconds
  (200 ms to coalesce roster changes plus a 2-second activation deadline) after
  the roster update reached it. This holds however long the server delays key
  deliveries, while other rekeys (joins, publication changes, the five-minute
  renewal) are running, under repeated removals or a stream of other roster
  changes, and if the wall clock is changed (the deadline is a timer started
  at the removal); a removed device never receives an epoch created after its
  removal. If new keys cannot be set up in time (for example, the encryption
  worker keeps failing), the sender's audio and video are paused at the
  deadline instead of continuing under the old keys, and the voice status
  says so until new keys are ready. The bound starts when the
  roster update arrives: the server decides when it tells clients that
  someone left. Remaining members whose new key is still in transit get it
  when it arrives and cannot hear the sender until then (the voice status
  says so). A late control is refused by the recipient after its
  two-minute expiry, and an older epoch can never be reinstalled.

Receivers get their encryption transform before the answer that adds them is
sent, so the SFU can never route a frame to a receiver without one; a receiver
that cannot be protected aborts the negotiation. Video receivers on the older
encoded-streams path ask the SFU for a keyframe after their first decrypted
frame. Voice and video tiles show the account whose verified device delivered
the keys for that participant.

<a id="e2ee-upgrade"></a>

**Upgrading to end-to-end encryption.**

- **Deploy the server first (or together with the clients).** Updated
  desktops and mobile web apps sign in only with the domain-separated login
  challenge that updated servers offer. Against an older server they refuse
  to sign in with "this server needs an update" and do not fall back. Older
  clients can still sign in to an updated server, but they cannot join voice
  channels (they are asked to update), cannot use end-to-end encrypted direct
  or group messages, and can send legacy direct messages only to contacts
  with a single desktop device.
- **Private calls need current apps on both ends.** Call media is end-to-end
  encrypted; a call between an updated and an older app ends with "needs to
  update noXa" instead of carrying unencrypted media. No server change is
  needed for this.
- **Secure messaging and encrypted channel voice are always on.** There is no
  setting to turn them off. They need the server's database (the signed device
  directory and admission live there) and control-channel TLS (keys are bound
  to the server's certificate fingerprint). A server without either refuses
  direct and group messages and channel voice with an explicit message, and
  logs a startup warning. **A server with `tls_enabled: false` cannot offer
  channel voice at all.**
- **Server recording cannot be started** while channel media is encrypted;
  an existing recording can still be stopped and keeps its files and
  retention. Whisper, uploaded stream previews, HEVC publication and the
  connection benchmark are unavailable (see above).
- Plan certificate rotations as a secure-messaging change too (see
  [Certificate trust and rotation](#certificate-rotation)).

**What is not end-to-end encrypted (yet).** Channel and global chat, threads
and forum bodies use server-managed scope keys: the server can decrypt them
(it moderates at send time and stores history sealed).

**Private calls.** Direct and group calls outside channels are end-to-end
encrypted with the same frame cipher as channel media (pinned LiveKit
FrameCryptor, AES-GCM, VP8/H.264 video and Opus audio). Each participant
device creates its own key epochs for the call and sends them as
authenticated Olm controls to the devices that answered for the other
accepted participants, and only to them: someone who leaves never receives a
later epoch and someone who joins never an earlier one. The TURN relay and
the server forward only ciphertext; the desktop additionally refuses to send a
call key to any device the server's call state does not show as an accepted
participant. Both apps must be current: a participant whose app does not
announce call encryption ends the call with an "update required" message
instead of falling back to unencrypted media. The call panel shows
"End-to-end encrypted" once keys are exchanged; in one-to-one calls it links
to the contact's safety number.

**Endpoint limits.** Encryption protects messages in transit and on the
server, not on a compromised device. Readable history is stored on each device
(desktop: in a protected local store tied to the identity, destroyed when the
identity is deleted; mobile web app: in IndexedDB under a non-extractable
WebCrypto key, deleted on sign-out). Chat exports are plain text unless you
choose a passphrase; anything you copy, export or screenshot leaves this
protection.

---

<a id="architecture"></a>

## 🏗️ System Architecture

```mermaid
graph TD
    subgraph Clients["Clients"]
        Wails["Wails Desktop Client\nGo bridge + webview UI / WebRTC"]
        PWA["Mobile / Browser PWA\nHTTPS + WebSocket / WebRTC"]
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

    PWA <-->|HTTPS / WebSocket| Web["Web Listener :12342\n/app/, /ws, /ft, /api/v1/web/info"]
    Web --> Control
    PWA <-->|DTLS-SRTP| WebRTC
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
| **`TCP :12337`** | HTTP services | Health/readiness, Prometheus, `/debug/voice`, `/debug/streams`, `/events`, `/hooks/`, `/dl/` | Media diagnostics stay loopback-only; metrics require explicit remote opt-in; pprof is opt-in and loopback-only. Remote authenticated HTTP routes need `NOXA_HEALTH_TLS_ENABLED=true` or an HTTPS proxy; production refuses their credentials over remote plaintext |
| **`TCP 127.0.0.1:12338`** | gRPC administration | Plaintext gRPC | Integration account authentication and current roles; loopback binding is mandatory |
| **`TCP :12339`** | ServerQuery over SSH | SSH-wrapped command stream | Disabled by default; integration credentials and `roles-v1` negotiation |

---

<a id="features"></a>

## ✨ Key Features

### 🎙️ Voice, video, and screen sharing

* **Pion WebRTC SFU**: Routes individual publisher tracks to authorized subscribers, with separate camera, screen, microphone, and shared-audio streams. A new video receiver briefly probes the measured source rate and keeps an increase only when receiver feedback confirms it; congestion and missing feedback end that startup probe.
* **Video packet repair**: The server waits only when a packet sequence has a gap, for up to 2.5 seconds. Ordered packets pass through immediately; a completed repair is released immediately too. Each incoming video track has hard limits of 16 MiB of encoded RTP and 16,384 queued packets (metadata adds overhead); storage pressure can end the wait earlier. The outgoing video queue retains a bounded repair backlog while congestion pacing drains it; audio keeps its separate low-latency queue. Configured relay bitrate ceilings still apply, including their burst limits. This is an on-demand repair window, not a fixed playback delay or a guarantee against freezes and bandwidth adaptation.
* **Opus controls**: Per-channel bitrate, Forward Error Correction (FEC), Discontinuous Transmission (DTX), and stereo settings. The default bitrate is 32 kbps; latency and capacity depend on the network and host.
* **One quality per stream**: Each camera or screen share uploads one encoding using the sender's selected settings. Screen sharing keeps the selected resolution unless the uplink is congested (see Screen-share resolution) while actual FPS can adapt to the sender's resources. All viewers receive the same source; a slower viewer needs the sender to lower the source settings manually. Different shares retain independent sender settings. On updated servers, video and screen-share audio upload pause without viewers or an active server recording; capture and previews remain available. Legacy publishers with multiple layers retain their compatible receive controls.
* **Screen-share audio choices**: In channel shares and private calls, choose **No audio**, **Shared application**, or **System audio**. Application audio requires a window and a supported capture runtime; enable audio in the system picker. It can include other windows of the same application. If application-only capture is unavailable or cannot be confirmed, video still starts without audio and a short notification explains the fallback. System audio is only shared when explicitly selected.
* **Screen-share resolution**: The channel share dialog offers 720p, 1080p, 1440p, 4K, **Original source resolution**, and **Custom** dimensions (160–8192 pixels, 15/30/60 fps). The encoder has 50 Mbps of headroom instead of Chromium's implicit ~2.5 Mbps default; this is a ceiling, not a target or minimum. Screen-share presets keep the selected resolution while actual frame rate can adapt to network and encoder capacity, without an additional system-CPU-triggered 500 kbps cap. If the uplink limits encoding for five seconds, a share above 720p drops straight to 720p (short side); persistent congestion then lowers it gradually to a 540-pixel floor, and it returns to the selected resolution after five healthy seconds. Server limits and explicit Low bandwidth mode still apply.
* **Live share controls**: **Change quality** adjusts a running channel share's resolution and frame rate without reopening the capture picker or changing its source/audio. It updates the one encoding received by every viewer. Watched-stream details show measured resolution, decoded frames per second, codec and payload bitrate separately from the sender's selected settings.
* **Shared video codec**: camera and screen publications use one sender encoding and upload. Channel media is always end-to-end encrypted on this release (see Encryption), so channel publications use VP8 or, from the desktop, encrypted H.264 Baseline; the HEVC selection described next applies only to unencrypted media. On a capable server, HEVC Main 8-bit is preferred when native hardware-support queries succeed and every active viewer supports the publisher's exact level. Otherwise the whole publication uses compatible H.264 Baseline or VP8; the SFU forwards without transcoding. Server recording and explicit server dimension bounds retain VP8 compatibility. Hardware predictions are checked against runtime encoder/decoder statistics, with bounded recovery and codec re-admission after a fallback.
* **Hardware encoder vendor**: Stream details name the GPU vendor (NVIDIA, AMD or Intel) of a hardware encoder, never the device or driver text. WebView2 chooses the encoder, so noXa cannot prefer one vendor; on a multi-GPU PC set **Windows Settings → System → Display → Graphics** to *High performance* for `msedgewebview2.exe` (this also affects other WebView2 apps).
* **Stream diagnostics for viewers**: Any authorized viewer can compare sender capture/encoding/transmission, server ingress/forwarding and local reception/decoding in **Stream details**. A plain-language health summary explains the available evidence and suggests an action for the sender or viewer; live share controls show the sender's summary too. Bitrate, retransmission share, keyframes, frame size and processing delays help locate bottlenecks. Updated senders are required for sender measurements; missing or stale data stays unknown.
* **Priority speaker**: Non-priority publishers in the current voice channel are ducked to 25% gain (about −12 dB) while a priority speaker talks.
* **Whisper Routing**: Point-to-point and cross-channel targeted voice transmission bypasses standard channel boundaries. Unavailable while channel media is end-to-end encrypted (a whisper would leave the channel key scope).
* **Microphone recovery**: If the selected microphone disconnects, receiving audio and video continues. Choose and apply a device in **Capture** settings, then select **Retry microphone**. A replacement microphone never starts automatically.
* **Personal audio controls**: **Personal audio…** in a member's menu opens a compact popover with separate voice and screen-share volume/mute controls, percentage and actual gain in dB. Personal volume uses 100% as unity and reaches +20 dB at the 200% endpoint, with a limiter for loud peaks. Capture settings include push-to-talk, channel voice activation on the audio thread with 100 ms of local pre-roll and a 450 ms release hold, continuous transmission, and local microphone testing. Guided microphone calibration recommends thresholds in the same 0.1% steps as the manual control.
* **Private calls**: Accepted direct/group calls carry voice, camera, and screen sharing without moving participants into a voice channel. Call media is end-to-end encrypted (see [Encryption](#encryption)).
* **Studio (desktop)**: compose a live picture from several sources and publish it to your voice channel as your screen share. See [Studio](#studio).
* **Network echo test**: In **Capture** settings, explicitly join the server's echo channel to hear your microphone through the normal voice connection. Only you hear your microphone: other participants cannot hear you and you cannot hear them. Echo media is excluded from whispers, cross-participant video, and channel recordings. Mute and push-to-talk still apply; wear headphones. The return button restores your previous channel while the test remains active on that server tab. Each server automatically creates `Echo Test` on startup after role setup, granting admitted users permission to view, join and speak only in that channel. Existing channels with the configured name retain their access rules, custom metadata and history; startup updates only the known old system-created echo topic. Set Docker environment variable `NOXA_ECHO_CHANNEL_ENABLED=false` and recreate the server container to disable creation and loopback; existing channels and history are retained. Configure the name with `NOXA_ECHO_CHANNEL_NAME` or `echo_channel_name` in YAML.
* **Connection benchmark**: A separate, explicitly started test in **Capture** sends synthetic Opus packets for 20 seconds through the server's actual WebRTC endpoint and private Echo Test. It uses a temporary guest with the current server's pinned certificate, without microphone/speaker access or moving your existing voice connection. Results include round-trip timing, arrival gaps and unmatched packets after a short drain period; Cancel, tab disconnect and application shutdown clean up the guest. Guest admission and private echo support are required. This measures packet delivery, not perceived audio quality or maximum video throughput. Unavailable while channel media is end-to-end encrypted (it would send unencrypted test media).

### 💬 Messaging and collaboration

* **Encrypted messages and attachments**: Direct messages and private groups are end-to-end encrypted with Olm (see [Encryption](#encryption)); channel/global bodies use server-managed scope-key generations. Attachment keys travel inside their encrypted message bodies.
* **Disappearing direct messages**: either side of a direct conversation can turn on a timer (30 seconds, 5 minutes, 1 hour, 1 day or 1 week; **•••** menu on desktop, conversation menu in the mobile app). The timer travels inside the end-to-end encrypted message, so each updated device enforces it for its own copies: a sent message disappears that long after sending; on a receiving device the timers of all received messages start when the conversation is opened there, including messages further up that were not scrolled into view. Expired text is removed from the local history, search and previews, and redacted in the protected message store so a history replay cannot restore it; the desktop app also clears its Windows notification for the conversation, but notification history kept by the operating system or browser may still show a preview. Only the message text disappears: uploaded attachment files stay on the server, unreadable without the key that was in the expired text. The server never sees the timer and cannot read or enforce it; anyone can still copy or screenshot a message before it disappears. Older apps show the setting change as a normal message and keep messages until they are updated.
* **Message tools**: Emoji reactions, replies, pins, polls, voice messages, typing indicators, read receipts, and mentions. Inbox, history search, and saved messages are scoped to the current server; history search decrypts and matches bodies in the native client.
* **Chat navigation**: Channel and direct-message tabs stay on one row, with an **All chats** menu for overflow. Right-click to pin or reorder, or drag within the pinned/unpinned section. Arrow keys move focus, Enter opens a chat, and Ctrl+Shift+Left/Right reorders it. Order and pins are saved locally, encrypted and separated by server and identity; closing a tab removes its pin. **Recently closed** in All chats and **Ctrl+Shift+T** reopen explicitly closed chats from the current session (up to ten); channel access is checked again. This list resets when the server or identity changes.
* **Chat media**: Enlarging an attached video keeps the same player, playback position, volume, speed and pause state. Animated images and chat videos pause offscreen or after a minute without window focus; only visible media resumes, and manually paused videos stay paused.
* **Attachment previews**: Text and code files (up to ~384 KB, UTF-8 only) show their first 40 lines inline with **Show all**, audio files (MP3, M4A, AAC, Ogg/Opus, WAV, FLAC) play inline, and images and videos preview as before. Previews are decrypted locally and text is never interpreted as markup; the download button stays next to each preview.
* **Composer and attachments**: Selecting text in the message box shows a formatting toolbar (bold, italic, underline, strikethrough, code; also Ctrl+B/I/U) that inserts the usual markdown. Pasted PNG/BMP screenshots are re-encoded as high-quality WebP (quality 0.92) before encryption when that saves at least 10%; GIF, JPEG and WebP are attached unchanged. Attachments have their own right-click menu (save, open full size, copy file name). Deleting your own message offers **Don't ask again** (Settings → Chat restores the question); deleting another member's message as a moderator always asks.
* **Resumable uploads**: The desktop client sends each upload's SHA-256 with its token request. If the connection drops, it asks for a new token up to three times (after 1, 2 and 4 seconds), and an updated server reports how many bytes it already holds, so only the rest is sent; the final digest still covers the whole file. Partials are tied to the uploader, folder, name, size and digest, and an untouched one is removed after 24 hours. They do not count against the user's quota; instead an upload is sent single-shot (without resume) when the user already keeps four partials or when the pending partials plus this upload would exceed their quota. Older servers and the mobile app keep single-shot uploads.
* **Image editor for attachments**: A staged PNG, JPEG, WebP or BMP image has an **Edit** button (✎) that opens an editor before sending: crop (free, 1:1, 4:3, 3:2, 16:9; arrow keys move the crop area, Shift+arrow keys resize it, Enter applies), rotate 90°, **Redact** (an opaque black box for text and numbers that must stay secret), **Pixelate** (cosmetic, e.g. for faces; pixelated text can sometimes be guessed), pen, arrow, rectangle and text in any colour, with undo/redo (Ctrl+Z / Ctrl+Y). The result is a new encoding of the flattened pixels made on your device before encryption: redacted areas cannot be recovered from it, and metadata of the original (EXIF, including GPS) is dropped. Photos stay JPEG at quality 0.92; screenshots stay PNG unless WebP is at least 10% smaller. Images above 50 MP are exported scaled down to 50 MP, and editing never blocks plain sending. Cancel (or Escape, which asks first when something was changed) keeps the staged file as it was. Animated GIF, WebP and PNG (APNG) images are not offered for editing, since re-encoding would keep only one frame.
* **Presence at a glance**: Member avatars in the channel tree carry a status dot whose shape and colour differ (online filled, away crescent, busy with a bar, offline ring); invisible members look offline. A collapsed channel's unread badge includes its subchannels.
* **Code, emoji and pins**: Fenced code blocks show line numbers and a copy button (the copy never includes the numbers); highlight.js (BSD-3-Clause, about 190 languages, bundled locally and loaded on first use) colours blocks whose language it knows, e.g. ```` ```rust ````. The emoji picker searches shortcodes in English and German keywords, keeps recently used emoji and lets you pin favorites (right-click or Alt+click). Pinned messages open as a side drawer; jumping to a pin or a quoted reply that is older than the loaded history opens a window of 100 messages around it in one request. While you read that window, new messages wait behind a **Jump to present** pill; scrolling down pages forward until the window meets the present (servers without window support fall back to paging older history, up to about 1,000 messages back).
* **Long conversations**: once a chat holds 300 or more loaded messages, only the rows near the viewport are rendered (spacers stand in for the rest), so scrolling stays smooth with 100,000 messages. In-chat search filters the loaded messages themselves (author, text and attachment names), jump-to scrolls any loaded message into view, attachment previews and link cards load only for rows you scroll to, and printing renders up to the newest 5,000 rows. While you read scrollback, live messages no longer trim the loaded history; the **Chat max lines** limit applies again at the newest message.
* **Threads and forums**: Persistent posts/replies, tags, following, unread state, archive/reopen, resolved questions, and pinned posts, under the parent channel's access rules.
* **Incoming webhooks**: Revocable, channel-scoped integration tokens; posts remain subject to the creator's current access.
* **Moderation**: Role-controlled message and thread moderation, domain allow/block lists, word filters, duplicate suppression, and rate limits. Server-side content filtering does not inspect E2EE direct-message bodies.

### 🖥️ Desktop experience

* **Connections**: Multiple server tabs, bookmarks, last-successful-connection prefilling, optional OS-protected password storage, and automatic reconnect after connection loss. A failed voice transport restarts ICE at once on the existing peer (keeping its media keys), then backs off to 1, 2, 5 and 15 seconds; a network change or waking the computer from sleep retries immediately and checks every server connection within five seconds instead of waiting for the 45-second read timeout. Login errors explain the next step and reveal the relevant field, distinguishing account credentials, server passwords, connection failures and certificate problems while retaining technical details for support.
* **Speaking and unread indicators**: Animated avatar highlights, alphabetical channel member lists, stream indicators, and unread direct-message badges that keep pulsing until opened.
* **Windows overlay**: Individual active speakers with avatars and animation over the desktop or windowed/borderless games. Configure it in **Settings → Overlay**; local mute/deafen hides it. Exclusive-fullscreen games may cover the overlay.
* **Windows notifications**: On Windows 10/11 the desktop app's native notifications (the matrix column "native") appear in the Action Center with an inline **Reply** box and **Mark as read**. A reply opens the conversation and is sent through its normal composer, so it gets exactly the same protection as typed text (end-to-end encryption in direct messages and private groups, the channel's own encryption in channels); an unsent draft is never replaced (the reply is appended to it). Mark as read clears the local unread count and, for private groups, the read marker, without sending a direct-message read receipt. Each conversation shows at most one toast, which disappears when you read the conversation. Settings → Notifications can switch to the classic tray balloon or hide sender and text ("New message"). Registration is per user (HKCU, AppUserModelID `noXa.Desktop`); no installer step or administrator rights are needed.
* **Notifications**: Per-event controls, volume and previews, with bundled English/German spoken announcements. Notification history links to the originating conversation and message when available, with server and identity checks. Same-server navigation leaves voice membership unchanged; opening another server asks first when switching would interrupt active voice, a call or a share. Events with speech use that recording without a duplicate beep. Playback follows the selected audio output device; no network TTS service is required.
* **Diagnostics**: A playback-health badge evaluates recent voice buffering, concealment and loss separately from ping. Client Info exposes visible members' ping and reported client version; owners and administrators can inspect recent voice history and correlated sender/SFU/receiver measurements. See [Operations](#operations).

<a id="studio"></a>

### 🎬 Studio (desktop)

Studio composes several sources into one live picture and publishes it to
your voice channel through the normal screen-share path, so it is end-to-end
encrypted like every channel stream (VP8). It is part of the desktop app
only.

* **Open it** from the voice bar (**More → Studio**) while connected to a
  voice channel. Opening Studio captures nothing.
* **Sources**: screen or window, camera (video only), image (PNG or JPEG, at
  most 8 MB and 4096×4096) and text (up to 280 characters, system fonts), or
  reuse a capture that is already running. Up to 8 scenes with up to 8
  sources each; position, size, crop and layer order per source.
* **Preview and program**: you edit the draft in **Preview**. **Take**
  (Ctrl+Enter) puts the selected scene on **Program** with a cut or a
  crossfade (100–1500 ms); nothing reaches the program any other way. Keys
  1–8 select a scene, Alt+↑/↓ moves the selected source.
* **Go live** publishes the program (1280×720 at 30 fps, reduced to the
  server's video limits); **Stop** ends it. Closing the panel while live keeps
  the program on air. Studio cannot go live while another screen share is
  running.
* **GPU compositing**: on a hardware GPU the program is composited with
  WebGL2 (on an Intel Iris Xe: about 0.5 ms of main-thread time per frame
  instead of 4 ms with the 2D canvas; the delivered frame rate is the same).
  Software WebGL (remote desktops, machines without GPU acceleration) is far
  slower there, so Studio keeps the 2D canvas on it, and it also switches to
  2D if GPU frames stay slow or the graphics context is lost. The published
  track never changes when it switches.
* **Audio**: only screen-capture audio of sources that are visible on the
  program, with audio enabled, is sent, and the routing changes only with
  **Take**. The microphone is never mixed in.
* **Stored on this device**: the layout (in local storage) and up to 16
  images / 32 MB (in IndexedDB); unused images are removed. Captures are never
  restored, so each screen or camera source must be captured again after a
  restart. Switching channel, server or account ends Studio.

<a id="permissions"></a>

### 🛡️ Roles and Channel Access

* **Named roles**: `@everyone` supplies the baseline; a member's role grants combine. Role order controls delegated management, moderation targets, and appearance.
* **Channel access**: Channels either sync with their parent or use custom overrides. Overrides use Inherit / Allow / Deny for roles and individual registered members.
* **Predictable resolution**: Start with server role grants, apply the channel's `@everyone` overrides, combine role overrides (allow wins at this stage), then apply member-specific overrides. Capability prerequisites and moderation restrictions still apply.
* **Protected ownership**: Owner and Administrator bypass configurable channel overrides, but not account bans, admission checks, ownership protection, hierarchy checks, or operational limits. Only the owner can grant Administrator or transfer ownership.
* **Explainable changes**: Check access shows the effective decision and its source. Revision checks prevent stale saves; role/access mutations are audited.

The current server and client require the coordinated `roles-v1` model and a fresh database. Existing legacy databases are not automatically migrated. Follow [New server setup](#new-server-setup) for a new installation.

---

<a id="quick-start"></a>

## ⚡ Quick Start

For the desktop client, download `noxa-client-windows-amd64.exe` from
[GitHub Releases](https://github.com/arumes31/noxa/releases/latest).
Releases also include `noxa-server-linux-amd64`, checksums, and a signed manifest.
Container builds publish `linux/amd64` and `linux/arm64` images to
[`ghcr.io/arumes31/noxa`](https://github.com/arumes31/noxa/pkgs/container/noxa).
The desktop updater verifies the signed manifest using its embedded trusted keys.

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
   credential, and set `POSTGRES_SSLMODE` to `verify-full` (or supply `NOXA_COMPOSE_DATABASE_URL` with that sslmode).
   Each Compose secret also supports an `_FILE` counterpart; configure exactly
   one non-empty source. Set an `_FILE` value to a readable host path; Compose
   mounts it read-only at `/run/secrets/...`. Prefer a path outside the
   repository—`docker/empty-secret` is only the checked-in empty fallback.
   On Linux, keep the source directory root-owned `0700` and each source file
   root-owned `0444`; Compose mounts individual files, never the directory.
   This permits the non-root service reader without exposing host traversal.
   Production startup rejects the sample credential and plaintext database
   transport. It also requires `NOXA_SERVER_PASSWORD` (or an explicit
   `NOXA_ALLOW_ANONYMOUS=true` for an open server) and a `REDIS_PASSWORD`
   for the bundled Redis.

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

* **Go**: `>= 1.27.3` for the root and client modules; the separately tested WebView2 fork declares its own version.
* **Node.js**: `>= 24`
* **Wails CLI**: `go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0` (match `client/go.mod`)
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
dirty trees receive deterministic commit/content metadata automatically.
Plain `go build` and `wails build`
also use Go's embedded VCS information, while the Make targets additionally
stamp the exact dirty-tree fingerprint into the binary.

The client build writes to `client/build/bin/`. Custom builds that need the
signed updater must embed trusted public keys using `NOXA_UPDATE_PUBLIC_KEYS`
for Make or `-UpdatePublicKeys` for the PowerShell wrapper. The private signing key is never part
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
docker compose --profile tools run --rm --no-deps noxa-operator /out/migrate
read -r -s -p 'Choose an owner account password: ' OWNER_PASSWORD; printf '\n'
printf '%s\n' "$OWNER_PASSWORD" | docker compose --profile tools run -T --rm --no-deps noxa-operator /out/adduser \
  -nickname owner -password-stdin
unset OWNER_PASSWORD
```

Store the password securely and copy the exact `unique_id` printed by `adduser`.
The password is passed through standard input, so it is absent from command-line
arguments and shell history.
`adduser` initializes the fresh schema and creates an account; it does not grant
administrative rights by itself. If it reports that the account already exists,
it has not changed that account's password.

Replace the placeholder below with that exact ID, not the nickname:

```bash
docker compose --profile tools run --rm --no-deps noxa-operator /out/role-setup \
  -owner-uid '<unique_id from adduser>' \
  -activate -confirm ACTIVATE-ROLES-V1 \
  -chat-master-key-file /data/keys/chat_master.key \
  -query-timeout 30s
```

Before starting, complete [Runtime database privilege separation](#runtime-database-privilege-separation)
to provision `noxa_runtime` and `noxa_backup`, and configure their distinct password
files in `.env`. This is also required for the development Compose stack.

```bash
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
Use `role-setup -h` to inspect the setup and activation options.

### 2. Connect as owner and grant administrators

Open the desktop client, connect to `localhost:12333` (or your server's hostname),
and enter `owner` as the **Account login / guest name** and the chosen **Account password**. A server
join password, if configured, is separate from the account password. Compare the
server certificate fingerprint with its startup log before trusting it.

To appear as `Daniel`, enter it in the optional **Display name** field. Everyone
on the server sees that name; the `owner` login, identity, and roles stay the same.
Use **Settings → Change display name** to change it during a session. The login form
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

Open **Tools → Roles**, then **Members**. Find a registered member,
select the **Administrator** role and choose **Add role** to grant admin access.
Only the owner can grant Administrator or transfer ownership. Keep this role
limited to trusted operators; use Moderator for routine moderation.

In **Tools → Roles**, the owner can enable **Protect from deletion** for
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

1. In **Tools → Roles**, select an existing role or **Create role**. Use
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
The bundled coturn shares only the dedicated `noxa-turn-net` network with the
noxa container, disables TCP relays and refuses to relay to loopback, private,
CGNAT, link-local and ULA addresses except noxa's fixed address on that network
(`TURN_NOXA_IP` in `TURN_NETWORK_SUBNET`), so TURN credentials cannot reach
PostgreSQL, Redis or other internal services. If clients relay to another
private noxa media address (LAN-only deployments), allow exactly that address
with `TURN_ALLOWED_PEER_IP`.

Back up PostgreSQL together with the matching chat/PII keys, uploaded files,
recordings, TLS identity and configuration. Verify a restore before relying on
the installation.

---

<a id="configuration"></a>

## ⚙️ Configuration Reference

`NOXA_*` environment variables override `config.yaml`, which is searched first
in the working directory and then in `/etc/noxa`; missing values use built-in
defaults. Nested YAML keys use underscores in environment variables, for example
`webrtc.udp_addr` becomes `NOXA_WEBRTC_UDP_ADDR`. The server does not load `.env`
itself; Docker Compose uses it for interpolation.

Set a required native-client minimum in YAML, for example
`client_min_version: "0.6.20"`, or through `NOXA_CLIENT_MIN_VERSION`; it takes
effect after server restart. The checked-in default is `0.0.0`.
`0.0.0` keeps compatibility with older clients that omit a version or report a
non-semantic descriptive version; existing length/control-character checks remain.
With a minimum enabled, missing, malformed and older versions are rejected before
membership is published, for password, signature and anonymous logins alike.
Exactly `9.9.9` follows the running server's semantic version as a minimum,
including prerelease precedence; newer clients remain accepted. `9.9.9.9` is
invalid. Build metadata does not affect ordering. Invalid configuration or an
unparseable server version in sentinel mode prevents startup. Rejections include
the installed and required version and an update instruction, also on older
clients. This is a compatibility gate on self-reported version metadata, not
binary attestation. ServerQuery/gRPC integration accounts remain governed by their
separate authorization contract and cannot create native channel membership.


This table lists commonly used settings and built-in defaults. See
[`internal/config/config.go`](internal/config/config.go) for the complete schema
and validation, [`config.yaml`](config.yaml) for a development example, and
[`docker-compose.yml`](docker-compose.yml) for container-specific overrides.

| Environment Variable | Default Value | Description |
| :--- | :--- | :--- |
| `NOXA_SERVER_NAME` / `NOXA_SERVER_PASSWORD` | `noXa` / empty | Displayed server name and admission password; required in production unless `NOXA_ALLOW_ANONYMOUS=true` |
| `NOXA_ALLOW_ANONYMOUS` | `false` | Acknowledge that an empty server password admits anyone, including anonymous guests, when `NOXA_DEV_MODE=false` |
| `NOXA_CLIENT_MIN_VERSION` | `0.0.0` | Native client minimum; `0.0.0` permits any version, exactly `9.9.9` requires the running server version or newer, e.g. `0.5.94` requires that release or newer |
| `NOXA_DEV_MODE` | `false` | Production validation by default; the checked-in development profiles explicitly opt in to `true` |
| `NOXA_TCP_ADDR` | `:12333` | Primary control TCP listener address |
| `NOXA_UDP_ADDR` | `:12334` | UDP keepalive ping/pong listener address |
| `NOXA_GRPC_ADDR` | `127.0.0.1:12338` | Plaintext gRPC administration listener; loopback is mandatory |
| `NOXA_QUERY_ADDR` | `127.0.0.1:12335` | ServerQuery admin protocol binding address |
| `NOXA_QUERY_ALLOW_REMOTE` | `false` | Explicitly permit a non-loopback raw ServerQuery bind; prefer SSH instead |
| `NOXA_QUERY_SSH_ENABLED` | `false` | Enable the SSH-wrapped ServerQuery listener |
| `NOXA_QUERY_SSH_ADDR` | `:12339` | SSH ServerQuery listener address |
| `NOXA_QUERY_ALLOWED_CIDRS` | empty | Comma-separated canonical CIDRs (e.g. `10.0.0.0/8,192.0.2.10/32`) allowed to connect to raw and SSH ServerQuery; other sources are closed before the banner. Empty allows every source the bind address reaches |
| `NOXA_FILE_ADDR` | `:12336` | File transfer upload/download listener address |
| `NOXA_HEALTH_ADDR` | `:12337` | Health/readiness HTTP listener, also serving `/events`, `/hooks/` and `/dl/` links; plaintext unless `NOXA_HEALTH_TLS_ENABLED=true`, so otherwise bind loopback or proxy it behind HTTPS |
| `NOXA_HEALTH_TLS_ENABLED` | `false` | Serve the health listener over TLS with the control certificate (requires `NOXA_TLS_ENABLED`); download links then use `https://` |
| `NOXA_HEALTH_ALLOW_PLAINTEXT_CREDENTIALS` | `false` | With `NOXA_DEV_MODE=false` and health TLS off, `/events` credentials and `/hooks/` secrets are refused over plaintext from remote peers (loopback and `X-Forwarded-Proto: https` proxies pass); `true` accepts that risk |
| `NOXA_METRICS_ALLOW_REMOTE` | `false` | Permit remote `/metrics` requests; without this opt-in, only IPv4/IPv6 loopback is accepted |
| `NOXA_PPROF_ENABLED` | `false` | Enable runtime `/debug/pprof/` diagnostics; every pprof endpoint remains GET-only and direct-loopback-only |
| `NOXA_SHUTDOWN_TIMEOUT` | `30s` | Positive total grace period shared by all services during orderly shutdown |
| `NOXA_SHUTDOWN_GRACE_PERIOD` | `0s` | Announcement window before an orderly shutdown (max `10m`). After the first SIGTERM/interrupt, readiness turns unhealthy, clients get a countdown (repeated at 5 min, 1 min, 30 s and 10 s left, with a reconnect window so clients return spread over 5–60 seconds after the restart) and connected sessions keep working. New logins are refused during the period: updated clients are told when to come back and retry then, older clients see a closed connection and retry as usual; a second signal skips the rest of the wait, and the orderly shutdown then completes within `NOXA_SHUTDOWN_TIMEOUT`. Keep your orchestrator's stop timeout (e.g. Compose `stop_grace_period`) above grace period plus shutdown timeout |
| `NOXA_INGRESS_SHIELD` | `off` | Anti-raid shield: `off`, `auto` (raised by a connection surge) or `on`. While raised, every login first solves a SHA-256 proof-of-work challenge bound to its connection (desktop and web app solve it automatically, the web app in a background worker); clients without support are refused with "update noXa". The pre-auth connection caps still apply, and linking a new device waits until the shield is lowered |
| `NOXA_INGRESS_SHIELD_ARRIVALS_PER_SECOND` | `30` | `auto` surge threshold: new control connections within one second |
| `NOXA_INGRESS_SHIELD_DISTINCT_IPS` | `10` | `auto` surge threshold: distinct source addresses among them (IPv6 counted per /64) |
| `NOXA_INGRESS_SHIELD_DIFFICULTY` | `18` | Proof-of-work leading zero bits, `12`–`24` (18 ≈ 260k hashes, well under a second on a phone) |
| `NOXA_INGRESS_SHIELD_HOLD` | `5m` | How long an automatically raised shield stays up after the last surge second (`1m`–`1h`) |
| `NOXA_DATABASE_URL` | `postgres://...` | PostgreSQL connection URL |
| `NOXA_REDIS_ADDR` | `localhost:6379` | Optional Redis address for pub/sub fanout |
| `NOXA_REDIS_ENABLED` | `true` | Set `false` to run without Redis; the supplied Compose stack starts Redis by default |
| `NOXA_REDIS_PASSWORD` | empty | Redis password; required in production when `NOXA_REDIS_ADDR` is not loopback (Compose passes `REDIS_PASSWORD`) |
| `NOXA_TLS_ENABLED` | `true` | Enable TLS 1.3 encryption on control port |
| `NOXA_TLS_DIR` | `./data/tls` | Directory storing the generated TLS certificate and key |
| `NOXA_TLS_CERT_FILE` / `NOXA_TLS_KEY_FILE` | empty | Custom certificate and key; both must be configured together |
| `NOXA_FILE_TLS_ENABLED` | `true` | Enable TLS 1.3 on file transfers; disabling is development-only |
| `NOXA_FILE_ROOT` | `./data/files` | Root storage path for uploaded channel files & avatars |
| `NOXA_FILE_MAX_CONNECTIONS` | `128` | Concurrent accepted file-transfer connections (1–10000) |
| `NOXA_FILE_USER_QUOTA_MB` | `0` | Per-user storage quota across uploads and moves (`0` means unlimited) |
| `NOXA_FILE_ORPHAN_CLEANUP` | `true` | Storage sweep, at most every six hours: removes upload leftovers (single-shot `.part-*` after 1 h, resumable `.part-r*` after 24 h) and blobs no files row references; never touches a referenced path, so deduplicated hard links stay intact |
| `NOXA_FILE_ORPHAN_DRY_RUN` | `false` | Log and count (`noxa_file_reconcile_total`) what the sweep would remove without removing anything |
| `NOXA_FILE_ORPHAN_GRACE` | `24h` | Minimum age of an unreferenced blob before the sweep removes it (at least `1h`) |
| `NOXA_FILE_ORPHAN_DELETE_MISSING_ROWS` | `false` | Also delete files rows whose blob is missing; by default they are only logged, since an unmounted volume looks the same |
| `NOXA_UDP_RATE_LIMIT_PPS` / `NOXA_UDP_RATE_BURST` | `200` / `400` | UDP per-source-IP packet rate limit and burst allowance for DDoS mitigation |
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
| `NOXA_WEBRTC_ENABLE_AV1` | `false` | Register optional AV1 video codec on the SFU engine |
| `NOXA_TURN_SECRET` / `NOXA_TURN_URIS` | empty / empty | TURN shared secret and advertised relay URIs |
| `NOXA_TURN_CREDENTIALS_TTL` | `24h` | TURN credential lifetime; must be positive and at most 30 days |
| `NOXA_REDIS_DIAL_TIMEOUT` / `NOXA_REDIS_READ_TIMEOUT` / `NOXA_REDIS_WRITE_TIMEOUT` | `5s` / `3s` / `3s` | Redis client timeouts when Redis is enabled |
| `NOXA_REDIS_TLS_ENABLED` | `false` | Enable verified Redis TLS (TLS 1.2+); optional server name and CA file use `NOXA_REDIS_TLS_SERVER_NAME` / `NOXA_REDIS_TLS_CA_FILE` |
| `NOXA_RECORDING_ENABLED` / `NOXA_RECORDING_DIR` | `false` / `recordings` | Opt-in server recording; requires FFmpeg and recording permissions |
| `NOXA_RECORDING_MAX_CONCURRENT` | `4` | Combined budget for starting and active recording sessions |
| `NOXA_RECORDING_WINDOWS_ACL_READY` | `false` | Acknowledge restricted, inheritable NTFS DACL provisioned on the recording directory for Windows |

`NOXA_CHAT_MASTER_KEY` takes precedence over `NOXA_CHAT_MASTER_KEY_FILE`.
Use secret injection for the override; it accepts either a single base64 32-byte
key or a newline-separated `id:base64` key ring for key rotation.

Default member assignment is managed through **Tools → Roles** in the
active `roles-v1` policy. The retired `NOXA_DEFAULT_GROUPS_ENABLED` setting does
not configure current roles. Video ceilings can also be managed through the
authorized media-limits interface.

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

<a id="certificate-rotation"></a>

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
5. Secure messaging is bound to the pinned certificate fingerprint, so plan a
   rotation as a secure-messaging change too: readable history stays, but a
   desktop may need **Settings → Security → Recover secure messaging**
   afterwards. Each linked phone or browser holds a binding its desktop signed
   for the old certificate: it detects the change, blocks secure messaging
   and asks to be linked again. Link it again from a desktop that already
   trusts the new fingerprint (**Connections → PWA / Mobile login**). A browser
   guest instead shows the old and new fingerprints and continues only after
   its user explicitly accepts the change.

<a id="mobile"></a>

### Mobile web app

The Docker build compiles `mobile/` through Vite's production pipeline, including
its separate service-worker build. The runtime image contains the minified
JavaScript in `/web/app/assets/` and `/web/app/sw.js`; source maps and frontend
source files are not shipped. The separately hosted product website and the
Windows desktop frontend are not part of that Docker image.

The server can serve an installable web app for phones. It needs a domain name
with a publicly trusted certificate: browsers cannot pin the control
certificate, and passkeys bind to the domain.

| Key | Default | Purpose |
| :--- | :--- | :--- |
| `web.enabled` | `false` | Start the web listener |
| `web.addr` | `:12342` | Listener address (put HTTPS in front of it) |
| `web.public_url` | — | `https://` origin phones open; its host is the passkey relying party |
| `web.cert_file` / `web.key_file` | — | Native TLS with your own certificate |
| `web.acme` / `web.acme_email` / `web.acme_cache_dir` | `false` / — / `./data/acme` | Native TLS through Let's Encrypt (TLS-ALPN on :443) |
| `web.trusted_proxies` | `[]` | Exact proxy CIDRs allowed to send `X-Forwarded-For`/`-Proto` |
| `web.allowed_origins` | `[]` | Extra HTTPS origins accepted on `/ws` |
| `web.max_devices_per_account` | `10` | Linked devices per account, including the first desktop |
| `web.ws_connections_per_ip` / `web.ws_attempts_per_minute` | `8` / `30` | WebSocket admission per client IP |
| `web.push_enabled` | `true` | Send Web Push notifications to linked phones |
| `web.push_vapid_key_file` | `./data/keys/vapid.pem` | Push signing key, created on first start; keep it to keep subscriptions |
| `web.push_subject` | `web.public_url` | `mailto:` or `https:` contact push services see |

Exactly one TLS mode is required: certificate files, ACME, or a TLS-terminating
proxy listed in `trusted_proxies`.

**Docker Compose with Caddy.** Point a DNS name at the host, then:

```bash
NOXA_WEB_DOMAIN=chat.example NOXA_WEB_ENABLED=true \
NOXA_WEB_PUBLIC_URL=https://chat.example \
docker compose --profile mobile up -d
```

**Other proxies.** The proxy must pass WebSocket upgrades on `/ws`, overwrite
`X-Forwarded-Proto` and `X-Forwarded-For`, and allow long-lived connections.
Its address must be the only entry in `web.trusted_proxies`.

```nginx
location / {
    proxy_pass http://noxa:12342;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header X-Forwarded-For $remote_addr;
    proxy_set_header X-Forwarded-Proto https;
    proxy_read_timeout 1h;
}
```

```yaml
# Traefik (Docker labels on the noxa service)
- traefik.http.routers.noxa-web.rule=Host(`chat.example`)
- traefik.http.routers.noxa-web.tls.certresolver=letsencrypt
- traefik.http.services.noxa-web.loadbalancer.server.port=12342
```

**What the app does.** The web app covers the desktop client's everyday
features:

- **Chat:** rich text (markdown, custom emoji, link previews, images in a
  lightbox, inline video), an emoji picker, replies, reactions, pins,
  editing and deleting, @mentions (users, roles, @here), keyword highlights,
  undo send, join/leave lines, a new-messages divider, blocked users' messages
  folded, channel topics and slow mode, polls, threads and forum channels,
  search inside a chat and across history, an **Inbox** of unread
  conversations and mentions, **Saved messages**, chat export (text or
  passphrase-encrypted), and a camera/photo/file attach sheet.
- **Direct messages and private groups:** end-to-end encrypted, with
  attachments, voice messages, typing and read receipts (can be turned off),
  safety numbers (compared aloud or by scanning each other's QR code), disappearing messages, muting, missed-call lines, and group management (rename,
  invite, remove, roles, owner, leave, delete).
- **Voice and calls:** voice channels with push-to-talk or voice activation,
  microphone settings and test, per-member volume, whispering, priority
  speaker, raised hands, voice-room chat, a data saver, watching cameras and
  screens on a stage, and private audio/video calls with camera switching and
  call waiting.
- **People:** status and status message, display name and profile picture,
  profile cards, a member list, contacts, private notes, blocking and pokes.
- **Notifications:** per-event and per-channel levels, do not disturb, quiet
  hours, sounds and an **Activity** list of mentions, messages, calls,
  invitations and pokes. The phone also sends these filters to the server, so
  muted pushes are not sent at all.
- **Files:** a per-channel file browser with an upload queue, rename, move,
  delete and integrity checks.
- **Settings and diagnostics:** **You** has the profile, contacts and a
  searchable list of settings (appearance with themes, accent and text size;
  chat display; notifications; voice; direct messages; language; data and
  storage; about), then the account (passkeys, devices, key backup,
  servers). **Diagnostics** shows live ping, runs a connection test and
  exports logs.
- **Administration:** owners, administrators and members with the matching
  permissions get **You → Administration**: roles and their members, bans,
  channels and their access, server settings, chat filters, webhooks, custom
  emoji, the audit log and complaints. Moderators can also move, mute, kick
  and ban members from their profile card or the Servers tab.
- **Tablets and desktops:** from 900 px wide the app switches to a
  three-column layout with a sidebar, chat tabs, a details panel, a voice card
  and a quick switcher (Ctrl/Cmd+K) that also opens Search, Inbox, Saved
  messages and Activity.
- **Sharing into the app:** on Android, the app appears in the system share
  sheet; shared text and files go to a chat you pick.

**Linking a phone.** In the desktop client choose **Connections → PWA / Mobile login**. Scan the QR code with the phone camera, or open the server
address on the phone and type the hidden code. Both screens show the same
two-digit code; compare it, then approve on the desktop. The phone can then save
a passkey, which lets a new phone sign in without the desktop. Administrators
list, sign out and reset devices with `devicelist`, `devicerevoke` and
`devicereset` (ServerQuery) or `ListDevices`, `RevokeDevice` and
`ResetDevices` (gRPC). Devices unused for 180 days are signed out after a
warning at 166 days; an account's first desktop never expires.

**Signed device revocations.** When you sign out one of your devices from a
desktop or phone that is itself a trusted device of the account, that device
signs the revocation (`noxa-device-revocation-v1` over the revoked device's
exact keys, the time and the reason). The server stores the statement and
lists recent revocations next to your active devices in the key directory.
Your contacts and your other devices check the signature against your device
chain and then pin the revocation: the device stays refused for direct
messages and secure messaging even if a server later lists it as active
again, its sessions end, and the conversation shows that you signed it out.
Only your first device can revoke itself. Revocations nobody signed (an
administrator's reset, idle expiry, an integration or an older app) are still
applied, but clients show them as removed by the server. Older clients ignore
the statements and keep relying on the server's device list. This release
adds migration `069_device_revocations.sql`; run the matching `migrate`
offline before starting the server.

**Several servers.** Each server serves its own copy of the app at
`https://<server>/app/`, so one installed app (home screen icon) belongs to one
server, with its own sign-in, keys and notifications. **You → Servers** (or the
⋯ menu in the Servers tab) is a per-browser bookmark list: the first open of a
server adds it, **Add server** saves another address, and choosing one opens
that server's app. Another server's name appears once it has been opened there,
because the app's content security policy only allows connections to its own
server.

**Invitations, rules and backups.** Channel invitations from the desktop
(`noxa://<host>/invite/<channel>#<token>`) open in the app as
`https://<server>/app/invite/<channel>#<token>` or `/invite/<channel>#<token>`;
a `noxa://` link can be pasted in the Servers tab's ⋯ menu. Someone not yet
signed in joins after signing in or continuing as guest. When the server has
rules, the phone shows them and stays out of channels and chat until they are
accepted. **You → Back up this device's keys** saves the phone's keys in a file
encrypted with a passphrase (AES-256-GCM with a PBKDF2-SHA256 key, 600,000
iterations; format in `mobile/src/core/backup.js`); **Restore from backup** on
the sign-in screen signs the same device in again as long as it has not been
signed out. A confirmed phone can confirm another device of the account for
direct messages, copy its direct-message history to it and rename devices
(**You → Devices**).

**Notifications.** In the phone's **You → Notifications**, turn on
notifications for direct messages, mentions, pokes and incoming calls. On
iPhone this needs iOS 16.4 or later and the app added to the Home Screen.
The server needs outbound HTTPS to the browsers' push services (Google,
Mozilla, Apple). Pushes carry only the kind of event and, unless the phone
turned on **Hide notification details**, the sender's nickname and channel
name; message text never leaves the server in a push, and direct messages stay
end-to-end encrypted. While the account's desktop has seen input in the last
two minutes, pushes for messages wait 60 seconds and are dropped if the
conversation is read in that time; calls always push at once, and **Always
notify this phone** turns the delay off. Reading a conversation on any device
clears its notification on Android and desktop browsers; iPhones clear it when
the app next opens, because iOS ends subscriptions that receive pushes without
a visible notification.

**Direct messages, voice and calls.** Direct messages are end-to-end
encrypted with Olm for every confirmed device of both people, so they reach
the desktop and the phone (see [Encryption](#encryption)). Contacts verify each device against the identity they pinned: the
account's first desktop, and devices it approved. After passkey sign-in, a connected trusted desktop automatically confirms the
new device for direct messages after verifying the server's passkey proof.
If no trusted desktop is online, that endorsement waits until one connects;
manual confirmation remains available in **Connections → PWA / Mobile login**. When you approve a phone, the desktop can copy
recent direct-message history to it (90 days by default; up to 10,000
messages and 50 MB, deleted from the server when the phone has it or after
24 hours). Copied records are marked as copied history. Updated apps send
direct messages only with end-to-end encryption; older apps can still send
legacy direct messages to a contact with a single desktop, and are asked to
update when the contact has several devices or uses the web app.

A linked phone or browser trusts its own account only through the desktop
statement it was linked with: its account root is the key of the desktop that
approved the link, never a key the server reports. Every desktop of an account
signs in with the account's one identity key, so any of them can link. If the
account's key changes (for example, a desktop with a different key signs in
with the password and rebinds the account), phones and browsers linked
earlier stop sending secure messages and ask to be linked again from a
desktop that has the current key. A typed link code, a passkey sign-in without
a link statement, or a backup from before this release also asks for a new
link.

Voice channels, watching streams and private calls use WebRTC in the
browser. Private calls are relay-only, so they need TURN (`turn.managed`);
on mobile networks that block UDP, offer TURN over TLS on port 443 as well
(configure `turn.tls_proxy_listen_addr` and exact `turn.tls_proxy_trusted_ips`
for a private PROXY-protocol backend behind the TLS proxy). A callee's desktop and phones ring
together; the device that answers takes the call. Attachments travel over
`/ft` on the web listener, end-to-end encrypted as on the desktop.

Messages written while the phone is offline wait in an encrypted outbox and
are sent when it reconnects, or by the service worker through Background
Sync on Android. Unsent messages expire after 14 days.

**What the phone stores.** The device's Ed25519 and X25519 keys live in the
browser's IndexedDB, encrypted with a non-extractable WebCrypto key. Signing out
or revocation deletes the local device keys. Normal sign-out preserves saved
passkeys so they can be used to sign in again. Direct-message history, contacts' pinned
identities, the outbox, saved-message references, the activity list, call
history and content shared into the app are kept in IndexedDB under the same
key and are deleted on sign-out. Account-specific settings (including contacts,
notes, blocks and presence text) are cleared too; browser appearance preferences
and server bookmarks are retained. The server keeps each device's public keys, name,
creation and last-use times, and the passkey's public key and sign counter; it
never receives private keys. Channel messages are encrypted with
server-managed keys, as on the desktop.

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
| `broadcast` | `data=<escaped_JSON_with_text>` | Send a one-time encrypted alert (up to 2048 bytes of text, and the escaped command must fit the 4096-byte line limit) to every connected member allowed to view the server (server-level `view_channel`); requires `manage_server`, is audited and is not stored or replayed. Delivery is best effort, like other server-wide events |
| `ingressshield` / `ingressshieldset` | — / `data=<escaped_JSON_with_override>` | Show the anti-raid shield (mode, operator override, whether it is raised, until when, difficulty), or force it with `{"override":"on"}` / `{"override":"off"}` and return it to the configured mode with `{"override":"auto"}`; requires `manage_server`, and every change is audited |
| `messagebulkdelete` | `data=<escaped_JSON_with_channel_id_and_message_ids>` | Delete up to 100 messages of one channel (`channel_id` 0 = server chat) in one transaction and return `deleted` and `not_found` IDs. Own messages need `view_channel`, other members' messages `manage_messages` in that channel; one message the account may not delete rejects the whole request. Members get the usual delete events; deleting others' messages is audited |
| `integrationscopes` / `integrationscopesset` | `data=<escaped_JSON_with_unique_id>` / `data={"unique_id":…,"scopes":[…]}` or `{"unique_id":…,"full":true}` | Read or replace an integration account's scope ceiling (needs `manage_server`, audited) |

**Integration scopes.** An integration account can be limited to a scope
ceiling, so a bot gets only the APIs it needs. Scopes never grant anything:
every call still needs the role permission it checks. Without a ceiling an
account keeps its current access. Scopes: `server:read`, `server:manage`,
`roles:read`, `roles:manage`, `channels:manage`, `members:voice`,
`members:move`, `members:kick`, `members:ban`, `devices:manage`, `audit:read`,
`complaints:manage`, `events:read` (gRPC `Events.Subscribe` and the event
WebSocket) and `messages:manage`. Every ServerQuery command and gRPC method maps
to one scope (an empty ceiling allows nothing); signing in (`login`, gRPC
`Authenticate`) needs none. Set them with
`adduser -integration -integration-scopes messages:manage,events:read`, the
`integrationscopesset` command or the gRPC `SetIntegrationScopes` RPC. A scoped
caller can only hand out scopes it holds and cannot clear a ceiling. A change
signs the account's open integration sessions out: they must log in again.

**Voice bots.** `internal/botvoice` reads Opus packets from an Ogg/Opus file
and paces them as RTP for a WebRTC audio track (the publisher `cmd/loadtest`
uses). It cannot publish on servers with `noxa-media-e2ee-v1`: media keys go
only to enrolled noXa devices over Olm ratchet controls, which bots do not have
yet, so `botvoice.CheckServer` refuses such servers.

Provision integration accounts offline with `adduser -integration`; this enables
login but grants no roles. Current capabilities and hierarchy apply to every
operation, including existing sessions. Retired numeric permissions and privilege
tokens have no compatibility fallback. Use `help` for the complete command list.
SSH sessions must separately negotiate `NOXA_AUTHORIZATION_MODEL=roles-v1`.

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

CI runs once per pull-request update, plus pushes to `main` and `v*` release
tags. Browser tests run in four shards with two workers each; tests within the
same spec file can run in parallel. To reproduce one shard locally, run
`npm run test:e2e -- --shard=1/4 --workers=2` from `client/frontend` (replace `1`
with `2`, `3`, or `4`). The `frontend-e2e` check requires all four shards and the
separate frontend build, unit, and SFU browser checks to pass.

On Linux, install the desktop libraries listed above and Playwright's Chromium
system dependencies (`npx playwright install --with-deps chromium`). Headless
native-client tests in CI use `xvfb-run -a go test ./...`.

| Command | Scope |
| :--- | :--- |
| `make build` | Compile the server binary into `./bin` |
| `make client-build` | Build the Wails desktop client with embedded version metadata |
| `make run` | Run the server locally with version metadata |
| `make test` | Root and client Go tests; supplies an embed placeholder, but does not run browser tests |
| `make migrate` | Run database migrations via `./cmd/migrate` |
| `make compose-up` / `make compose-down` | Build and start/stop the detached container stack (noXa + PostgreSQL + Redis) |
| `make chaos` / `make webrtc-load` | Run database failure drills and simulate 100 authenticated Opus publishers |
| `make canary` | Run encryption rotation and plaintext-leakage security canaries |
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
exact commands and live protocol fixtures. Website checks are separate in [`website/README.md`](website/README.md).

The browser/SFU test checks independent single-encoding shares, upload suspension
without viewers, resumption and sender-controlled quality changes using synthetic
canvas capture. CI runs
the 720p30 profile. On an idle development machine, set
`NOXA_BROWSER_MEDIA_PERF=1` to include the more demanding 1080p60 profile; its
observed frame rate still depends on software-encoder and host capacity.

### Performance measurements (2026-11-07)

These measurements decided three proposals; none of them changed code. Rerun them before revisiting the decisions.

**Media frame encryption (WebCrypto vs. a WebAssembly cipher).** The script
`node client/frontend/scripts/bench-media-e2ee.mjs` measures the real pinned LiveKit
e2ee worker with the noXa wrapper. Each frame is timed from enqueue to output
(Node 26 on a Windows desktop CPU, 300 frames per case):

| Frame | Encrypt p50 / p95 | Decrypt p50 / p95 |
|---|---|---|
| Opus 20 ms (160 B) | 0.09 / 0.17 ms | 0.10 / 0.17 ms |
| VP8 1080p60 delta (16.7 KB) | 0.11 / 0.26 ms | 0.11 / 0.23 ms |
| VP8 1080p60 keyframe (133 KB) | 0.34 / 0.98 ms | 0.23 / 0.62 ms |
| VP8 1440p60 keyframe (266 KB) | 0.43 / 1.41 ms | 0.33 / 0.98 ms |

The same frames through Chromium's own WebCrypto AES-GCM take no more than 0.1 ms
at p95 for 1080p delta frames, which is Chromium's timer resolution. At p95 a 1080p60 delta frame costs
about 1.6% of the frame budget to encrypt, and about 3% to encrypt and decrypt
together. Keyframes, sent roughly every 2 s, cost under 10% for both. The
decision rule was to look at a WebAssembly cipher only if 1080p60 video frames
exceeded about 1 ms at p95. They don't, so the maintained LiveKit cryptor stays.

**Video pacing gain.** The real pacer was simulated under virtual time at gains
1.0, 1.25, 1.5 and 2.0. A modelled bottleneck link (a drop-tail queue with 100 ms of buffer)
ran at 1.0x, 1.2x and 1.5x the congestion estimate:

- A higher gain only lowers end-to-end delay when the link has spare capacity above
  the estimate. At 1.0–1.2x it produces loss at the bottleneck: 32–293 packets
  dropped per 10 s at gains 1.5–2.0.
- A lower gain avoids that loss but adds 150–280 ms of p95 queueing. At 1.0–1.25 the pacer
  starts dropping expired packets itself after the estimate falls.
- A dynamic gain cannot tell the two cases apart without the bottleneck feedback
  that GCC already turns into the estimate.
- When the estimate drops below the source rate, the queue grows to seconds at
  any gain. Fixing that is the sender's job (resolution and frame-rate adaptation).

The static gain of 1.5 therefore stays (`internal/webrtc/media_pacer.go`).

**Database indexes.** A scratch database seeded with 1M Olm messages and 2M
deliveries, of which 2,000 were pending, ran the two pending-delivery sweeps:

- Both use the existing partial index `olm_delivery_pending_sender`
  (`WHERE state='pending'`) and read only pending rows: 207 buffers.
- Warm, they take 14 ms and 10 ms. The first cold run took about 150 ms, mostly JIT start-up and uncached pages.

The other hot paths are already covered by keys:

- Chat history uses `(channel_id, id)`.
- Private conversations use `(conversation_id, from_uid, client_reference)` and `(message_id, recipient_uid)`.
- Olm copies use `(message_id, device_id)`.
- `server_traffic_buckets` is small and indexed.
- `audit_log` is pruned by monthly partitions.

No index was added.

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
├── mobile/                     # Installable PWA, service worker and browser tests
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

Production operators should define service-level objectives, practice backup
and restore drills, and keep their incident runbook outside the deployment being
operated. Update these local procedures when architecture, telemetry or recovery
steps change. The `docs/` directory and `docker/secrets/` are local-only and ignored
by Git; keep operator notes and credentials out of the public repository.

`noxa_push_deliveries_total{result}` counts Web Push attempts: `ok`, `retry`
(a 429/5xx or network failure retried up to twice within the 20-second send
budget, honouring `Retry-After`), `gone` (expired subscription, removed) and
`failed`. Incoming webhook responses carry `X-RateLimit-Limit` (5 per
3 seconds per hook), `X-RateLimit-Remaining` and `X-RateLimit-Reset`
(seconds); a 429 also sets `Retry-After` to the real reset time.

`noxa_ingress_shield_events_total{event}` counts the anti-raid shield:
`raised` and `lowered` (also written to the audit log with their cause),
`challenged`, `solved` and `failed` logins, and `rejected` clients that cannot
solve a challenge. A raised shield with few `solved` events means a flood
that is not finishing logins.

`noxa_control_disconnects_total{transport, reason}` counts closed control
connections (`tcp` or `ws`) by fixed reason: `eof`, `unexpected_eof`,
`local_close`, `read_timeout`, `read_error`, `write_timeout`, `write_error`,
`protocol_error`, `frame_too_large`, `auth_timeout`, `preauth_rejected`,
`server_inactivity`, `kicked`, `shutdown`, `panic` and others. A rise in
`auth_timeout` or `preauth_rejected` points at stalled handshakes or a
connection flood; `protocol_error` and `frame_too_large` at incompatible or
misbehaving clients.

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
loopback-only even if remote metrics are enabled.

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

For a scoped, point-in-time server view of the publisher's selected ICE pair,
use the separate loopback-only endpoint with the publisher ID, publication
`generation` and ingress `ssrc` from `/debug/streams`:

```sh
docker compose exec noxa wget -qO- 'http://127.0.0.1:12337/debug/stream-route?publisher_id=EXAMPLE&slot=screen&generation=23&ssrc=42'
```

The response includes each candidate's address, port, protocol and type, plus an
opaque `route_token`. Pass that token in subsequent requests to reject a changed
peer, ICE epoch or candidate pair with HTTP 409; stale publication or membership
scope also returns 409. Invalid scope returns 400. This is a loopback access
boundary, not endpoint authentication, even when remote metrics are enabled.
Addresses are confined to this operator response, never ordinary client stream
telemetry; ICE credentials and SDP are not returned. Candidate tuples describe
Pion's selected pair and may differ from host wire tuples after NAT or container
translation. A successful snapshot does not attest continuous route stability.

Publisher-facing TWCC feedback uses one worker per connection so a slow feedback
write does not block the recorder and incoming RTP reads. Pending feedback is
bounded to four batches and 64 KiB of serialized reports, plus one in-flight
batch (decoded objects add overhead). Reports retain their original arrival
measurements and order. Before admission and sending, reports pending for 250 ms
are discarded; overflow discards the oldest pending reports. This omits feedback,
not media, and does not fabricate packet loss or bandwidth. The shared audio/video
TWCC scheduling changes; audio RTP pacing and non-TWCC RTCP remain unchanged.
`upstream_feedback.dispatch` distinguishes admission, completed write attempts,
errors, queued bytes, and discarded reports. Missing dispatch data means unknown;
a successful write does not prove delivery. On shutdown pending reports are
cleared, while Pion closes the transport before waiting for the in-flight write.
This removes a reproducible feedback/read dependency; it is not by itself proof
of stable 1080p60 on a particular network.

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

### Data retention

The server runs bounded housekeeping at startup and every five minutes:

- Delivered offline DMs: 30 days after delivery. Undelivered messages are retained.
- Server recordings: 30 days after the recording ends. Active recordings and processes still cleaning up are retained. Recognizable abandoned recording sets use their last manifest write time. Corrupt manifests and unrelated files are preserved for operator inspection.
- Audit events: 180 days, including daily aggregates. Daily aggregates retain the cutoff calendar day in UTC.

Each pass removes at most 500 rows per database table and 128 recording files, and scans at most 128 recording directory entries. Existing backlogs therefore expire gradually. Housekeeping uses indexed queries, skips locked rows, has a bounded execution context, and runs independently of chat and audio requests. Shutdown cancels and joins the worker before closing storage. These limits apply to the live database and recording directory; independently retained backups are not rewritten.

#### Audit log partitions

`audit_log` is partitioned by month (UTC) since migration `068_audit_log_partitions.sql`. **Upgrading to this release needs the offline `/out/migrate` run described under [Runtime database privilege separation](#runtime-database-privilege-separation)** — the server refuses to start against the old schema. The migration copies every audit row into the partitioned table inside one transaction (same ids, same id sequence; the table is locked while it runs, which takes about a second per few hundred thousand rows) and fails if the row count differs.

The serving role cannot create tables, so `/out/migrate` maintains the partitions: every run creates the partitions for the current month and the next 12 months, and moves any rows that reached the `audit_log_default` partition into a monthly partition of their own. Inserts never fail for a missing month; they land in the default partition, and the server logs a daily warning (`audit_log needs partition maintenance`) when next month's partition is missing or the default partition holds rows. Running `/out/migrate` at least once a year, as every upgrade does, is enough.

`audit_partition_retention_months` (default `0`, keep every partition) lets `/out/migrate` drop whole monthly partitions that ended more than that many months before the current month; it must be `0` or at least `6`, so it never removes audit rows the 180-day row retention above would keep. Each dropped partition is recorded in the audit log (`audit.partition_dropped`, with the number of rows it held). Chat messages are not partitioned: reactions, pins, polls and discussions reference messages by id, and PostgreSQL only allows such references to a partitioned table through a key that includes the partition column.

Migrations `070_olm_session_initial_delivery.sql` and `071_olm_session_target.sql` add two indexes to `olm_sessions` (built `CONCURRENTLY`, so the table stays writable), used by every delivered DM copy's acknowledgement and by admission. **Upgrading to this release needs the offline `/out/migrate` run**, as for 068.

### Direct-message identity verification

Updated desktop clients require a signed encryption identity for direct messages,
private-group recipients and private-call device keys. The Ed25519 signature binds
an account's canonical UID to its X25519 key; signatures travel with directory
responses, live/offline DM bindings and call snapshots. The server checks signed
publication against the identity used by that authenticated session. Channel keys
continue to use the raw X25519 handshake key for server-readable channel chat.

The desktop stores the first verified signing fingerprint per local identity,
server destination and contact. This is trust on first use: verify fingerprints
through another trusted channel when establishing an important contact. A later
signing-identity change blocks the contact, including after restarting the client.
Sending or calling offers an explicit dialog showing both fingerprints; Cancel
keeps the block. Rotating an encryption key under the same signing identity does
not reset trust. A damaged or unwritable trust store fails closed. Retired identities
cannot silently replace a confirmed identity through delayed message delivery.

Older peers publishing only unsigned encryption keys must update before an updated
client can exchange private messages or calls with them. Unverifiable historical
DM bindings are shown as unavailable rather than marked verified. This change does
not add forward secrecy: current message bodies still use static X25519 and
XSalsa20-Poly1305. Channel passwords restrict voice entry; chat and files use the
channel's Permissions policy.


#### Safety codes (QR)

Next to the safety number, the desktop's encryption details and the phone's
**Safety number** sheet show a QR code, and **Scan their code** opens the camera
to read the contact's code. The code holds `noxa-sn:v1:<scheme>:<shown by>:<shown to>:<digest>`
and nothing secret. A scan verifies the contact only when the code was shown by
that contact, for you, and its SHA-256 digest equals yours; a different digest
is reported as a mismatch and never saved, and a code for another conversation
keeps the scanner open. Frames are decoded on the device (BarcodeDetector where
available, otherwise the bundled jsQR); the camera opens only from the button,
without audio, and stops when the scanner closes. Comparing the numbers aloud
stays available, also when the camera is unavailable or denied.

The desktop code carries the pairwise safety number (both X25519 identity keys);
the phone's code carries both account fingerprints it shows. The two check
different keys, so neither the codes nor the displayed values can be compared
across apps: a desktop and a phone report each other's code as coming from the
other app, and comparing aloud only works between contacts using the same app.
Bundled libraries: jsQR 1.4.0 (Apache-2.0) and, on the phone, qrcode-generator
1.4.4 (MIT); their notices ship in `frontend-licenses.txt`.

### Runtime database privilege separation

Production (`dev_mode=false`) checks the complete migration ledger and its
checksums before opening listeners. It never applies migrations at startup and
refuses superuser, database/schema owner, privileged role membership, schema
creation, temporary-table creation, or writable migration-ledger permissions.
Development mode also requires offline migrations; it only relaxes the runtime
privilege check for local tooling.

Stop the server and take a verified backup before an upgrade. Run `/out/migrate`
from the `noxa-operator` tools profile with the offline owner's credentials.
`NOXA_MIGRATION_DATABASE_URL_FILE` supplies a separate owner URL for external
PostgreSQL. The serving container receives only `NOXA_COMPOSE_DATABASE_URL_FILE`
(the runtime URL) or the dedicated `NOXA_RUNTIME_DB_PASSWORD_FILE` secret for
`noxa_runtime`; it does not mount the owner's password.

After the initial migration, provision the dedicated runtime and read-only
backup roles as the database owner. The SQL script also sets default grants
for future tables/sequences created by that owner, protects the schema ledger,
and removes inherited role memberships. Run it against this application's
dedicated database only. Passwords must be distinct and at least 24 characters:

```bash
read -r -s -p 'Runtime database password: ' NOXA_RUNTIME_PASSWORD; printf '\n'
read -r -s -p 'Backup database password: ' NOXA_BACKUP_PASSWORD; printf '\n'
export NOXA_RUNTIME_PASSWORD NOXA_BACKUP_PASSWORD
docker compose exec -T -e NOXA_RUNTIME_PASSWORD -e NOXA_BACKUP_PASSWORD postgres \
  psql -U "${POSTGRES_USER:-noxa}" -d "${POSTGRES_DB:-noxa}" \
  < scripts/provision-database-roles.sql
unset NOXA_RUNTIME_PASSWORD NOXA_BACKUP_PASSWORD
```

Store the runtime password in `NOXA_RUNTIME_DB_PASSWORD_FILE` (or use a complete
runtime URL), and the backup password in `BACKUP_POSTGRES_PASSWORD_FILE` with
`BACKUP_PGUSER=noxa_backup`. Keep the owner credential exclusively in the
operator tools and PostgreSQL service. For external PostgreSQL, run the same
SQL using the owner connection and `sslmode=verify-full`. Re-run provisioning
after migrations that intentionally replace protected tables. The script
updates credentials for the named dedicated service roles; do not use them
for other databases.


### Encrypted database backups

The backup profile publishes only `.tar.gz.age` files. Set
`BACKUP_AGE_RECIPIENTS_FILE` to a public age recipient file (an SSH public key is
also supported); keep the corresponding private identity off the server.
Each encrypted bundle contains `database.dump`, `keys/pii.key`, the complete
`keys/chat_master.key` ring, and a format manifest. The backup container reads
`noxadata` read-only as UID 10001, matching the application's private key files.
For a custom deployment, mount the actual configured keys; an environment-only
key override must also be supplied to this protected backup source.

Plaintext staging requires tmpfs and is removed on success or failure. Allow
RAM for both the compressed dump and archive; Compose supplies `/tmp` as tmpfs.
If encryption fails, a key changes during the dump, or a required key is missing,
no archive is published or uploaded. Seven-day local retention applies to the
new encrypted archives. Existing plaintext backups require a separate verified
conversion; uploaded objects and unrelated host backups are not rewritten by
this script. Uploaded files and recordings need the separate full data-volume
backup as well.

Restore on a trusted machine using `age -d -i <private-identity> -o bundle.tar.gz
<archive.tar.gz.age>`, extract into a private directory, and restore
`database.dump` with `pg_restore --exit-on-error --no-owner`. Restore both key
files to the configured application paths with owner-only access before
starting the matching server version. The CI image test performs this complete
round trip and compares both key files and relational fixture data.

### Remote ICE address policy

The SFU rejects remote loopback, private and shared-address-space candidates by
 default, across SDP, trickle ICE and peer-reflexive discovery. mDNS resolution
is disabled. For intentional LAN/VPN clients, set `webrtc.allowed_remote_cidrs`
(or `NOXA_WEBRTC_ALLOWED_REMOTE_CIDRS`) to the specific client subnets. An
explicit loopback-only UDP listener permits loopback for local testing.
Link-local/metadata, multicast and unspecified addresses remain blocked even
with an allowlist. Public server-reflexive and TURN relay candidates continue
to work; operator-configured STUN/TURN discovery servers are unaffected.
