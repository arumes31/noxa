# Pinned Pion congestion controller

Copied from `github.com/pion/interceptor v0.1.48` (`pkg/gcc`,
`internal/cc`, and `internal/ntp`), with its MIT license and upstream tests.
Internal import paths are adjusted for this repository.

This narrow local copy lets noXa correct the receive-rate calculator's
zero-duration samples without modifying the module cache or inventing packet
arrival timestamps. Equal TWCC timestamps and the first packet after an idle
window must retain the previous valid estimate until a positive time span is
available.

The Kalman noise estimator also uses the observed group period in seconds,
with a bounded 60-group minimum window. The upstream expression used a Go
duration's nanoseconds as an inverse rate, effectively freezing noise
adaptation. This follows [WebRTC's UpdateNoiseEstimate and UpdateMinFramePeriod](https://webrtc.googlesource.com/src/+/825f83b99ef588189d98317afed26f49c027ea44/modules/remote_bitrate_estimator/overuse_estimator.cc).

The additive AIMD increase also retains the current target when sparse input
puts its throughput cap below that target, matching [WebRTC AIMD](https://webrtc.googlesource.com/src/+/2e631f5c38819a37486ba2c945edd8d5258e95ce/modules/remote_bitrate_estimator/aimd_rate_control.cc).
The explicit decrease state still responds to congestion normally.

Remove the copy when an upstream release includes these fixes and the
regressions pass against that release.
