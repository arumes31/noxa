# Fixed noXa spoken announcements

These WAVs are rendered during development. The shipped client only loads and
plays them. No voice models, TTS engine, dynamic text or network TTS is packaged.
English and German include fixed phrases for channel activity, forced moves,
channel/server removal and a preview. Source text: tools/speech-lines.json.

## Sources and redistribution

- English: Piper en_US-ljspeech-high, trained on the [LJ Speech dataset](https://keithito.com/LJ-Speech-Dataset/), whose recordings and transcripts are public domain. The accompanying en-MODEL_CARD identifies that source and license.
- German: selected audition **D33**, Piper **de_DE-mls-medium**, speaker ID **9** (MLS source speaker **9132**). The [Multilingual LibriSpeech dataset](https://www.openslr.org/94/) by Vineel Pratap, Qiantong Xu, Anuroop Sriram, Gabriel Synnaeve and Ronan Collobert (2020) is licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/). Model published by rhasspy / Michael Hansen; see de-MODEL_CARD. German noXa recordings are distributed with this attribution under CC BY 4.0. Changes: synthesized noXa sentences, pronunciation overrides, context cropping, silence trimming, fades and loudness normalization. Attribution is also bundled in public/noxa-audio-licenses.txt.
- Models came from [rhasspy/piper-voices](https://huggingface.co/rhasspy/piper-voices). Exact source revision and model paths are recorded in provenance.json.
- The build tool uses [Piper](https://github.com/OHF-Voice/piper1-gpl) (GPL-3.0). Piper and model weights are not redistributed in noXa. These are generated recordings of noXa's fixed phrases, not copies of another application's notification recordings.

## Regeneration

Use an isolated Python environment with piper-tts==1.4.2, numpy and onnx==1.23.1. Download the
models and configurations at the recorded revision as en.onnx / de.onnx with
matching .onnx.json files, MODEL_CARD files and provenance.json. Run:

    python tools/download-speech-models.py
    python tools/generate-speech.py --models .cache/noxa-speech-models --languages de

Omit --languages to render both languages; --events selects specific announcements.
The German model needs longer context than a short notification. The renderer
repeats each phrase in a single inference and uses the model's phoneme durations
to extract the final occurrence. This prevents the unintelligible output of
isolated short MLS sentences. D33 uses length scale 1 and both noise scales 0.333,
with at least 200 context phonemes / three repetitions. A phoneme override keeps
the loanwords "Channel" and "Server" correctly pronounced in German. Four short
server announcements use length scale 1.15 for clearer consonants. All selection,
pronunciation, alignment and model checksum details are in provenance.json.

Generation trims excessive silence, applies short endpoint fades, validates PCM
and normalizes with a 0.115 peak ceiling. metrics.json records actual duration,
RMS, peak, sample rate, text and output SHA-256. Regeneration is a development
operation and is intentionally absent from build/start scripts. Neural inference
may produce slightly different recordings between runs; shipped file hashes
identify the shipped outputs.

Language policy: German UI/system locale selects German; English and unsupported
locales select English. A missing selected file is logged once and omitted;
actions with a recording use speech only. Their retired beep effects are excluded
from the application bus and preview catalog. Disabled, zero-volume or unavailable
speech remains silent, subject to the existing notification and sound gates.
There is no generated or other-language fallback. Human listening review remains
necessary to certify pronunciation, tone and comfort.
