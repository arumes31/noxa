# noXa sound pack

Hybrid Professional Console + Modern Desktop. 32 finished mono PCM WAVs, 48 kHz/16 bit, edited from licensed source files. These replace the two rejected noise-based auditions completely. No old effect is retained as a fallback.

The pack combines dry button/switch contacts, paper handling and short friction textures. Controls, movement, messages and alerts have different source combinations. The user's follow-up explicitly excludes metallic, drum-like and instrumental sounds. All Robin Lamb/VCSL/VSCO instrument samples, the lighter recording, glass and plucked elements were removed from every recipe and finished file. Do not infer recording techniques for the Kenney interface sources.

## Sources and redistribution

All source audio is CC0-1.0, permitting modification and redistribution, including commercial use. noXa's edits preserve that dedication. Authoring code remains under the repository MIT license. Source credits are retained for provenance:

- [Kenney — Interface Sounds 1.0](https://kenney.nl/assets/interface-sounds): selected click, switch, toggle, select and scratch assets. The archive's license is retained in `client/frontend/public/noxa-audio-licenses.txt`, which is also included in the application bundle.
- [Kenney — UI Audio 1.0](https://kenney.nl/assets/ui-audio): dry button and switch contacts.
- [Kenney — Casino Audio 1.1](https://kenney.nl/assets/casino-audio): paper/card placement and handling foley only. The original library title is retained for provenance. No chips, dice, casino jingles or reward cues are used.
- [Luckius — Various Paper Sound Effects](https://opengameart.org/content/various-paper-sound-effects): short excerpts of paper handling, crumpling and tearing.

`provenance.json` records source pages, authors, archive hashes, exact source filenames and hashes, and every per-event edit. Original filenames and third-party names are provenance, not noXa display branding. Original archives are development inputs, not application dependencies.

## Offline authoring

`tools/effect-recipes.json` is the single authoring inventory. `tools/master-sounds.py` decodes, trims, downmixes, resamples, removes DC/rumble, optionally softens high frequencies, edits contacts together, fades endpoints and masters levels. It does not synthesize noise, oscillators, pitches or melodies. All processing is baked into the checked-in WAV files.

Use Python 3.12 with numpy 2.5.3, scipy 1.17.1 and soundfile 0.13.1, then `node tools/generate-sounds.mjs --download`. Archives are fetched only by that explicit development command and verified against pinned SHA-256 values. Subsequent runs can omit `--download`; normal builds never run authoring tools.

PTT and frequent events stay quieter than alerts. Registry gain is 1; level differences are mastered into the files. `metrics.json` contains noXa titles, duration, peak, RMS, DC and SHA-256. Maximum asset peak is 0.112 (below the engine's 0.115 ceiling); four sources at 200% retain sample headroom.

The user approved implementation of this corrected pack after requesting removal of metallic/drum/instrument sounds. These files are integrated into production event playback and settings previews. This does not establish a full 32-event fatigue review. Automated measurements cannot establish comfort or perceptual distinction. See `docs/sound-system-report.md` for review status.
