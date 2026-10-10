# Fixed noXa spoken announcements

These 34 English and 34 German WAVs are rendered during development. The shipped
client only loads and plays bundled files. No models, TTS engine, dynamic text
or network TTS is packaged. Source text: tools/speech-lines.json.

Status, channel activity, administrative actions and poke use localized speech.
Messages (mention, keyword, direct/channel messages, announcement and whisper)
and PTT intentionally retain short effects. German poke uses AN06 speech.

## Sources and redistribution

- English: Piper en_US-ljspeech-high, trained on the public-domain [LJ Speech dataset](https://keithito.com/LJ-Speech-Dataset/). en-MODEL_CARD identifies the source and license. The existing 24 English WAVs are preserved when adding the ten status/activity phrases.
- German: selected synthetic audition **AN06**, an original adult fictional voice designed with [Qwen3-TTS VoiceDesign](https://huggingface.co/Qwen/Qwen3-TTS-12Hz-1.7B-VoiceDesign). Complete fixed sentences use [Qwen3-TTS Base](https://huggingface.co/Qwen/Qwen3-TTS-12Hz-1.7B-Base) with the original reference WAV and its complete transcript. Both upstream models are Apache-2.0. de-MODEL_CARD records their license evidence and the reference-to-clone workflow.
- English comes from pinned [rhasspy/piper-voices](https://huggingface.co/rhasspy/piper-voices); German uses pinned Q8 GGUF conversions from [audio-cpp/audio.cpp-gguf](https://huggingface.co/audio-cpp/audio.cpp-gguf). provenance.json separately records each provider, revision, file and checksum.
- Development tools are [Piper](https://github.com/OHF-Voice/piper1-gpl) (GPL-3.0) for English and [audio.cpp](https://github.com/0xShug0/audio.cpp) (Apache-2.0) for German. Models and engines are not shipped. The original noXa German recordings and synthetic AN06 reference are distributed under this repository's MIT license. Apache-2.0 describes the authoring models, rather than an automatic license for generated audio. public/noxa-audio-licenses.txt records that distinction. No actor's recording or identifiable character voice was used as a reference.

## Regeneration

Use an isolated environment with piper-tts==1.4.2 and numpy==2.5.3 for English,
and numpy==2.5.3 plus the pinned Windows x64 audio.cpp v0.9.1 CPU runtime for
German. Download models manually; authoring is absent from build/start scripts.

```text
python tools/download-speech-models.py --languages en de
python tools/generate-an06-speech.py --model .cache/noxa-speech-models/qwen3-tts-12hz-1.7b-base-q8_0_v2.gguf --runtime .cache/noxa-accent-local/runtime-fast/bin/audiocpp_cli.exe --output .cache/noxa-an06-speech
```

`--events` selects canonical IDs. `--prepare-only` writes requests without
inference. `--import-raw DIR --import-manifest FILE` masters a separately generated
batch only when its recorded model, reference, runtime, text, seed and raw hashes
match. Retries use a recorded `--seed-offset` and a fresh output directory.

The German tool verifies the model, executable and original AN06 SHA-256. It
uses task `tts`, German language, per-request `voice_ref` and `reference_text`, four CPU
threads and a bounded 192-token budget. Each event's stable seed follows its
canonical index. Raw WAVs and authoring manifests are retained. Native inference
has a bounded timeout. The original reference and transcript are stored in
tools/voices; the reference's original VoiceDesign model, seed and prompt are
recorded in AN06-provenance.json.

The top-level provenance pronunciation_aliases.de map renders the brand spelling
`noXa` as `Noksa` for Qwen pronunciation. Canonical UI/catalog text stays `noXa`.
Native requests bind the actual rendered spelling; results.text retains the
canonical sentence and results.renderText records the native sentence. Import
validation compares today's rendered request, so the former unaliased test clip
cannot pass as the current brand pronunciation.

The German tool writes candidates and results.json in a development directory.
Review the complete transcript and delivery before copying selected WAVs and
merging text/hash/level metadata into speech metrics.json. ASR detects many
omissions, repetitions and added words; it does not certify listening comfort,
natural pronunciation or voice identity. Austrian coloring was model-directed,
not authenticated by a native listener. A clone may drift in voice, tone or
pronunciation. No human listening review should be claimed without evidence.

Preserve the existing 24 English WAVs when extending the pack:

```text
python tools/download-speech-models.py --languages en
python tools/generate-speech.py --models .cache/noxa-speech-models --languages en --events connection_connected connection_disconnected connection_reconnecting connection_failed disconnect_failed server_error poke buddy_online channel_watch stream_watch_started --skip-catalog
python tools/generate-speech.py --catalog-only
node tools/audio-inventory.mjs
```

`--catalog-only` requires complete metrics matching every canonical transcript.
Both providers share speech mastering code. The former German MLS context and
alignment renderer is retired. Neural output may vary between runs despite
recorded seeds; shipped hashes identify the accepted files.

## Audio contract and runtime policy

Mastering trims silence, removes DC and applies endpoint fades. It targets
-31 dBFS RMS subject to a 0.115 peak ceiling. English retains 22.05 kHz,
25 ms padding and 5 ms fades; German uses 24 kHz, 40/60 ms leading/trailing
padding and 6 ms fades. Every finished phrase is mono PCM16, 0.5–6 seconds,
with zero endpoints, absolute DC below 0.0001 and a unique hash. Malformed,
silent, clipped, overlong and suspected token-truncated raw audio is rejected.
No pitch shifting, time stretch or duplicated-voice editing is applied.
metrics.json records text, duration, rate, RMS, peak and output SHA-256.

German UI/system locale selects German; English and unsupported locales select
English. The separate speech-language setting can override that choice. Missing
selected audio is logged once and omitted. Actions with speech use speech only;
retired effects are excluded from the application bus and preview catalog.
Disabled, zero-volume or unavailable speech stays silent under the existing
notification and sound gates. No generated or other-language fallback exists.
