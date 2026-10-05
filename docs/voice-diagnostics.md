# Receiver voice diagnostics

Client Info shows the selected visible member's ping and self-reported client
version. These fields do not grant access to hidden members, remote addresses or
restricted connection counters. Clients predating version reporting display
"Unknown" until updated and reconnected.

The **server owner and administrators** additionally see **Reception on this member's client**.
This is the selected member receiving other speakers. The ordinary Voice section
above it still describes audio received on the viewing client (or its own send
stream). The View connection info permission alone does not grant access to this
section. Each read rechecks the current role policy, so removing Administrator
revokes access immediately on the server.

Updated clients send quality measurements every five seconds while connected to
a voice channel, after the server advertises `voice-telemetry-v1`. The native
bridge supplies the client build identity. A report includes inbound Opus packet
and sample counters, interval loss/concealment percentages, jitter-buffer delays,
selected ICE-pair RTT, audio-output state/latency and playback processing settings.
It does not contain audio, SDP, ICE addresses, device identifiers or credentials.
Measurements are diagnostic claims, not authorization or proof of client identity.

Only the latest accepted report is held in memory on each authenticated connection.
Reports have a 32 KiB payload limit, a 64-track limit and a two-second ingestion
throttle. Invalid or non-finite measurements are rejected. Disconnect discards the
report, and a report from an earlier channel membership is not returned, including
leaving and returning to the same channel. The native bridge trims trailing tracks
and marks a report truncated if JSON escaping would exceed the payload budget.
A sample older than
15 seconds is marked stale. Missing initial/reset measurements are null, not zero.
This is not a recording or durable history service.

The server also retains the latest RTCP receiver report for each active outgoing
audio binding, including older clients. It validates the sender and SSRC, identifies
the source publisher/slot, and converts the Opus 48 kHz clock to milliseconds.
This feedback supplies receiver-side loss and jitter, but not audio-output or
concealment details. Removing the binding removes its feedback. RTCP percentages
use the report interval; cumulative lost packets remain separate.

## Operator access

`GET /debug/voice` on the existing health listener returns bounded JSON (at most
256 connected clients). It is **loopback only**, even when remote metrics are
enabled. No new public diagnostic listener or owner password endpoint is added.
Use exactly one optional filter: `client_id` or case-insensitive `nickname`.
The response disables caching and a missing filtered client returns 404.

For the current Docker deployment:

```sh
ssh orderotto-dev "docker exec noxa-server-1 wget -qO- 'http://127.0.0.1:12337/debug/voice?nickname=wDAF'"
```

An owner or authorized operator can compare affected and healthy receivers while
the problem occurs. Check freshness first, then interval loss, concealment, buffer
delay and output state. Healthy network counters alone do not rule out capture,
decoding, local playback processing or output-device problems. Initial join/ICE
recovery measurements should not be mistaken for steady-state playback quality.
