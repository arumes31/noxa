"""Development-only static speech rendering. Never imported or run by noXa.

Usage: python tools/generate-speech.py --models .cache/noxa-speech-models [--events user_join user_leave]
Requires piper-tts==1.4.2, numpy and onnx in an isolated environment.
Model directory contains en.onnx, de.onnx, matching .json files and provenance.json.
"""
import argparse
import hashlib
import importlib.metadata
import io
import json
import math
import wave
from pathlib import Path
import numpy as np
from piper import PiperVoice, SynthesisConfig
from piper.config import PiperConfig


def load_german_voice(models, settings):
    import onnx
    import onnxruntime as ort

    config = json.loads((models / 'de.onnx.json').read_text(encoding='utf-8'))
    assert config['speaker_id_map'][settings['source_speaker']] == settings['speaker_id']
    model = onnx.load(str(models / 'de.onnx'))
    # Expose existing duration predictions; weights and inference stay unchanged.
    model.graph.output.append(onnx.helper.make_tensor_value_info(
        settings['alignment_output'], onnx.TensorProto.FLOAT, [1, 1, 'phonemes']))
    onnx.checker.check_model(model)
    options = ort.SessionOptions()
    options.intra_op_num_threads = 1
    options.inter_op_num_threads = 1
    session = ort.InferenceSession(model.SerializeToString(), sess_options=options,
                                   providers=['CPUExecutionProvider'])
    return PiperVoice(session=session, config=PiperConfig.from_dict(config))


def render_german(voice, text, settings):
    for word, pronunciation in settings['pronunciation'].items():
        text = text.replace(word, pronunciation)
    phonemes = [p for sentence in voice.phonemize(text) for p in sentence]
    # MLS needs audiobook-length context. One inference avoids Piper's sentence
    # splitting; only the final repetition becomes the shipped announcement.
    repeats = max(settings['minimum_repetitions'],
                  math.ceil(settings['minimum_context_phonemes'] / len(phonemes)))
    prefix = (phonemes + [' ']) * (repeats - 1)
    ids = voice.phonemes_to_ids(prefix + phonemes)
    start = len(voice.phonemes_to_ids(prefix)) - 1
    samples, counts = voice.phoneme_ids_to_audio(ids, SynthesisConfig(
        speaker_id=settings['speaker_id'], length_scale=settings['length_scale'],
        noise_scale=settings['noise_scale'], noise_w_scale=settings['noise_w_scale']),
        include_alignments=True)
    assert counts is not None and len(counts) == len(ids) and counts.sum() == len(samples)
    rate = voice.config.sample_rate
    offset = max(0, int(counts[:start].sum()) - round(rate * settings['alignment_padding_ms'] / 1000))
    return rate, samples[offset:].astype(np.float64)

parser = argparse.ArgumentParser()
parser.add_argument('--models', type=Path, required=True)
parser.add_argument('--events', nargs='+', help='Render only these events; retain other existing recordings and metrics')
parser.add_argument('--languages', nargs='+', choices=['en', 'de'], default=['en', 'de'])
args = parser.parse_args()
root = Path(__file__).resolve().parents[1]
out = root / 'client/frontend/src/assets/speech'
lines = json.loads((Path(__file__).parent / 'speech-lines.json').read_text(encoding='utf-8'))
if args.events and set(args.events) - {event for phrases in lines.values() for event in phrases}:
    parser.error('Unknown speech event')
assets = {}
provenance = json.loads((out / 'provenance.json').read_text(encoding='utf-8'))
assert importlib.metadata.version('piper-tts') == '1.4.2'
model_provenance = json.loads((args.models / 'provenance.json').read_text(encoding='utf-8'))
assert model_provenance['models'] == provenance['models'] and model_provenance['revision'] == provenance['revision'], 'Download the current authoring models first'
for name, digest in provenance.get('checksums', {}).items():
    if name.split('.')[0] in args.languages:
        assert hashlib.sha256((args.models / name).read_bytes()).hexdigest() == digest, name
metrics = json.loads((out / 'metrics.json').read_text(encoding='utf-8'))
for language, phrases in lines.items():
    german = provenance['authoring']['de'] if language == 'de' else None
    voice = None
    if language in args.languages:
        voice = load_german_voice(args.models, german) if german else PiperVoice.load(str(args.models / (language + '.onnx')))
    (out / language).mkdir(parents=True, exist_ok=True)
    assets[language] = {}
    for event, text in phrases.items():
        if voice is None or (args.events and event not in args.events):
            assert metrics[f'{language}/{event}']['text'] == text, f'Regenerate changed text: {language}/{event}'
            assets[language][event] = metrics[f'{language}/{event}']['duration']
            continue
        if german:
            event_settings = {**german, **german.get('events', {}).get(event, {})}
            rate, samples = render_german(voice, text, event_settings)
        else:
            raw = io.BytesIO()
            with wave.open(raw, 'wb') as wav:
                voice.synthesize_wav(text, wav, syn_config=SynthesisConfig(length_scale=1.08, noise_scale=.55, noise_w_scale=.7))
            raw.seek(0)
            with wave.open(raw, 'rb') as wav:
                assert wav.getnchannels() == 1 and wav.getsampwidth() == 2
                rate = wav.getframerate()
                samples = np.frombuffer(wav.readframes(wav.getnframes()), dtype='<i2').astype(np.float64) / 32768
        assert rate in (22050, 24000, 48000) and np.isfinite(samples).all()
        active = np.flatnonzero(np.abs(samples) > .002)
        assert len(active), f'{language}/{event}: empty clip'
        leading = round(rate * (german['leading_padding_ms'] if german else 25) / 1000)
        trailing = round(rate * (german['trailing_padding_ms'] if german else 25) / 1000)
        samples = samples[max(0, active[0]-leading):min(len(samples), active[-1]+trailing+1)].copy()
        samples -= samples.mean()
        ramp = np.linspace(0, 1, round(rate * (german['endpoint_fade_ms'] if german else 5) / 1000))
        samples[:len(ramp)] *= ramp
        samples[-len(ramp):] *= ramp[::-1]
        rms = np.sqrt(np.mean(samples**2))
        gain = min(10**(-31/20) / rms, .115 / np.max(np.abs(samples)))
        pcm = np.round(samples * gain * 32767).astype('<i2')
        measured = pcm.astype(float) / 32768
        duration = len(pcm) / rate
        assert .5 <= duration <= 6, f'{language}/{event}: duration {duration}'
        assert np.max(np.abs(measured)) <= .1151 and pcm[0] == 0 and pcm[-1] == 0
        assert abs(measured.mean()) < .0001
        path = out / language / (event + '.wav')
        with wave.open(str(path), 'wb') as wav:
            wav.setnchannels(1); wav.setsampwidth(2); wav.setframerate(rate); wav.writeframes(pcm.tobytes())
        assert path.stat().st_size == 44 + len(pcm)*2
        assets[language][event] = duration
        metrics[f'{language}/{event}'] = {'title':f'noXa — {event}', 'text':text,'duration':duration,'sampleRate':rate,'peak':float(np.max(np.abs(measured))), 'rmsDB':float(20*np.log10(np.sqrt(np.mean(measured**2)))),'sha256':hashlib.sha256(path.read_bytes()).hexdigest()}
        print(language, event, round(duration, 3), flush=True)

catalog = '// Generated by tools/generate-speech.py. Static assets only.\nexport const SPEECH_ASSETS = {\n'
for language, events in assets.items():
    catalog += f'    {language}: {{\n'
    for event, duration in events.items():
        transcript = json.dumps(lines[language][event], ensure_ascii=False)
        catalog += f'        {event}: {{ url: new URL("./assets/speech/{language}/{event}.wav", import.meta.url).href, duration: {duration}, transcript: {transcript} }},\n'
    catalog += '    },\n'
catalog += '};\n'
(root / 'client/frontend/src/speech-catalog.js').write_text(catalog, encoding='utf-8')
(out / 'metrics.json').write_text(json.dumps(metrics, indent=2, ensure_ascii=False)+'\n', encoding='utf-8')
for language in args.languages:
    (out / (language+'-MODEL_CARD')).write_bytes((args.models/(language+'-MODEL_CARD')).read_bytes())
