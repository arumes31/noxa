# Video packet-loss recovery

Video recovery keeps transport sequence numbers separate from decoded-frame
state. Valid RTP padding from the active source is forwarded with translated
sequence numbers and timestamps, so it does not create artificial gaps at the
viewer. It does not start a stream, complete a layer switch, satisfy a resumed
watch, update VP8 frame references, or keep a silent simulcast layer active.
New layers receive a one-second opportunity to begin sending actual video.

Ingress reorders packets before checking frame dimensions. Contiguous packets
pass immediately; only packets waiting behind a gap are copied. Each track
holds at most 128 packets or 256 KiB and allows 200 ms for reordering or NACK
repair. A Pion read deadline also releases the final queued frame of an idle
screen. At timeout the reader first drains already queued RTX repairs: Pion's
separate RTX queue cannot wake a blocked primary RTP read. Unrepaired gaps
still invalidate reference dimensions and request a new keyframe. Track
ownership, publication permission, current codec and current limits are checked
again when buffered packets are released.

The frame-size inspector counts padding in sequence continuity without
discarding known dimensions. Genuine forward gaps still invalidate the
reference state. Old or duplicate packets are discarded before inspecting
their dimensions and cannot invalidate a newer valid keyframe. This matters
after outages, when retransmitted packets arrive behind current media.

The latest media sequence is tracked separately from the latest packet
sequence. Padding that overtakes a media packet must not reverse that frame's
7-bit VP8 PictureID arithmetic. Both packet sequence and frame-reference
continuity remain intact across simulcast switches.

A viewer's keyframe request repairs its currently forwarded source and also
requests the desired source when a quality switch is pending. Existing
per-source coalescing prevents repeated viewer feedback from flooding the
publisher. Once the switch completes, the retired source is no longer targeted.

The engine uses Pion's default interceptor chain with the NACK generator set
to ten retries per missing packet, at its default 100 ms interval. This limits
the obsolete-packet storms observed on sparse and idle layers after a server
freeze. New missing packets remain eligible, and the NACK/RTX responder is
unchanged. Pion's current 16-bit retry counter can wrap on unchanged idle loss
after about 109 minutes; this setting is not a permanent lifetime suppression.

Ingress byte limits, dimension limits, publication/watch ownership and final
write permission checks remain in force. Padding bytes also count toward
pacing and transport-feedback throughput.

Regression coverage includes actual-engine NACK generation and RTX responses,
padding serialization through Pion, sequence and PictureID wrap/reordering,
late oversized packets, frame-size enforcement and current-versus-desired
layer recovery. Native deployment measurements are recorded separately in
`tasks/deployment-finish.md`.
