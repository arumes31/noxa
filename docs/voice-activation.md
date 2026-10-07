# Channel voice activation

Channel microphone transmission uses an AudioWorklet gate. It evaluates mean
absolute amplitude on each audio render block, using the existing configurable
VAD threshold. Main-thread timers only update warnings and presence; a busy or
background UI does not decide when microphone audio passes.

VAD keeps 100 ms of samples in a local ring buffer. When speech crosses the
threshold, the preceding quiet onset is included. This adds approximately
100 ms of delay in VAD mode. The 450 ms release hold follows the delayed speech
to preserve word endings. Push-to-talk and continuous transmission bypass the
ring and do not add this delay. Private calls retain their existing VAD path.

The raw microphone and the transmitted track have separate ownership. WebRTC
receives only the processed track. Mute/deafen immediately disable that track;
the worklet clears its ring and stops buffering while blocked. Unmute waits for
the worklet to acknowledge the new state. Microphone replacements begin silent
and activate only after they become current. Failed or stale replacements
dispose their own resources, and disconnect/reconnect disposes the old gate.

Silence remains silence on the existing WebRTC connection; no fake speech or
audible keepalive is generated. Network packet loss is independent of this fix.

Tests cover a minute of silence, quiet onsets at 44.1/48 kHz, release timing,
stereo, mode changes and mute privacy. Real-browser tests block the UI thread
while scheduled audio starts, check microphone replacement and teardown, and
exercise channel reconnect and device recovery. The worklet is bundled and
minified as a separate build asset.
