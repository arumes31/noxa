# VOICX static sound and speech redesign

Implemented on codex/audio-update. Runtime audio is a player: 32 finished sound-effect WAVs and 18 finished speech WAVs. All creative synthesis and TTS happens in development tools. There is no runtime oscillator, noise generator, pitch transformation, speech synthesis, TTS service, model download or generated fallback.

**Verification status:** automated checks and the Windows production build passed. Final native multi-client UI testing was blocked by a Computer Use app-approval timeout. Subjective headphone/speaker listening and cross-platform native verification remain incomplete. Those acceptance criteria are not certified by this report.

## Architecture and event routing

- tools/generate-sounds.mjs deterministically renders dry, fixed-filter noise contacts, taps and short console feedback into mono 48 kHz/16-bit PCM files. No pitched oscillators, sweeps, melodies or reverb remain in the recipes. Each event has its own finished file; channel join is 185 ms and PTT is 28/34 ms.
- tools/generate-speech.py uses a development-only Piper environment to render the fixed text in tools/speech-lines.json. The shipped files are mono 22.05 kHz/16-bit PCM. No engine or model weights ship with VOICX.
- sound-catalog.js and speech-catalog.js centralize static URLs and metadata. sounds.js assembles one catalog for one SoundEngine/AudioContext. All 50 assets are eagerly decoded and cached. Runtime work is selecting a buffer, applying gain, routing and playing at its original rate.
- SpeechQueue accepts only fixed event IDs, selects English/German, serializes speech and rechecks policy before playback. Reasons, names and message content cannot be passed as spoken text.
- Main event handlers preserve channel, control, connection and notification routing. PTT applies voice state before scheduling feedback. The voice processing/capture pipeline remains separate from system audio.
- Kick/ban use distinct effects, then fixed speech after 350 ms from event dispatch (roughly 130–195 ms after the warning file ends). Only self-removal speaks; server and channel removal have different recordings. A forced move requires a different by_client_id. Connection loss waits 1.2 seconds and successful recovery cancels obsolete connection speech. Only final retry exhaustion speaks reconnect_failed.
- Permission errors use an explicit permission-error prefix to choose a fixed clip; their detail remains visual. A semantic server_shutdown event selects its fixed clip. Servers without that new event produce ordinary connection-loss behavior.
- The server attempts a direct terminal event before closing a kicked connection and announces graceful shutdown. Each write has a 150 ms deadline and skips an already-busy writer, so delivery is best effort and teardown stays bounded. Existing observer broadcasts remain. This adds a semantic event, not dynamic audio data.

## Removed implementation

Deleted channel_join.mp3, old CUES/PACKS and frequency-array synthesis, soft/bright/retro oscillator packs, beep/play/playChannelJoin helpers, custom-beep settings and BeepSpec bindings, obsolete imports and CSS. The earlier experimental generated resonant/sweeping recipes were also replaced in full. Windows visual notifications now use NIIF_NOSOUND rather than allowing an extra OS sound outside the selected volume/device.

## Effects inventory

Files are in client/frontend/src/assets/sounds/.

| Event | Filename | Duration | Priority | Design description |
| --- | --- | --- | --- | --- |
| connection_connected | connection_connected.wav | 210 ms | Attention | Soft console contact with a short full confirmation body |
| connection_reconnected | connection_reconnected.wav | 135 ms | Attention | Shorter light contact with a compact settled body |
| connection_disconnected | connection_disconnected.wav | 160 ms | Attention | Single damped closure with a rounded soft attack |
| connection_lost | connection_lost.wav | 225 ms | Warning | Interrupted coarse contact and a muted second stop |
| connection_reconnecting | connection_reconnecting.wav | 110 ms | Control / medium | Quiet single neutral status contact |
| connection_failed | connection_failed.wav | 180 ms | Warning | Dry rejected contact with a dense low body |
| server_error | server_error.wav | 145 ms | Warning | Two compact dry refusal ticks |
| own_channel_join | own_channel_join.wav | 185 ms | Attention | Soft voice-path contact with a short rounded confirmation tail |
| own_channel_switch | own_channel_switch.wav | 150 ms | Attention | Muted transition contact followed by a small firmer contact |
| own_channel_leave | own_channel_leave.wav | 120 ms | Attention | Single felt-damped closure with a short final decay |
| user_join | user_join.wav | 90 ms | Low | Quiet clean presence tick |
| user_leave | user_leave.wav | 95 ms | Low | Related presence tick with a softer damped edge |
| user_move_in | user_move_in.wav | 130 ms | Low | Small paired contacts with a clean second edge |
| user_move_out | user_move_out.wav | 135 ms | Low | Related paired contacts ending in a damped edge |
| mic_on | mic_on.wav | 70 ms | Control / medium | Small clean control-surface contact |
| mic_off | mic_off.wav | 75 ms | Control / medium | Muted control-surface release |
| deafen_on | deafen_on.wav | 120 ms | Control / medium | Cushioned double contact with dark filtering |
| deafen_off | deafen_off.wav | 125 ms | Control / medium | Matching double contact with a clearer open edge |
| ptt_on | ptt_on.wav | 28 ms | Control / medium | Tiny dry talkback contact |
| ptt_off | ptt_off.wav | 34 ms | Control / medium | Tiny damped talkback release |
| mention | mention.wav | 165 ms | Attention | Defined single desktop tap with a brief supporting body |
| keyword | keyword.wav | 120 ms | Control / medium | Compact textured tap with a softened attack |
| dm | dm.wav | 185 ms | Control / medium | Two close dry desk contacts |
| channel_message | channel_message.wav | 85 ms | Low | Very quiet single muted tick |
| whisper | whisper.wav | 140 ms | Attention | Close dry soft contact and a tiny adjacent contact |
| poke | poke.wav | 115 ms | Attention | One firm controlled physical tap |
| join_leave | join_leave.wav | 100 ms | Low | Small neutral presence contact |
| buddy_online | buddy_online.wav | 150 ms | Control / medium | Warmer, slightly fuller presence contact |
| kick | kick.wav | 155 ms | Warning | Firm low contact with a short abrupt cushioned stop |
| ban | ban.wav | 220 ms | Warning | Low dry stop followed by a subdued final contact |
| announcement | announcement.wav | 240 ms | Warning | Broader clean contact with a brief dry body |
| channel_watch | channel_watch.wav | 150 ms | Low | Quiet separated pair of damped status ticks |

## Fixed speech inventory

Each filename exists in assets/speech/en/ and assets/speech/de/. The queue ranks ban highest, followed by kick/shutdown/final failure, connection loss, and administrative move/permission failure. All durations below are measured from the shipped files.

| Event | Filename per language | English | German | English text | German text |
| --- | --- | --- | --- | --- | --- |
| banned | banned.wav | 1.54 s | 1.32 s | You were banned from the server. | Du wurdest vom Server gebannt. |
| kicked | kicked.wav | 1.38 s | 1.57 s | You were kicked from the server. | Du wurdest vom Server entfernt. |
| kicked_channel | kicked_channel.wav | 1.56 s | 1.77 s | You were removed from the channel. | Du wurdest aus dem Channel entfernt. |
| connection_lost | connection_lost.wav | 2.22 s | 2.09 s | Connection to the server was lost. | Die Verbindung zum Server wurde unterbrochen. |
| reconnect_failed | reconnect_failed.wav | 2.08 s | 2.78 s | Unable to reconnect to the server. | Die Verbindung zum Server konnte nicht wiederhergestellt werden. |
| permission_denied | permission_denied.wav | 2.47 s | 2.05 s | You do not have permission to perform this action. | Du hast keine Berechtigung für diese Aktion. |
| moved_by_admin | moved_by_admin.wav | 1.81 s | 1.99 s | You were moved to another channel. | Du wurdest in einen anderen Channel verschoben. |
| server_shutdown | server_shutdown.wav | 1.86 s | 1.50 s | The server is shutting down. | Der Server wird heruntergefahren. |
| test | test.wav | 2.72 s | 2.61 s | VOICX spoken notifications are enabled. | Die gesprochenen VOICX-Benachrichtigungen sind aktiviert. |

Speech data sources and exact model revision are documented in assets/speech/README.md, model cards and provenance.json. English uses the public-domain [LJ Speech dataset](https://keithito.com/LJ-Speech-Dataset/); German uses [Thorsten Voice](https://github.com/thorstenMueller/Thorsten-Voice), whose model card identifies CC0. [Piper](https://github.com/OHF-Voice/piper1-gpl) is a development-only tool. Effects are original and MIT licensed under the repository license.

## Settings and previews

Settings version 8 replaces legacy pack IDs with voicx. Master sound enablement, volume, per-event choices, notification matrix, DND and replay suppression remain. The new ban effect inherits the old kick preference during migration. Old custom_sounds JSON is ignored and omitted on subsequent save.

Spoken messages have an enable switch, independent 0–200% volume, connection-problem and administrative-action switches, plus speech_events support for individual persisted choices. New defaults enable speech; explicit false/zero values survive load/save. UI language selects German or English; unsupported languages deliberately use English. A missing selected-language recording is omitted, never synthesized or replaced with another language.

Settings provide individual, category and complete-effect previews; a static spoken-message test; and Stop preview. Previews use draft volume/output settings without saving them. Explicit previews may bypass master mute/history, while DND, disabled events and the speech toggle remain authoritative. Closing or changing pages cancels previews; changing server tabs cancels stale speech. Saving settings reconciles pending speech with the new policy.

## Gain, burst protection and lifecycle

Each asset has a maximum sample peak of 0.115. A shared ceiling of four audible system sources at 200% gives a conservative summed sample peak of 0.92. Speech uses the same ceiling and bus. This bound concerns system audio; it is not a claim about arbitrary live voice/music mixed by the operating system. Runtime gain never manufactures variants.

Movement events share a 180 ms cooldown; low-priority repeats use 250 ms; reconnecting is limited to 5 seconds. Identical effects and opposite PTT transitions replace their previous instance. Replacement uses a 3 ms gain fade and reserves the next start until the retiring source stops, avoiding stacked tails. Higher-priority events can replace lower-priority effects. Completed fades are reclaimed even if main-thread activity delays ended callbacks.

Speech has one playing clip, at most three pending clips, 10-second per-event deduplication and an 8-second queue expiry. Higher-priority speech removes obsolete pending items and can interrupt a lower-priority clip; lower-priority speech is rejected during a higher-priority announcement.

One context handles startup, gestures/focus/visibility resume, selected output changes and device changes. Missing assets/context/decoder/output failures are logged once and cannot invoke fallback generation. A cold or suspended live event is skipped rather than replayed late. Explicit previews await preparation. setSinkId is capability checked and falls back to system default when unavailable. The [Web Audio specification](https://webaudio.github.io/web-audio-api/) governs that playback lifecycle. Native Windows notification flags follow [NOTIFYICONDATAW](https://learn.microsoft.com/en-us/windows/win32/api/shellapi/ns-shellapi-notifyicondataw).

Sources disconnect on completion; pending preview timers, speech timers, listeners and buffers are released on cancellation/shutdown. A failed asset stays failed for the session; reloading retries it.

## Audio and performance measurements

- Effects: 418,720 bytes; speech: 1,558,290 bytes.
- Maximum effect sample peak: 0.11194; maximum absolute effect DC mean: 0.000000308.
- Highest whole-cue energy above 2 kHz: 5.99% (poke). Mono files avoid interchannel phase/width problems. These measurements cannot prove perceptual comfort or recognizability.
- Generator and tests validate finite samples, PCM headers, channel count/rate, duration, exact silent endpoints, peak/DC and effect RMS target tolerance. Speech generation trims excessive leading/trailing silence, validates duration/file size and records hashes.
- 1,000 real Chromium PTT submissions: p95 0.20 ms, maximum 3.00 ms, total submission CPU/wall timing 90.6 ms. Reported context base latency 10 ms; preparation 437.5 ms. This is one local benchmark, not end-to-end microphone latency or an OS CPU profile. After settling: zero active/retiring sources.
- Mandatory repetition browser test played PTT on/off 100 times each, user join/leave 50 each, message 50, mic on/off 50 each and channel switch 30: 480 completed submissions, with no leftover active/retiring nodes. It also decoded every spoken file.

## Commands and results

Repeated invocations of an identical command are grouped; transient failures and their resolution are retained. Logs are in temp/sound-*.log.

| Command / scope | Result |
| --- | --- |
| node tools/generate-sounds.mjs | Final: 32 cues generated and validated. Earlier sandbox mkdir failure rerun with authorized access; first revised-contact pass failed RMS validation, fixed with offline peak shaping. |
| uv venv temp/speech-env --python 3.12; uv pip install --python temp/speech-env/Scripts/python.exe piper-tts==1.4.2 numpy | Passed; isolated development environment. |
| python temp/sound-runtime/download-models.py | Downloaded two build-only models/configurations/cards at a recorded revision. |
| python tools/generate-speech.py --models temp/sound-runtime/speech-models | Passed, 18 measured static speech files. |
| node --test unit/sounds.test.mjs | Initial expected missing-module failure before implementation; subsequent passes. |
| npm run test:unit:sounds | 16 tests pass in final full suites. Initial new guard matched a generator comment; fixed to inspect prohibited runtime APIs/imports. |
| npm run lint | Passed, including in final quality. |
| npm run test:unit | Passed in final quality and npm test: core 70, UI 11, tray 2, stats 3, sound/speech 16. |
| npm run test:a11y | 6 passed in final quality. Earlier failures exposed fixture media-device capability handling and speech timer binding; both fixed. |
| npm run build | Final Vite production build passed. Initial sandbox dist-write failure rerun with authorized access. |
| npm run quality | Final PASS: lint, all unit groups, six accessibility workflows and production frontend build. Log: sound-quality-static-final.log. |
| npx playwright test tests/sounds.spec.js | Earlier decode/headroom tests passed; final expanded tests run through npm test. |
| npx playwright test tests/workflows.spec.js --grep 'grouped, distinct action sounds&#124;scopes connection failures' | Passed after updating oscillator-based instrumentation to observe the actual buffer player. |
| npx playwright test tests/workflows.spec.js --grep 'grouped, distinct action sounds&#124;scopes connection failures&#124;@a11y' | Earlier 7 passed. |
| npx playwright test tests/workflows.spec.js --grep 'sound previews use draft' | Passed. |
| npx playwright test tests/workflows.spec.js --grep 'starts voice, plays the original' | Passed after replacing the obsolete MP3 assertion. |
| npx playwright test tests/workflows.spec.js --grep 'static speech&#124;sound previews use draft&#124;@a11y' | 7 passed, including real German ban speech and English draft-volume preview. |
| npx playwright test tests/sounds.spec.js tests/workflows.spec.js --grep 'original sound set&#124;four simultaneous&#124;grouped, distinct action sounds&#124;scopes connection failures&#124;starts voice, plays the original&#124;sound previews use draft' | Earlier six focused checks passed. |
| npm run test:e2e | Before speech: 119/120 first pass; old MP3 assertion fixed; 120/120 rerun. |
| npm run test | Final PASS: unit groups plus 122/122 end-to-end tests, 3.8 minutes. Log: sound-test-static-full.log. |
| gofmt -w changed Go files; go test ./... (client) | Passed; final client run 14.711 s. Log: sound-go-speech.log. Earlier sandbox file/cache failure rerun with authorized access. |
| go test ./internal/server ./internal/broadcast | Final PASS, server 25.544 s. First run exposed an existing asynchronous permission-test race; test now waits for cache invalidation explicitly. |
| go build -o temp/sound-runtime/server.exe ./cmd/server | Passed for isolated runtime testing. |
| wails build -m -nosyncgomod -debug -o voicx-sound-review.exe | Earlier debug desktop build passed and used for native two-client checks. |
| wails build -m -nosyncgomod -o voicx-sound-production.exe | Final production Windows desktop build passed in 45.082 s. Log: sound-wails-production.log. |
| python temp/sound-runtime/analyze.py | Passed; measured final effect spectrum/DC/peaks and wrote audition-alphabetical.wav. |
| node temp/sound-runtime/benchmark.mjs | Passed; completed-fade cleanup confirmed under 1,000 real WebAudio PTT submissions. |
| powershell -NoProfile -File temp/sound-runtime/start-final.ps1 | Legacy PowerShell rejected AsHashtable; rerun in the current PowerShell runtime succeeded. |
| git diff --check | Passed after removing unrelated Wails-generated formatting/reordering. |

## Native and listening validation

Earlier native debug build: two isolated profiles connected to a local TLS server, joined Echo Test and appeared in voice together. Exercised PTT press/release, microphone on/off, deafen on/off, channel message/mention delivery, an actual permission denial, forced server loss, failed retries and successful automatic recovery after a brief outage. Server logs confirmed WebRTC sessions/audio tracks. There was a transient renegotiation warning, so this is not a claim of warning-free voice operation.

Those native checks preceded the final dry-contact assets and speech addition. The final production executable launched in English/German test profiles, but Computer Use approval timed out before UI validation. No claim is made that all final cues, administrative speech or final native multi-client workflows were manually heard. Build-time assets were decoded and played by Chromium tests; that is distinct from human listening.

Native physical output switching, headphones/laptop speakers, long-session subjective fatigue, speech pronunciation/neutrality, macOS and Linux WebViews remain unverified. WAV PCM was selected for conservative decoder compatibility; Windows Chromium/WebView2 builds were exercised, but cross-platform native support is not certified. setSinkId may be unavailable or permission-limited in some WebViews, in which case sound uses the default device and logs once. No TTS fallback exists.

## Significant files

- Added: tools/generate-sounds.mjs, tools/generate-speech.py, tools/speech-lines.json.
- Added: frontend sound-engine.js, sound-catalog.js, speech-queue.js, speech-catalog.js; 32 assets/sounds WAVs and metadata; 18 assets/speech WAVs, metrics, provenance, model cards and licensing/readme.
- Reworked: frontend sounds.js, main.js, notifications.js, settings-ui.js, tabs.js, style.css; removed unused chat-ui.js import.
- Settings/bindings: client/settings.go, settings_test.go, frontend/wailsjs/go/models.ts.
- Native notifications: client/notify_windows.go, notify_windows_test.go.
- Protocol delivery: internal/server/terminal_events.go, terminal_events_test.go, admin.go, tcp.go; groups_test.go timing fix.
- Tests/tooling: frontend/package.json, unit/sounds.test.mjs, unit/speech.test.mjs, tests/sounds.spec.js, tests/workflows.spec.js.
- Removed: frontend/src/assets/channel_join.mp3.
- Local review artifacts: docs/plans/2026-09-14-sound-redesign.md, this report, temp/sound-*.log, temp/sound-runtime/audition-alphabetical.wav and benchmark/signal-analysis JSON. The temporary TTS environment/model weights are not packaged or staged.

The Windows binary is client/build/bin/voicx-sound-production.exe. No deployment or publication was performed.
