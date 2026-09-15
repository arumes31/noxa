# Fixed VOICX spoken announcements

These WAVs are rendered during development. The shipped client only loads and
plays them. No voice models, TTS engine, dynamic text or network TTS is packaged.
English and German each contain nine fixed phrases, including the preview and
separate channel/server removal messages. Source text: tools/speech-lines.json.

## Sources and redistribution

- English: Piper en_US-ljspeech-high, trained on the [LJ Speech dataset](https://keithito.com/LJ-Speech-Dataset/), whose recordings and transcripts are public domain. The accompanying en-MODEL_CARD identifies that source and license.
- German: Piper de_DE-thorsten-medium, based on [Thorsten Voice](https://github.com/thorstenMueller/Thorsten-Voice), CC0-1.0. See de-MODEL_CARD and the project's CC0 license.
- Models came from [rhasspy/piper-voices](https://huggingface.co/rhasspy/piper-voices). Exact source revision and model paths are recorded in provenance.json.
- The build tool uses [Piper](https://github.com/OHF-Voice/piper1-gpl) (GPL-3.0). Piper and model weights are not redistributed in VOICX. These are generated recordings of VOICX's fixed phrases, not copies of another application's notification recordings.

## Regeneration

Use an isolated Python environment with piper-tts==1.4.2 and numpy. Download the
models and configurations at the recorded revision as en.onnx / de.onnx with
matching .onnx.json files, MODEL_CARD files and provenance.json. Run:

    python tools/generate-speech.py --models <model-directory>

Generation trims excessive silence, applies short endpoint fades, validates PCM
and normalizes with a 0.115 peak ceiling. metrics.json records actual duration,
RMS, peak, sample rate, text and output SHA-256. Regeneration is a development
operation and is intentionally absent from build/start scripts. Neural inference
may produce slightly different recordings between runs; shipped file hashes
identify the reviewed outputs.

Language policy: German UI/system locale selects German; English and unsupported
locales select English. A missing selected file is logged once and omitted;
there is no generated or other-language fallback. Human listening review remains
necessary to certify pronunciation, tone and comfort.
