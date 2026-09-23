# Hardware video support

Status: implementation in progress. GPU execution has not been tested on these
QA clients; the user confirmed that they have no GPUs. Native QA10 reports
`libvpx` and `powerEfficientEncoder/Decoder: false` for the tested VP8 streams.

Hardware acceleration is preferred when the device, driver and negotiated codec
support it. Software fallback remains required. The application must not force
unsafe driver overrides or claim that the presence of a GPU process proves
hardware video encoding or decoding.

## Completed adaptation changes

Host CPU pressure enters at 85%. Recovery requires 15 seconds of readings below
70%; missing, invalid or failed telemetry interrupts that recovery window.
Sending and receiving adapt independently. Runtime-reported power-efficient
video is exempt from a host-CPU-only downgrade. This is an efficiency signal,
not proof of hardware execution. Explicit bandwidth settings and server limits
still apply. Stale asynchronous polls cannot affect replacement sessions.

The native application does not set `disable-gpu`; WebView2 retains its normal
hardware acceleration and driver fallback behavior.

Server information now includes expandable video processing diagnostics. It
reports the negotiated codec, actual encoder/decoder implementation and the
runtime's independent efficiency hint for each direction. Missing values remain
"not reported"; a GPU process or a processor name never fabricates an efficiency
result. Identical processor combinations are listed once. Reports from a
replaced peer connection are discarded. English/German labels, safe text
rendering, missing metadata and connection replacement have regression coverage.

This diagnostic addition does not enable H.264 or complete the codec work below.

Validation: five connection-statistics unit tests, five focused browser tests,
ten localization tests, frontend lint, a narrow-window visual check and an
independent review passed. Native QA12 was built successfully. Its publisher and
viewer diagnostics match the actual VP8/libvpx/efficiency-false runtime reports.
Native opt-in watch and stop/resume also passed; stopping one screen halts its
video and shared audio while camera and voice continue. Evidence:
`.cache/communication-qa12-processors.log`, `.cache/communication-qa12-stop.log`
and `.cache/video-processing-*`.

## Codec work still required

The current SFU outputs, reference continuity and bounded frame inspection are
VP8-specific. Merely advertising H.264 or reordering browser codec preferences
would corrupt or reject media. Do not enable a codec until its complete path is
validated.

The proposed next increment adds one explicit H.264 profile alongside VP8:

- Pin codec/profile/packetization mode to a publication generation and all its
  layers. Codec changes require an explicit new publication, never a change
  inferred from one arriving packet.
- Negotiate separate send-only publication transceivers. Build matching SFU
  outputs after the publication codec is validated. Retire old output ownership,
  queued packets and RTX before negotiating a replacement.
- Validate single NAL, STAP-A and FU-A packets with bounded buffering. A source
  switch needs matching SPS/PPS and an IDR, not only an IDR marker. Preserve
  dimensions, packet limits, authorization and opt-in watch enforcement.
- Probe actual capture resolution, rate and layering with Media Capabilities;
  prefer supported, smooth, power-efficient configurations, handle rejected or
  absent probes, and inspect actual runtime encoder/decoder statistics.
- Establish subscriber decode compatibility before admitting the publication.
  An SFU cannot transcode a publisher's H.264 into VP8 for another viewer.
- Update the recorder's codec admission, keyframe handling, SDP and manifests.

## Encryption boundary

The installed LiveKit H.264 transform leaves only one slice-data byte clear
after the first NAL header. Variable-length slice-to-PPS references and later
NALs can therefore be encrypted. Plaintext SPS inspection alone cannot prove
encrypted-frame dimension compliance. Resolve and test that boundary before
enabling H.264 E2EE; do not silently weaken frame validation or decrypt on the
SFU. Recording key entitlement remains a separate pending user decision.

## Acceptance still required

Actual GPU-equipped camera and screen encode/decode, software fallback,
publisher/subscriber codec compatibility, late watching, stop/resume, packet
loss, publication replacement, bounds changes, queued/RTX revocation, recording
and encrypted-frame validation. Capability mocks on these GPU-less machines
do not replace GPU hardware acceptance.

The GPU-less QA10 clients passed the strict 600-second local three-video/four-audio
transport and playback gate (screens at least 6 fps, camera at least 12 fps in
every 15-second interval). Brief freezes still occurred. QA11 includes the CPU
adaptation changes; its remote gate failed when a screen fell to 5.46 fps.
Follow-up 177-second runtime traces show software encoding/decoding, occasional
sender frame-rate reductions and bandwidth limitation. They do not establish
that GPU absence caused the remote slowdown. Evidence lives under
`.cache/communication-qa11-*` and `.cache/video-local-qa/`.

Checkpoint: the server must forward encoded video by default. Selecting an
existing publisher layer does not require transcoding. Server transcoding must
require an explicit viewer quality choice; encrypted media must be resized by
clients because the SFU has no decryption keys.

The user has provided a GPU-equipped `daniel-pc` through Codex Connect and
confirmed it is online. Native hardware acceptance has not run there yet.
The new constrained-baseline H.264 packet/header inspector is groundwork only:
it is not wired into publication negotiation or SFU forwarding. Its focused
tests pass, but real-encoder interoperability, idle expiry integration, codec
continuity, recording, E2EE and GPU acceptance remain required before enabling
H.264. Header validation does not entropy-decode or validate macroblock data.

Sources: [WebView2 performance guidance](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/performance),
[Media Capabilities](https://www.w3.org/TR/media-capabilities/),
[WebRTC statistics](https://www.w3.org/TR/webrtc-stats/),
[H.264 RTP packetization](https://www.rfc-editor.org/rfc/rfc6184),
and installed `livekit-client/src/e2ee/worker/naluUtils.ts`.
