# Finite upstream packet timing (operator only)

Default off; no files, packet capture, client telemetry, automatic retries, or congestion policy changes.
The loopback-only `/debug/stream-timing` endpoint requires fresh `/debug/stream-route` pins:
`publisher_id`, `slot`, decimal `generation`, decimal `ssrc`, `route_token`, `peer_id`,
`ingress_started_at`. Forwarded headers grant no authority. Credentials, payloads,
keys, SDP and route addresses are not recorded.

POST with `X-NoXa-Diagnostic: upstream-timing` explicitly arms one 60-second record;
the publisher must still be authenticated in its channel. Origin and request bodies
are rejected. GET never arms or extends recording. DELETE with the same header stops
recording and preserves results. One record per router; new arm conflicts until its
automatic removal six minutes after arm (60 seconds plus five-minute retention).

Bounds: 131072 primary RTP rows, 4096 TWCC reports, 16-MiB conservative storage budget.
Arrays are preallocated before arming. Overflow stops and invalidates the recording.
Export copies use additional bounded memory. Retention expiry releases bulk arrays
even if a foreign RTCP write is still blocked; transport is never killed or joined.

Active GET and ended-but-pending GET return metadata only. Full arrays are available
only after `settled` and `data_available`: admission ended and all recorded write
outcomes returned. Write end times may exceed the 60-second capture endpoint. A pending
write remains unknown; late results cannot repopulate expired records. `complete` also
requires nonempty RTP/feedback and successful write outcomes, not merely a timer tick.

RTP rows contain only primary SSRC, raw 16-bit RTP sequence, raw transport sequence,
and monotonic microseconds since arm, immediately after SRTP read. Extension ID uses
actual StreamInfo negotiation. Kernel fixed RTP headers can join via this mapping even
when Cryptex encrypts extensions. Missing/unsupported extensions, read/parse failures,
rebind, unbind, scope change and observer close terminate recording.

TWCC copies retain typed fields, encoded status chunks and quantized receive deltas,
after asynchronous dispatch but before SRTCP. TWCC is peer-wide (audio/video/RTX).
MediaSSRC is NOT a screen filter. Attempt/end/result/bytes are separate. A successful
write is not proof of NIC transmission or sender receipt. Queue-dropped reports never
reach this hook: inspect existing dispatcher counters alongside it. Not-received
status has no arrival delta; do not replace unknown with zero.

Raw RTP/transport sequence, feedback-count and reference-time wrapping, duplicate,
reordered and overlapping reports remain unresolved. `match_validity` is null and
`sequence_matching` is `raw_modular_unresolved`. Offline matching must prove epoch,
unwrap without ambiguity, exclude failed/pending writes and preserve unmatched rows.
Wire reference time is 24 bits in 64-ms units; deltas are quantized in 250-us units.
These are application-read arrivals, not kernel socket arrivals.

Local publication/peer/ingress identities are sampled every 500 ms using nonwaiting
TryRLock/TryLock. Missed checks are counted. Pion route getters run only on explicit
arm/read, outside diagnostic locks; they use the existing synchronous operator path.
No background Pion getter can retain the capture worker. The independent timer stops
admission on idle streams too. Route snapshots cannot rule out away-and-back changes;
`scope_continuity` and `route_continuity` remain null. End-route match/time are explicit
and may postdate the capture. No strict same-packet validity is inferred by this API.

Instrumentation precedes Pion TWCC arrival stamping and therefore has measurable
cost. Packet/feedback recording uses TryLock; contention drops only that diagnostic observation instead of waiting. Recording continues
with explicit dropped RTP/report counters, but complete remains false. Normal read
deadline timeouts increment read_timeouts and do not terminate the recording. Active GET cannot copy arrays. `rtp_hook_count`, `rtp_hook_total_ns`
and `rtp_hook_max_ns` expose observed parse/record cost, excluding final atomic accounting.
Start/end UTC plus elapsed monotonic time are not a proven kernel clock bridge.
Evaluate clock drift/steps, quantization and observer overhead before comparing
relative same-packet delays. Only a window overlapping an actual BWE decline is
informative; healthy windows do not explain earlier collapses or establish causality.
