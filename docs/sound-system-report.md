# noXa static sound and speech rework

## Status

The implementation ships 33 effects and 18 English/German speech recordings. The production application only plays bundled files. The original 50 recordings replace the previous pack; the additional viewer-start cue uses the same licensed source collection. The design target is Hybrid Professional Console + Modern Desktop.

**Approved for implementation after the metallic, drum-like and instrumental sources were removed.** The user's final instruction was “ok implement.” The corrected assets are integrated into production event playback, settings previews and the application build. The initial two noise-based auditions were rejected as too similar and are not shipped. Approval to implement does not constitute a complete fatigue or platform listening review. This report does not certify natural pronunciation, long-session comfort, physical headphone/speaker routing, or final native multi-client behavior. Audition files are in `.cache/noxa-audition/`.

The checked-out repository is `https://github.com/arumes31/noxa.git`, on the existing `codex/fix-live-audio-and-members` branch. The remote was inspected, not renamed. No commit, push, release or deployment was performed by this task.

## Audio architecture

- `sound-catalog.js` defines every effect URL, category, duration, relative gain, priority and cooldown/concurrency policy. `speech-catalog.js` defines the localized full-sentence files and transcripts. Both are generated during authoring, not application startup or build.
- `sounds.js` coordinates one dedicated system-audio context, cached PCM buffers, language selection and previews. It preloads all short effects and only the selected language's speech. Previously selected language buffers may remain cached; other languages are not decoded proactively.
- `sound-engine.js` only decodes, schedules buffered playback, controls gain and routes output. No notification oscillators, frequency tables, TTS, pitch/rate changes, sound-design filters or generated fallbacks exist in the production frontend. Live microphone, codecs, voice processing, participant playback and screen-reader live regions remain separate.
- The engine admits at most four audible system sources, including speech. Replacing a source reserves its slot through a 3 ms stop fade. Nodes are disconnected on completion/cancellation; ended callbacks are delivered once. PTT replaces the previous PTT transition without waiting in a notification queue. Main.js applies microphone/voice state before requesting feedback.
- Effects are mono 48 kHz/16-bit PCM WAVs; speech is mono 22.05 kHz/16-bit PCM WAV. Every effect has its own finished file. Final effect ranges: PTT 25/35 ms; microphone/deafen 125–140 ms; own channel 165–275 ms; other-user movement 95–120 ms.
- Relative gain is 1: intended level differences are mastered into the assets. The asset peak ceiling is 0.115. Four system sources at 200% are conservatively bounded by a summed sample peak of 0.92. This does not bound unrelated live voice or other applications in the operating-system mixer.

## Viewer-start cue

A short, independently configurable sound plays for a publisher when another member starts watching the current camera or screen stream. The server detects the transition atomically: duplicate watches, catalog polling, failed requests and stopping stay silent. Resuming counts as a new start. Notifications contain no viewer identity and are discarded after the publication changes. Master mute, effects volume, per-event opt-out, DND and history suppression apply; a 250 ms cooldown bounds bursts.

## Events and speech ordering

The generated [complete inventory](audio-inventory.md) lists all effect and speech files, transcripts, measured durations, source call sites, categories, priorities and concurrency policies. It is generated with `node tools/audio-inventory.mjs`; source registries remain authoritative.

Important events use `playAlert`: the actual effect completion releases the speech sequence, followed by a 150 ms gap. If the effect is disabled or unavailable, eligible speech still plays. If speech is disabled, the effect still plays. Connection loss has an additional 1.2-second relevance delay; successful reconnect clears it. Reconnect failure speaks only on final exhaustion, never on each retry.

`SpeechQueue` stores at most three waiting announcements, expires entries after eight seconds, and allows only one speech clip at a time. It scopes 10-second speech deduplication to event/tab/connection generation and bounds that map to 128 entries. Effects retain their separate short cooldowns, so repeated rejected actions can still have feedback while duplicate speech is suppressed. Recovery and tab changes cancel stale speech. Higher priority live speech can replace obsolete lower-priority speech. Previews cannot displace pending/live announcements, change their output, or suppress an incoming live sentence.

Ban and kick use authoritative `ban` and `from_server` fields. Self-removal clears reconnect intent before teardown, preventing a generic disconnect/retry cascade. Channel removal has a distinct fixed sentence. Forced movement requires `by_client_id` different from the affected user and a changed positive destination. Voluntary channel actions do not speak. Permission speech requires a user-visible, explicit permission-rejection prefix. Explicit `server_shutdown` is supported by this repository; connection failure is never interpreted as shutdown. Reasons, user names, server names, destinations and message contents remain visual/accessibly announced interface text only.

The existing native Windows notification implementation uses `NIIF_NOSOUND`; all audible app feedback goes through the system-audio policy. Existing server terminal-event delivery and live voice code were inspected and retained.

## Settings and compatibility

- Existing `play_sounds` remains the total application-audio gate. The label now explicitly includes effects and speech. DND and replay suppression apply to both.
- New `effects_enabled` defaults true. `sound_volume` remains the effects volume; `spoken_messages` and `speech_volume` control speech independently. Existing 0–200 values are preserved and safely bounded.
- Existing `speech_connection` and `speech_admin` remain category gates. New `speech_removal` and `speech_permissions` add narrower controls. `speech_events` and `event_sounds` retain explicit per-event opt-outs; the kick notification-matrix row also gates self-removal speech.
- Settings version 10 adds these fields without resetting unrelated settings. Legacy pack IDs (`soft`, `bright`, `retro`, empty and `noxa`) resolve to the single displayed **noXa** pack. Old custom-synthesis settings stay retired.
- Pre-speech profiles (before version 8) with master audio off or effects volume zero do not gain enabled speech. Existing explicit speech preferences survive load/save. The repository's earlier migration for pre-version-1 nonfunctional sound flags is unchanged.
- Internal configuration keys, package identity, globals, paths and native IDs were retained. Existing `NOXA_*` environment keys and historical migration documentation are compatibility identifiers, not new display branding.

## Localization, previews and output

English and German recordings can be selected independently of the interface. The default follows the interface language, including system-language selection; unsupported interface languages deterministically select bundled English. A missing selected-language clip is omitted; there is no translation, TTS, fragment assembly or dynamic-text fallback. German uses the existing UI term “Channel.” The test clips use the exact noXa test sentences from the brief.

Settings provide individual, category and all-effects previews; individual, category and all-speech previews; and Stop preview. All use production files/playback and the unsaved settings draft. Explicit previews may bypass only master mute and replay/history; DND, disabled effects/speech and per-event opt-outs remain authoritative. Sequences follow actual completion. Cancel, page changes and closing stop playback without saving the draft. New controls have accessible labels and status feedback.

The shared system context follows the selected playback output when `AudioContext.setSinkId` is supported. Playback is withheld during routing. If the selected device is unavailable or routing unsupported, the documented fallback is the system default, potentially speakers. The settings page explicitly explains that behavior. If both selected and fallback routing fail, playback stays silent. The live voice output path is unchanged. Physical device behavior still requires native validation.

## Provenance and authoring

Effects are edited offline from pinned CC0 audio using `tools/master-sounds.py` and the single inventory `tools/effect-recipes.json`. The former procedural noise renderer is removed. Sources are Kenney Interface Sounds, Kenney UI Audio, paper/card placement and handling from Kenney Casino Audio, and Luckius Various Paper Sound Effects. The original library titles are provenance; no casino jingles, chips or dice are used. The source pages, archive hashes, source-file hashes, excerpts, weights and mastering choices are recorded in `assets/sounds/provenance.json`. Source licensing notices ship in `public/noxa-audio-licenses.txt`.

The final pack uses dry controls, paper contacts and short friction textures. All instrument-library samples, the lighter recording, glass and plucked elements used in the intermediate audition are removed. 24 affected cues were rebuilt after the user's correction; the remaining eight dry control cues are retained from the accepted replacement direction. No prior rejected generated-noise waveform remains. Source licenses permit modified redistribution under CC0; the noXa edits are dedicated under CC0 and authoring code remains MIT.

Offline work consists of onset trimming, downmixing, resampling, rumble/DC cleanup, optional high-frequency softening, contact editing, endpoint fades and level mastering. There is no oscillator, random-noise generator, pitch shift, reversal or melodic assembly. The ordinary build never downloads or authors audio.

Speech was rendered afresh from complete fixed sentences using development-only Piper 1.4.2, not converted from old speech files. The selected voices and licensing evidence are recorded in `assets/speech/README.md`, pinned model cards and `provenance.json`:

- English: en_US-ljspeech-high. Its [model card](https://huggingface.co/rhasspy/piper-voices/blob/main/en/en_US/ljspeech/high/MODEL_CARD) identifies the public-domain [LJ Speech dataset](https://keithito.com/LJ-Speech-Dataset/).
- German: de_DE-thorsten-medium. Its [model card](https://huggingface.co/rhasspy/piper-voices/blob/main/de/de_DE/thorsten/medium/MODEL_CARD) identifies Thorsten Voice under CC0.
- Pinned voice repository revision: `1162a9173d0ce503555aed757976b7a9912eae4c`. The model weights and GPL-licensed Piper tool are development dependencies only and are not included in the application. These generated fixed sentences contain no copied application sound branding.
- Authoring parameters: length scale 1.08; noise scale 0.55; noise width 0.7; 25 ms edge padding; 5 ms endpoint fades; target RMS -31 dBFS subject to 0.115 peak ceiling. These are applied only during authoring and are recorded in provenance.json.
- Effects and speech `metrics.json` record file hashes, durations and measured levels. Speech catalogs include the exact rendered transcripts. Tests check hashes, valid PCM, non-silence, safe peaks and registry/transcript consistency.

Authoring commands:

```text
uv --cache-dir .cache/uv venv .cache/noxa-audio-env --python 3.12
uv --cache-dir .cache/uv pip install --python .cache/noxa-audio-env/Scripts/python.exe piper-tts==1.4.2 numpy==2.5.3 scipy==1.17.1 soundfile==0.13.1
node tools/generate-sounds.mjs --download
python tools/download-speech-models.py
.cache/noxa-audio-env/Scripts/python.exe tools/generate-speech.py --models .cache/noxa-speech-models
node tools/audio-inventory.mjs
```

Installation, first run, ordinary builds and application startup do not run any generator or require models, services or keys. `npm run build` only bundles existing files and verifies them. Vite's [asset inlining setting](https://vite.dev/config/build-options.html#build-assetsinlinelimit) keeps all WAVs as discrete production assets. The [Web Audio specification](https://webaudio.github.io/web-audio-api/) defines the buffer lifecycle used for playback.

## Selected audio quick wins

Implemented selections 2, 3, 31, 32, 33, 34, 39, 51, 61 and 81:

- Playback and notification settings explain master mute, DND, disabled effects/announcements/events, zero volume, suspended playback and unavailable/fallback output. A rejected preview reports its cause.
- Voice, effect, speech and VAD sliders have synchronized editable percentage fields. Values remain in the settings draft until saved.
- Microphone testing and calibration use the current draft capture settings and the active channel's music profile. Only one check can run at a time; closing settings, changing pages or searching stops its tracks, loopback and audio context. A late permission response also releases its tracks.
- Ambient calibration displays a five-second countdown. Silent capture is identified separately from denied permission, absent hardware and a busy input device.
- Optional `duck_effects_while_speaking` reduces routine effects to 35% while a client in the active channel speaks. Priority 3+ alerts and prerecorded speech keep their configured volume. The option defaults off.
- `speech_language` accepts `interface`, `en` or `de`. It defaults to following the interface, preserving existing profiles; unsupported interface languages still use the English recordings.
- Failed asset loads leave the in-flight cache after completion so subsequent requests can retry. Concurrent requests still share one load; successful decoded assets stay cached.

Settings generation 10 adds these choices without renaming legacy keys or storage locations. All effects and announcements remain bundled static audio. This follow-up changes playback gain and controls, not the approved recordings.

## Verification

Final corrected pack validation (2026-09-17):

- `npm run lint`: passed, 72 frontend files checked.
- `npm run test:unit`: 117 tests passed (76 core, 11 UI, 2 tray, 3 stats, 25 sound/speech). Audio checks include provenance, excluded source families, shipped hashes, PCM format, levels, endpoints, queue sequencing, draft previews, routing and failure handling. Log: `.cache/noxa-unit-corrected.log`.
- `CI=1 npm run test:e2e -- --retries=0 --reporter=list`: all 147 Chromium browser tests passed in 3.1 minutes, including accessibility workflows. Log: `.cache/noxa-e2e-corrected.log`.
- Browser audio stress: 100 PTT on/off cycles; 50 joins, leaves and channel messages each; 50 mic on/off cycles; 30 own-channel switches. All 480 cues completed and released their nodes; all 18 speech files decoded. This tests playback lifecycle, not perceived fatigue.
- Browser workflows cover actual effect-end plus 150 ms sequencing, terminal ban without disconnect cascade, visual reasons, localized fixed speech, draft volume/DND, individual/all preview cancellation, output admission and safe maximum gain. The settings screenshot was visually inspected for clipping/readability.
- Client `go test ./...`: passed (cached), including settings migration/round trips and native notification tests.
- `wails build -o noxa-audio-review.exe`: Windows/amd64 production build passed in 20.769 seconds. Output: `client/build/bin/noxa-audio-review.exe` (19,061,760 bytes).
- `node tools/verify-audio-build.mjs`: all 50 exact WAVs and the licensing notice found in the production bundle; no obsolete or duplicate WAVs.
- Binary audit: all 50 complete source WAV byte sequences and the complete license notice were also found inside the built executable. Audio total: 2,129,020 bytes. Executable SHA-256: `0518952ca58456ea72dc9f4cf50b3f02f36b24a70a7f269c9b3b13d67497362b`. Record: `.cache/noxa-build-verification.json`.
- All 50 recordings differ byte-for-byte from the pack at HEAD. Final effects use 37 distinct source files from the four documented CC0 packages. All instrument/glass/lighter sources from the intermediate audition are absent from final recipes/provenance.
- Independent queue review identified four issues, all fixed with regressions: speech cooldown suppressing effects, previews displacing queued live speech, previews suppressing incoming speech, and rejected previews rerouting live speech.
- Earlier browser localization failures came from a reused Vite server exposing two module identities. The complete final suite used a fresh server and passed without weakened assertions.

### Selected quick-win verification

Follow-up validation (2026-09-17):

- `npm run lint`: passed, 75 frontend files checked.
- `npm run test:unit`: 121 tests passed (76 core, 11 UI, 2 tray, 3 stats, 29 sound/speech). New cases cover retry after failed asset loads, concurrent-load deduplication, optional effect ducking, blocking reasons and independent announcement language. Log: `.cache/noxa-quick-wins-unit.log`.
- Client `go test ./...`: passed, including new language/ducking round trips, generation-9 profile compatibility and rejecting an invalid announcement language without mutating settings.
- Fresh Chromium focused run: all 15 audio quick-win tests passed. Coverage includes live draft capture constraints, active music profiles, pending-permission cleanup, mutual exclusion, calibration countdown, silence versus denial, percentage synchronization/clamping/persistence, silent-output explanations, actual German asset playback with an English interface, and Cancel behavior. Log: `.cache/noxa-quick-wins-browser-final.log`.
- Notification controls and calibration screenshots were visually inspected; percentage input, language selection and countdown were readable without overlapping controls.
- Full fresh Chromium suite: all 162 tests passed without retries in 3.1 minutes. Log: `.cache/noxa-quick-wins-e2e.log`.
- `wails build -o noxa-audio-review.exe`: Windows/amd64 build passed in 28.665 seconds. The updated executable is 19,072,512 bytes; SHA-256 `f692cb23d06c373238674af36b47a6154941032b53b5715ddf26c0b8f30a921e`. All 50 complete WAV payloads (2,129,020 bytes) and the complete audio license notice were verified inside the executable. Record: `.cache/noxa-quick-wins-build-verification.json`.

### Still requiring human/platform review

The user approved implementation of the corrected pack. A complete cue-by-cue and long-session listening review has not been recorded. The effects and every EN/DE phrase still need that review for comfort, naturalness, pronunciation and consistent noXa delivery. Automated repetition and decoding are not a substitute. Final native multi-client administrative workflows, physical output switching/loss, offline playback in the packaged WebView, macOS/Linux WebViews and long-session fatigue remain unverified unless later results below explicitly say otherwise.
