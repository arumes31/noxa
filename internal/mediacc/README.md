# Pinned Pion congestion controller

Copied from `github.com/pion/interceptor v0.1.48` (`pkg/gcc`,
`internal/cc`, and `internal/ntp`), with its MIT license and upstream tests.
Internal import paths are adjusted for this repository.

TWCC feedback consumes only the declared packet status count; unused symbols
in its final chunk are not packet losses. Receive deltas advance even for
packets evicted from send history, while only matched packets enter congestion
control. Sequence advancement uses processed statuses rather than matched
acknowledgments. This preserves timestamps and avoids invented losses after
delayed feedback. Regressions cover both chunk forms, real history eviction,
signed deltas, sequence wrap, padding, genuine loss and missing delta data.

This narrow local copy lets noXa correct the receive-rate calculator's
zero-duration samples without modifying the module cache or inventing packet
arrival timestamps. Equal TWCC timestamps retain the previous valid estimate.
If the receive window expires, the gap since its last packet supplies the
positive time span; sparse traffic cannot preserve a stale high rate forever.
Reordered arrivals older than the last accepted sample are ignored, matching
delay grouping, so delayed acknowledgments cannot move that gap backwards.

The additive AIMD increase also retains the current target when sparse input
puts its throughput cap below that target, matching [WebRTC AIMD](https://webrtc.googlesource.com/src/+/2e631f5c38819a37486ba2c945edd8d5258e95ce/modules/remote_bitrate_estimator/aimd_rate_control.cc).
The explicit decrease state still responds to congestion normally.

The rate controller retains its previous state across delay samples and uses
its injected clock for both increases and decreases. A decrease followed by
normal usage settles in hold before increasing again.

Arrival groups retain the first departure for membership and the latest
departure for delay measurements. Pairing the last arrival with the first
departure manufactured queue delay when burst widths changed. Burst grouping
is also capped at 100 ms, following [WebRTC inter-arrival grouping](https://webrtc.googlesource.com/src/+/825f83b99ef588189d98317afed26f49c027ea44/modules/remote_bitrate_estimator/inter_arrival.cc).
Regressions cover constant-delay unequal bursts, continuous packet trains,
and equal or reordered departures within a group.

Overuse persistence uses the sender-group interval (arrival interval minus
delay variation), so feedback batching cannot change the decision. The AIMD
decrease is capped at the current target, preventing congestion from raising
the pacing rate. Each correction has a regression that failed before the fix.

The active delay estimator now uses a 20-group trendline with a raw-delay
slope cap. The cap compares the minimum raw delay in the first and last four
groups, preventing the smoother from continuing to report queue growth after
a latency spike has drained. The algorithm is described in
[WebRTC's trendline estimator](https://webrtc.googlesource.com/src/+/8c371f2a9baf8fef9bf3c327a93a709bb8c1e000/modules/congestion_controller/goog_cc/trendline_estimator.cc).
The unused Kalman implementation and its implementation-specific tests are removed.

Feedback reports are processed in order by one worker: calculate throughput,
update the delay detector, then make one rate-control decision. This avoids
repeated reductions using the previous report's throughput. The detector
retains its congestion hypothesis while above threshold, and compares raw
estimates rather than the startup-scaled threshold value. Missing arrivals
never initialize a delay group.
After two seconds without valid received feedback, arrival grouping and the
delay detector restart while retaining the current bitrate, following
[WebRTC's stream timeout](https://webrtc.googlesource.com/src/+/refs/heads/main/modules/congestion_controller/goog_cc/delay_based_bwe.cc).
Loss-only reports do not keep stale delay history alive. Regressions verify
both resumed flat timing and renewed genuine queue growth after the reset.

noXa's video controller uses bounded AIMD backoff: reduce the effective paced
target by 15%, at most once per RTT (bounded to 200–1000 ms). This is a deliberate
departure from the original throughput-based decrease: sparse encoded content
does not measure link capacity. Both increase paths hold their current target
when throughput does not support an increase, and loss control can still
impose a tighter target. Persistent congestion can reach the configured
minimum; there is no fixed video bitrate floor or production pacing override.
The effective paced rate is stored separately from the delay target: a held
loss estimate must not erase elapsed-time recovery between loss updates.
Congestion decreases still use the lower of the delay target and paced rate.
When loss already limits pacing below the measured delivered rate (including
the backoff margin), delay control retains its independent recovery target for
one feedback RTT (200–1000 ms). Continued overuse then reduces the rate even
if loss holds its estimate: pacing gain must not mask a growing queue.
Regressions cover gain, sparse and reordered feedback and
renewed congestion as well as the observed post-pause interaction.

Voice and shared audio retain their separate bounded priority lane. Outbound
audio does not consume video TWCC sequence numbers or drive video bandwidth
estimation; inbound audio feedback remains enabled. Video and RTX share the
same gap-free, final-egress TWCC accounting.
RTP padding bytes count toward video pacing and TWCC throughput, including
empty-payload probes; packet size is the header, media payload, and padding.

Native sustained validation is tracked in `tasks/deployment-finish.md`; unit
coverage alone is not proof of playback reliability.

Remove the copy when an upstream release includes these fixes and the
regressions pass against that release.
