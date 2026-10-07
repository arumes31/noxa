# Stream diagnostics

Every member who can currently watch a publication can inspect its stream details.
Open **Stream details** on a watched tile or its context menu. Owner/admin access
is not required. The server rechecks channel visibility, publication permission,
viewer eligibility and exact publication/session on every request. This does not
broaden the owner/admin-only receiver voice diagnostics.

Updated clients send up to eight video encoding rows with the existing five-second,
32 KiB-bounded voice telemetry. Rows contain no capture titles, device names, media,
credentials or network addresses. Reports are authenticated to their connection,
matched to the current publication's SSRC/RID, and expire after 15 seconds.

The `stream_diagnostics_v1` capability enables a scoped `VideoStreamControl`
`diagnostics` action. Reads do not start a watch. Server ingress can be inspected
before watching; forwarding counters only exist for the viewer's actual watch.
Older servers retain local receiver diagnostics; older senders have no sender row.
An expanded client panel refreshes every three seconds and ignores late replies
after a stream, tab or connection changes. Closing the panel stops further reads.

## Reading the measurements

The health summary above the measurements offers a next step for the sender or
viewer when recent evidence shows encoding pressure, delivery problems, or
decoding pressure. If capture continues while very few frames are encoded and
sent, it reports low sender output even when the browser reports no limitation.
These are diagnostic hints, not a confirmed cause. A still screen, a share
waiting for viewers, startup samples and unavailable or stale counters are
distinguished from evidence of a problem. The summary never changes stream
quality automatically.

Capture FPS, selected FPS, browser-reported encode FPS, interval-encoded FPS and
interval-sent FPS are separate values. The sender status uses interval-sent FPS;
it does not treat the selected capture preset as a measured transmission rate.
Severely reduced FPS remains visible even if Chromium reports no CPU/bandwidth
limitation. A still desktop can legitimately encode very few frames.

The server counts unique non-padding RTP timestamps at ingress and successful
forwarding. These are observed frames, not proof that every packet arrived or
that a receiver decoded them. Duplicate repairs do not inflate frame counts.
The receiver separately measures complete frames received, frames decoded,
drops, decoding time, buffer residence, recent loss and feedback requests.

Each stage uses its own displayed sample window; clocks are not synchronized.
Initial samples, counter resets, expired reports and replaced streams show **—**.
Copy diagnostics includes cumulative counters and interval measurements with
their scope and freshness, for comparison without conflating the stages.

Browser bitrates count RTP media payload, including retransmissions. The separate
retransmission rate/percentage is a subset and must not be added again. Encoder
target bitrate excludes retransmissions. Average frame payload subtracts repairs
before dividing by encoded frames, and is approximate when send/encode queues
differ. Keyframe counts help explain unusually large frames. Server bitrates
include RTP headers, so are not identical to browser payload rates.

Updated publishers send exactly one encoding per camera or screen share. All
viewers receive the sender's chosen source quality. The sender manually changes
resolution/FPS when needed; no parallel fallback encodings are uploaded.
Screen senders use `maintain-resolution` so startup quality adaptation does not
aggressively shrink the selected resolution. Actual FPS can still fall when the
sender lacks processing capacity or upload bandwidth; the preset is a target,
not a measured or guaranteed rate. This follows Chromium's
[video adaptation policy](https://webrtc.googlesource.com/src/+/main/video/g3doc/adaptation.md).
On servers with `stream_source_quality_v1`, a publisher's `upload_active` state
pauses its video and shared audio when no authorized viewer or recorder consumes
it. Capture and preview remain available, and microphone audio is independent.
The first watcher resumes upload even before any RTP source has arrived.

Legacy simulcast publishers remain inspectable, with each layer reported
separately. Their extra layers contribute to publisher upload, but a receiver's
selected-layer bitrate does not include all layers. High bitrate with low FPS
can mean large frames, repair traffic, or a processing/delivery bottleneck;
these measurements distinguish those cases.

Field semantics follow the [WebRTC statistics specification](https://www.w3.org/TR/webrtc-stats/).
