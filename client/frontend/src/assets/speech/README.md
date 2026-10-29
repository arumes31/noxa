# Fixed noXa spoken announcements

These 35 English and 36 German WAVs are rendered during development. The shipped
client only plays bundled recordings. No model, TTS engine, dynamic text or
network TTS is packaged. Canonical text: tools/speech-lines.json.

Status, channel activity, administrative actions and poke use localized speech.
Mentions, keywords, direct/channel messages, announcements, whispers and PTT
retain short effects. Notification preferences and queue policy are unchanged.

## Voice

Both languages use **ST13 "Eisbrecher"**, chosen from the ST01–ST20 auditions:
a synthetic adult voice with a very low pitch (median about 83 Hz) and a calm
pace. ST13 is a weighted blend of the Supertonic 3 preset styles, defined in
tools/voices/ST13.json (M5 × 1.25, M1 × −0.25, speed 0.97, 8 flow steps). No
actor recording or identifiable character imitation was used. The earlier EA60
(English) and AN06 (German) recordings are retired.

### German closing messages

The German client has two closing messages, chosen by the user on 2026-10-24
from 40 daytime and 40 night auditions in ten voices:

| Clip | Shown | Text | Voice |
| --- | --- | --- | --- |
| `client_closing` | 05:00–19:59 | noXa verabschiedet sich. Bis gleich. | F5 (audition T07) |
| `client_closing_night` | 20:00–04:59 | Gute Nacht, schlaf gut und träum süß. | F1 (audition N30) |

F5 and F1 are unblended Supertonic 3 presets (tools/voices/F5.json, F1.json)
with the same model, runtime and settings as ST13. provenance.json assigns them
under `authoring.de.clip_voices`, so `render` reuses them. The night clip is a
variant of `client_closing` (`SPEECH_VARIANTS` in speech-queue.js): it shares
its priority and settings switch, and English keeps one closing line. Whisper
hears "Nocksa" in the F5 clip as "Nuxer", so that clip is recorded as
`accepted-user-listening-review`: the user chose this exact recording by ear.

- Model: [Supertonic 3](https://huggingface.co/Supertone/supertonic-3), INT8
  conversion from the sherpa-onnx `tts-models` release, OpenRAIL-M weights.
- Runtime: [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) v1.13.8 CLI,
  Apache-2.0. Both are pinned by SHA-256 in tools/voices/ST13.json.
- Original noXa recordings use this repository's MIT license; model licenses
  identify authoring sources. public/noxa-audio-licenses.txt records this.

## Pronunciation

Supertonic reads characters directly, without a pronunciation dictionary, so
German text follows German spelling rules. provenance.json maps words to the
spelling the model receives (`renderText` in metrics.json); the catalog text
and the interface keep the normal wording:

| Language | Written | Rendered |
| --- | --- | --- |
| en | noXa | Noksa |
| de | noXa | Nocksa |
| de | Server | Sörwa |
| de | Serveraktion | Sörwaraktion |
| de | Channel | Tschännel |
| de | online | onlein |
| de | Ton | Tohn |
| de | angestupst | angeschtupst |

Sörwa, Sörwaraktion, Tschännel and onlein were chosen by listening to
alternatives; "Stream" and "gebannt" are rendered as written. Tohn,
angeschtupst and Nocksa fix clips that Whisper repeatedly heard as "Tun",
"angestopst" and "Nuxa"; they still need a listening check.

## Regeneration

Download the pinned model archive and the sherpa-onnx runtime listed in
tools/voices/ST13.json, plus the sherpa-onnx Whisper small model for the
content check, then:

```text
python tools/generate-supertonic-speech.py render --model-dir <model dir> --tts <sherpa-onnx-offline-tts> --out .cache/noxa-st13-speech
python tools/generate-supertonic-speech.py review --out .cache/noxa-st13-speech --asr <sherpa-onnx-offline> --whisper-dir <whisper dir>
python tools/generate-supertonic-speech.py publish --out .cache/noxa-st13-speech
python tools/generate-speech.py --catalog-only
```

render verifies every model file hash, builds the blended style, applies the
respellings and masters each clip with the shared contract in
generate-speech.py. review transcribes every clip with Whisper and accepts it
only when the transcript matches the intended text within one letter (spacing
and punctuation may differ; the edit count is recorded). Re-render rejected clips with `render --events ...` and review again;
publish refuses unless all 71 clips are accepted, either by the content check or, for a misheard name only, by a recorded listening approval. Synthesis is not
bit-reproducible, so the shipped hashes identify the accepted recordings.
st13-content-review.json binds each accepted check to its WAV hash. ASR checks
completeness, not listening comfort, timbre or accent.

## Audio contract and runtime policy

Mastering trims silence, removes DC and applies endpoint fades. Both languages
use 24 kHz mono PCM16, 40/60 ms leading/trailing padding and 6 ms fades,
targeting -31 dBFS RMS subject to the existing 0.115 peak ceiling. Every phrase
is 0.5–6 seconds with zero endpoints, absolute DC below 0.0001 and a unique hash.
Malformed, silent, clipped and overlong audio is rejected.
Only the two TROLL1 update clips use the documented offline pitch/growl treatment; other recordings remain unprocessed in pitch and timing. No repeated sample substitution is applied.
metrics.json records text, render text, duration, rate, RMS, peak and source/output hashes.

German UI/system locale selects German; English and unsupported locales select
English. The speech-language preference can override that choice. Missing selected
audio is logged once and omitted. Disabled, zero-volume or unavailable speech
stays silent under the existing notification gates, with no generated fallback.

### Update announcement

`client_updating` uses the synthetic **TROLL1 Höhlentroll** voice in both
languages: the ST13 preset blend, pitch ratio 0.86 and mild 35 Hz amplitude
modulation (10% depth). The treatment runs only during offline authoring and
is recorded in tools/voices/TROLL1.json and each clip's metrics. Final output
uses the same quiet mastering contract. No actor or existing character is imitated.

Playback occurs only immediately before an update restart with a confirmed
active server session. Offline startup, mute/deafen, DND and disabled speech
remain silent. Playback completion is bounded so missing audio hardware cannot
block an update. The client still reconnects using its protected native handoff.
