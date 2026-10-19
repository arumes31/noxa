"""Offline authoring of noXa's fixed spoken announcements with the ST13 voice.

Never run by noXa or its build. ST13 "Eisbrecher" is a synthetic voice: a
weighted blend of Supertonic 3 preset styles (tools/voices/ST13.json), rendered
with the sherpa-onnx CLI. Both languages use the same voice.

  python tools/generate-supertonic-speech.py render --model-dir DIR --tts BIN --out .cache/noxa-st13-speech
  python tools/generate-supertonic-speech.py review --out .cache/noxa-st13-speech --asr BIN --whisper-dir DIR
  python tools/generate-supertonic-speech.py publish --out .cache/noxa-st13-speech
  python tools/generate-speech.py --catalog-only

render verifies every pinned model file, builds the blended style, applies the
pronunciation aliases from provenance.json, masters each phrase with the shared
contract in generate-speech.py and writes candidates plus a manifest.
review transcribes every candidate with Whisper and records the automatic
content check. publish copies reviewed candidates into the production assets.
Synthesis is not bit-reproducible: the accepted recordings are identified by
their shipped hashes.
"""
import argparse
import hashlib
import importlib.util
import json
import re
import shutil
import subprocess
import wave
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
ASSETS = ROOT / 'client/frontend/src/assets/speech'
VOICE = json.loads((ROOT / 'tools/voices/ST13.json').read_text(encoding='utf-8'))
PROVENANCE = json.loads((ASSETS / 'provenance.json').read_text(encoding='utf-8'))
LINES = json.loads((ROOT / 'tools/speech-lines.json').read_text(encoding='utf-8'))
TARGET_RATE = 24000

spec = importlib.util.spec_from_file_location('generate_speech', ROOT / 'tools/generate-speech.py')
generate_speech = importlib.util.module_from_spec(spec)
spec.loader.exec_module(generate_speech)
sha256 = generate_speech.sha256


def render_text(language, text):
    """Apply whole-word pronunciation aliases; longer keys first so compounds win."""
    aliases = PROVENANCE['pronunciation_aliases'].get(language, {})
    for word in sorted(aliases, key=len, reverse=True):
        text = re.sub(rf'(?<!\w){re.escape(word)}(?!\w)', aliases[word], text)
    return text


def verify_model(model_dir):
    for name, expected in VOICE['model']['files'].items():
        actual = sha256(model_dir / name)
        if actual != expected:
            raise SystemExit(f'Model file {name} does not match the pinned hash: {actual}')


def blended_style(model_dir):
    """The ST13 style: weighted sum of the preset style vectors in voice.bin."""
    import numpy as np
    data = (model_dir / 'voice.bin').read_bytes()
    ttl_dims = np.frombuffer(data[:24], np.int64)
    dp_dims = np.frombuffer(data[24:48], np.int64)
    ttl_size, dp_size = int(np.prod(ttl_dims)), int(np.prod(dp_dims))
    ttl = np.frombuffer(data[48:48 + ttl_size * 4], np.float32).reshape(ttl_dims)
    dp = np.frombuffer(data[48 + ttl_size * 4:48 + (ttl_size + dp_size) * 4], np.float32).reshape(dp_dims)
    order = VOICE['model']['preset_order']
    weights = {order.index(name): weight for name, weight in VOICE['blend'].items()}
    style_ttl = sum(ttl[index] * weight for index, weight in weights.items()).astype(np.float32)
    style_dp = sum(dp[index] * weight for index, weight in weights.items()).astype(np.float32)
    blob = (np.array([1, ttl_dims[1], ttl_dims[2]], np.int64).tobytes() +
            np.array([1, dp_dims[1], dp_dims[2]], np.int64).tobytes() +
            style_ttl.tobytes() + style_dp.tobytes())
    return blob


def read_wav(path):
    import numpy as np
    with wave.open(str(path)) as source:
        if source.getnchannels() != 1 or source.getsampwidth() != 2:
            raise ValueError(f'Expected mono PCM16: {path}')
        rate = source.getframerate()
        samples = np.frombuffer(source.readframes(source.getnframes()), '<i2').astype(np.float64) / 32768
    return samples, rate


def resample(samples, rate, target):
    """Band-limited FFT resampling of a short clip (no extra dependencies)."""
    import numpy as np
    if rate == target:
        return samples
    count = int(round(len(samples) * target / rate))
    spectrum = np.fft.rfft(samples)
    keep = min(len(spectrum), count // 2 + 1)
    out = np.zeros(count // 2 + 1, dtype=complex)
    out[:keep] = spectrum[:keep]
    return np.fft.irfft(out, count) * (count / len(samples))


def render(args):
    model_dir = Path(args.model_dir)
    verify_model(model_dir)
    out = Path(args.out)
    (out / 'raw').mkdir(parents=True, exist_ok=True)
    style = out / 'ST13-style.bin'
    style.write_bytes(blended_style(model_dir))
    settings = VOICE['synthesis']
    manifest = {'voice': VOICE['id'], 'styleSha256': sha256(style), 'clips': {}}
    for language in args.languages:
        for event, text in LINES[language].items():
            if args.events and event not in args.events:
                continue
            spoken = render_text(language, text)
            raw = out / 'raw' / language / f'{event}.wav'
            raw.parent.mkdir(parents=True, exist_ok=True)
            subprocess.run([args.tts,
                f'--supertonic-duration-predictor={model_dir}/duration_predictor.int8.onnx',
                f'--supertonic-text-encoder={model_dir}/text_encoder.int8.onnx',
                f'--supertonic-vector-estimator={model_dir}/vector_estimator.int8.onnx',
                f'--supertonic-vocoder={model_dir}/vocoder.int8.onnx',
                f'--supertonic-tts-json={model_dir}/tts.json',
                f'--supertonic-unicode-indexer={model_dir}/unicode_indexer.bin',
                f'--supertonic-voice-style={style}', '--sid=0', f'--lang={language}',
                f'--speed={settings["speed"]}', f'--num-steps={settings["num_steps"]}',
                f'--num-threads={settings["threads"]}', f'--output-filename={raw}', spoken],
                check=True, capture_output=True, timeout=300)
            samples, rate = read_wav(raw)
            pcm, metric = generate_speech.master_samples(resample(samples, rate, TARGET_RATE), TARGET_RATE,
                                                         PROVENANCE['authoring'], language)
            clip = out / 'clips' / language / f'{event}.wav'
            generate_speech.write_recording(clip, pcm, TARGET_RATE)
            manifest['clips'][f'{language}/{event}'] = {
                'title': f'noXa — {event}', 'text': text, **metric, 'sha256': sha256(clip),
                'voice': VOICE['id'], 'renderText': spoken, 'rawSha256': sha256(raw), 'rawSampleRate': rate,
                'styleSha256': manifest['styleSha256'], 'modelSha256': VOICE['model']['archive_sha256']}
            print(f'{language}/{event}: {metric["duration"]:.2f} s')
    previous = out / 'manifest.json'
    if previous.exists():
        old = json.loads(previous.read_text(encoding='utf-8'))
        old['clips'].update(manifest['clips'])
        manifest['clips'] = old['clips']
    previous.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')


def letters(text):
    return re.sub(r'[^\w]|_', '', text.lower())


def review(args):
    out = Path(args.out)
    manifest = json.loads((out / 'manifest.json').read_text(encoding='utf-8'))
    results = {}
    for language in ('en', 'de'):
        keys = [key for key in manifest['clips'] if key.startswith(language + '/')]
        files = [str(out / 'clips' / f'{key}.wav') for key in keys]
        whisper = Path(args.whisper_dir)
        log = subprocess.run([args.asr, f'--whisper-encoder={whisper}/small-encoder.int8.onnx',
            f'--whisper-decoder={whisper}/small-decoder.int8.onnx', f'--tokens={whisper}/small-tokens.txt',
            '--model-type=whisper', f'--whisper-language={language}', '--num-threads=2', *files],
            check=True, capture_output=True, text=True)
        log = log.stdout + log.stderr
        texts = [json.loads('"' + match + '"') for match in re.findall(r'"text": "((?:[^"\\]|\\.)*)"', log)]
        if len(texts) != len(keys):
            raise SystemExit(f'Expected {len(keys)} transcripts, got {len(texts)}')
        for key, transcript in zip(keys, texts):
            clip = manifest['clips'][key]
            heard = transcript.lower()
            for written, spoken in PROVENANCE['pronunciation_aliases'].get(language, {}).items():
                heard = heard.replace(spoken.lower(), written.lower())
            # Compare letters only: Whisper splits or joins compounds and words freely.
            exact = letters(heard) == letters(clip['text'])
            results[key] = {'sha256': clip['sha256'], 'expected': clip['text'], 'renderText': clip['renderText'],
                'language': language, 'automatic_transcript': transcript, 'duration': round(clip['duration'], 3),
                'asr': 'Whisper small (sherpa-onnx int8)',
                'note': 'Automatic content review; not a human listening or timbre assessment.',
                'reviewStatus': 'accepted-automatic-content-review' if exact else 'needs-review'}
            print(key, 'ok' if exact else 'CHECK', '|', transcript)
    (out / 'st13-content-review.json').write_text(json.dumps(results, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')


def publish(args):
    out = Path(args.out)
    manifest = json.loads((out / 'manifest.json').read_text(encoding='utf-8'))
    reviewed = json.loads((out / 'st13-content-review.json').read_text(encoding='utf-8'))
    expected = {f'{language}/{event}' for language in LINES for event in LINES[language]}
    if set(manifest['clips']) != expected or set(reviewed) != expected:
        raise SystemExit('Render and review every event in both languages before publishing')
    for key, clip in manifest['clips'].items():
        if reviewed[key]['sha256'] != clip['sha256'] or reviewed[key]['reviewStatus'] != 'accepted-automatic-content-review':
            raise SystemExit(f'{key} is not accepted; re-render it with --events and review again')
        shutil.copyfile(out / 'clips' / f'{key}.wav', ASSETS / f'{key}.wav')
    (ASSETS / 'metrics.json').write_text(json.dumps(manifest['clips'], ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    (ASSETS / 'st13-content-review.json').write_text(json.dumps(reviewed, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print('Published', len(expected), 'recordings. Run: python tools/generate-speech.py --catalog-only')


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    commands = parser.add_subparsers(dest='command', required=True)
    first = commands.add_parser('render')
    first.add_argument('--model-dir', required=True)
    first.add_argument('--tts', required=True, help='sherpa-onnx-offline-tts executable')
    first.add_argument('--out', required=True)
    first.add_argument('--languages', nargs='+', default=['en', 'de'], choices=['en', 'de'])
    first.add_argument('--events', nargs='*')
    check = commands.add_parser('review')
    check.add_argument('--out', required=True)
    check.add_argument('--asr', required=True, help='sherpa-onnx-offline executable')
    check.add_argument('--whisper-dir', required=True)
    done = commands.add_parser('publish')
    done.add_argument('--out', required=True)
    args = parser.parse_args()
    {'render': render, 'review': review, 'publish': publish}[args.command](args)


if __name__ == '__main__':
    main()
