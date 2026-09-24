# Positional voice input

Enable **Settings → Playback → Positional voice audio**, then save settings. The adjacent button copies the input path. By default this is `%APPDATA%/noxa/positional/input.json` on Windows. Each native profile uses its own settings directory. No network listener, game-process inspection, or automatic capture of location is involved.

A game adapter writes UTF-8 JSON to this file, replacing it atomically at least twice per second. The client reads at 4 Hz, only while enabled and connected to voice. Input older than two seconds is rejected; remote positions expire after three seconds. Ordinary audio is restored when either participant has no fresh matching context. Both participants need positional audio enabled and an adapter in the same coordinate system.

```json
{"x":3,"y":1.7,"z":-2,"context":"game/map/session","forward":[0,0,-1],"up":[0,1,0]}
```

Coordinates use meters, a right-handed coordinate system, Y up, and negative Z forward. `forward` and `up` are approximately orthogonal unit vectors describing the local listener. `context` identifies the shared world/session; it must match exactly between peers and contain at most 128 UTF-8 bytes. Avoid names, account identifiers or secrets in this value: it is sent to voice-channel peers. Positions must be finite and within ±1,000,000 meters. The entire file is limited to 4 KiB. Files and source orientation stay local; only position, context and the current channel ID are relayed.

The panner sits after each speaker's gain/mute and before the existing master/deafen output. Screen-share audio uses its existing independent path. Spatialization uses HRTF and inverse distance attenuation, a 1 m reference, a 100 m distance limit and a 0.25 rolloff. See [PannerNode](https://developer.mozilla.org/en-US/docs/Web/API/PannerNode) and [AudioListener](https://developer.mozilla.org/en-US/docs/Web/API/AudioListener).

`tools/positional-demo.py --path PATH --x -3` writes a stationary test listener/publisher until interrupted. Run clients with different positions but matching context to exercise placement. This is an adapter interface and test source; support for any particular game requires that game's adapter. No claim of automatic TeamSpeak/Mumble plugin compatibility is made.
