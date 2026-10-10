# Fixed noXa spoken announcements

These 34 English and 34 German WAVs are rendered during development. The shipped
client only plays bundled recordings. No model, TTS engine, dynamic text or
network TTS is packaged. Canonical text: tools/speech-lines.json.

Status, channel activity, administrative actions and poke use localized speech.
Mentions, keywords, direct/channel messages, announcements, whispers and PTT
retain short effects. Notification preferences and queue policy are unchanged.

## Selected voices and redistribution

- English: **EA60**, "Epic adventure narrator", the exact original synthetic
  adult fictional voice selected from the English previews.
- German: **AN06**, "Schelmischer Begleiter", the previously selected synthetic
  adult fictional voice. Its accepted recordings are preserved.
- References were designed with [Qwen3-TTS VoiceDesign](https://huggingface.co/Qwen/Qwen3-TTS-12Hz-1.7B-VoiceDesign).
  Complete fixed sentences use [Qwen3-TTS Base](https://huggingface.co/Qwen/Qwen3-TTS-12Hz-1.7B-Base)
  conditioned on each unmodified reference WAV and its complete actual transcript.
- Both upstream models and the offline [audio.cpp](https://github.com/0xShug0/audio.cpp)
  runtime are Apache-2.0. Q8 conversions use the pinned audio-cpp/audio.cpp-gguf
  revision in provenance.json. en-MODEL_CARD and de-MODEL_CARD record sources.
- Original noXa fixed recordings and references use this repository's MIT
  license. Model licenses identify authoring sources; they do not automatically
  license generated audio. public/noxa-audio-licenses.txt records this distinction.
  No actor recording or identifiable character imitation was used.

## Regeneration

Use an isolated environment with numpy==2.5.3 and the pinned Windows x64
audio.cpp v0.9.1 CPU runtime. Models and engines are never invoked by the build
or shipped client. Download the authoring model explicitly when needed:

```text
python tools/download-speech-models.py --languages en de
python tools/generate-reference-speech.py --language en --model .cache/noxa-speech-models/qwen3-tts-12hz-1.7b-base-q8_0_v2.gguf --runtime .cache/noxa-accent-local/runtime-fast/bin/audiocpp_cli.exe --output .cache/noxa-ea60-speech
python tools/generate-reference-speech.py --language de --model .cache/noxa-speech-models/qwen3-tts-12hz-1.7b-base-q8_0_v2.gguf --runtime .cache/noxa-accent-local/runtime-fast/bin/audiocpp_cli.exe --output .cache/noxa-an06-speech
```

The shared reference tool replaces generate-an06-speech.py. German remains its
default language. It verifies the pinned model, executable and selected reference
SHA-256, then uses task tts, the explicit language, voice_ref and reference_text.
Four CPU threads, a 192-token budget and a bounded timeout apply to each batch.
Seeds follow canonical event indices even for event subsets. Raw WAVs, native
manifests and authoring requests are retained outside production assets.

`--events` selects canonical IDs. `--prepare-only` writes requests without
inference. `--import-raw DIR --import-manifest FILE` only accepts raw recordings
whose recorded provider, reference, runtime, text, seed and hashes match.
Retries use a recorded `--seed-offset` and a fresh development directory.

Review complete transcripts before copying candidates and their measurements
into production metrics.json. ea60-content-review.json and an06-content-review.json
bind accepted automatic content checks to each shipped SHA-256. ASR can detect
omissions, repetition and added words but does not certify listening comfort,
voice identity or native accent. A clone can drift in tone or pronunciation.
No human listening review is claimed without recorded evidence.

The pronunciation_aliases.de map renders noXa as Noksa for German synthesis.
Canonical catalog text remains noXa; renderText records actual native input.
The exact full reference transcript is never replaced with a desired target line.

After reviewing and publishing the candidates:

```text
python tools/generate-speech.py --catalog-only
```

generate-speech.py now shares mastering and publishes the reviewed catalog only;
the retired Piper generation path cannot overwrite the selected English voice.
Neural synthesis can vary between runs despite seeds; shipped hashes identify
the accepted recordings.

## Audio contract and runtime policy

Mastering trims silence, removes DC and applies endpoint fades. Both selected
voices use 24 kHz mono PCM16, 40/60 ms leading/trailing padding and 6 ms fades,
targeting -31 dBFS RMS subject to the existing 0.115 peak ceiling. Every phrase
is 0.5–6 seconds with zero endpoints, absolute DC below 0.0001 and a unique hash.
Malformed, silent, clipped, overlong and token-truncated raw audio is rejected.
No pitch shifting, time stretch or repeated sample substitution is applied.
metrics.json records text, duration, rate, RMS, peak, seed and source/output hashes.

German UI/system locale selects German; English and unsupported locales select
English. The speech-language preference can override that choice. Missing selected
audio is logged once and omitted. Disabled, zero-volume or unavailable speech
stays silent under the existing notification gates, with no generated fallback.
