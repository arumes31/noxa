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

## Interpreting receiver measurements

The expanded receiver table shows the sample interval alongside these values:

| Measurement | Meaning |
| --- | --- |
| Buffer actual / target / minimum | Average time spent in the receiver's jitter buffer, desired delay, and the minimum delay estimated without external playout constraints, in milliseconds. These are interval averages, not total microphone-to-speaker latency. |
| Concealment total / non-silent / silent | Samples generated to replace missing or late audio, split into non-silent output and generated silence. Silent concealment is included in the total; it must not be added to it again. |
| Acceleration / deceleration | Samples removed or inserted by adaptive playback to change buffer residence. A sustained rate can help explain time-stretched or uneven playback even when reported packet loss is low. |

Concealment and adaptive-playback percentages use the change in total received
samples over the displayed interval as their common denominator. Cumulative
counters remain available in the operator JSON, separately from these rates.
The first sample, a counter reset, a replaced peer/track or an unsupported browser
counter displays **—**, rather than a healthy-looking zero. Older clients can still
report the original measurements without the new optional fields.

Compare actual buffer residence with its target and minimum before changing
playout settings. If all three grow together, forcing a smaller playback target
does not remove the cause of the receiver's estimated delay and may replace it
with more missing audio. Compare affected and healthy receivers, then examine
sender timing and server forwarding. Low RTT or packet loss alone does not prove
smooth packet arrival or audible quality.

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
