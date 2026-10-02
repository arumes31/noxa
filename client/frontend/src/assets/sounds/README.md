# noXa sound pack

Hybrid Professional Console + Modern Desktop. 33 finished mono PCM WAVs, 48 kHz/16 bit. Most effects are edited from licensed source files; the poke uses the user-selected G05 recording. No old effect is retained as a fallback.

On 2026-10-02 the user selected G05 as the new poke: Microsoft David Desktop says “Wake up!”, followed by a double beep. This replaces the dry snap and is an explicit exception to the earlier non-tonal preference. The full 1.031875-second recording is retained, with uniform gain adjustment to a 0.112 peak for the existing four-source/200% headroom budget. Its measured RMS is about -33 dBFS, roughly 7 dB above the previous poke. It remains one static poke effect, controlled by the existing poke/effects settings in every UI language.

The pack combines dry button/switch contacts, paper handling and short friction textures. Controls, movement, messages and alerts have different source combinations. The user's follow-up explicitly excludes metallic, drum-like and instrumental sounds. All Robin Lamb/VCSL/VSCO instrument samples, the lighter recording, glass and plucked elements were removed from every recipe and finished file. Do not infer recording techniques for the Kenney interface sources.

## Sources and redistribution

The sample libraries below are CC0-1.0; noXa's edits of those samples preserve that dedication. The G05 poke is separately identified as locally generated Windows System.Speech output using Microsoft David Desktop, plus an original synthesized cue; it is not described as part of those CC0 libraries. Authoring code remains under the repository MIT license. Source credits are retained for provenance:

- [Kenney — Interface Sounds 1.0](https://kenney.nl/assets/interface-sounds): selected click, switch, toggle, select and scratch assets. The archive's license is retained in `client/frontend/public/noxa-audio-licenses.txt`, which is also included in the application bundle.
- [Kenney — UI Audio 1.0](https://kenney.nl/assets/ui-audio): dry button and switch contacts.
- [Kenney — Casino Audio 1.1](https://kenney.nl/assets/casino-audio): paper/card placement and handling foley only. The original library title is retained for provenance. No chips, dice, casino jingles or reward cues are used.
- [Luckius — Various Paper Sound Effects](https://opengameart.org/content/various-paper-sound-effects): short excerpts of paper handling, crumpling and tearing.

`provenance.json` records source pages, authors, archive hashes, exact source filenames and hashes, and every per-event edit. Original filenames and third-party names are provenance, not noXa display branding. Original archives are development inputs, not application dependencies.

## Offline authoring

`tools/effect-recipes.json` is the single authoring inventory. `tools/master-sounds.py` decodes, trims, downmixes, resamples, removes DC/rumble, optionally softens high frequencies, edits contacts together, fades endpoints and masters levels. It does not synthesize noise, oscillators, pitches or melodies. All processing is baked into the checked-in WAV files. Its `recording` entry preserves the checked-in G05 poke byte-for-byte, checks its pinned SHA-256 and PCM limits, and fails rather than substituting the old poke if that recording is missing or changed. G05's original audition hash and applied gain are retained in the provenance.

Use Python 3.12 with numpy 2.5.3, scipy 1.17.1 and soundfile 0.13.1, then `node tools/generate-sounds.mjs --download`. Archives are fetched only by that explicit development command and verified against pinned SHA-256 values. Subsequent runs can omit `--download`; normal builds never run authoring tools.

PTT and frequent events stay quieter than alerts. Registry gain is 1; level differences are mastered into the files. `metrics.json` contains noXa titles, duration, peak, RMS, DC and SHA-256. Maximum asset peak is 0.112 (below the engine's 0.115 ceiling); four sources at 200% retain sample headroom.

The user approved implementation of this corrected pack after requesting removal of metallic/drum/instrument sounds. These files are integrated into production event playback and settings previews. This does not establish a full 32-event fatigue review. Automated measurements cannot establish comfort or perceptual distinction. See `docs/sound-system-report.md` for review status.
