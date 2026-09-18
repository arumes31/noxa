"""Offline editing of licensed samples into finished noXa effects. No synthesis.

Requires numpy, scipy and soundfile in the development audio environment.
Source archives are pinned by SHA-256; --download explicitly fetches missing ones.
Normal application builds only package the checked-in WAV files.
"""
import argparse
import hashlib
import io
import json
import math
from pathlib import Path
import urllib.request
import wave
import zipfile

import numpy as np
import soundfile as sf
from scipy.signal import butter, resample_poly, sosfilt

ROOT = Path(__file__).resolve().parents[1]
ASSETS = ROOT / 'client/frontend/src/assets/sounds'
RATE = 48000


def sha(data):
    return hashlib.sha256(data).hexdigest()


def load_sources(spec, download):
    cache = ROOT / '.cache/noxa-effect-sources'
    cache.mkdir(parents=True, exist_ok=True)
    sources = {}
    for key, package in spec['sources'].items():
        path = cache / package['archive']
        if not path.exists():
            if not download:
                raise FileNotFoundError(f'{path}: run with --download to fetch pinned CC0 source')
            data = urllib.request.urlopen(package['download'], timeout=60).read()
            if sha(data) != package['sha256']:
                raise ValueError(f'Source checksum mismatch: {key}')
            path.write_bytes(data)
        data = path.read_bytes()
        if sha(data) != package['sha256']:
            raise ValueError(f'Source checksum mismatch: {key}')
        sources[key] = zipfile.ZipFile(io.BytesIO(data)) if path.suffix == '.zip' else data
    return sources


def source_part(part, sources):
    source = sources[part['source']]
    data = source.read(part['file']) if isinstance(source, zipfile.ZipFile) else source
    samples, rate = sf.read(io.BytesIO(data), dtype='float64', always_2d=True)
    samples = samples.mean(axis=1)
    # Trim to the first audible onset, retaining 0.5 ms of its original attack.
    active = np.flatnonzero(np.abs(samples) > np.max(np.abs(samples)) * .015)
    start = max(0, int(active[0]) - round(rate * .0005))
    samples = samples[start:start + round(rate * part['lengthMs'] / 1000)]
    divisor = math.gcd(rate, RATE)
    samples = resample_poly(samples, RATE // divisor, rate // divisor)
    # Technical cleanup only; no pitch change, reversal, oscillator or new waveform.
    samples = sosfilt(butter(2, 90, 'highpass', fs=RATE, output='sos'), samples)
    if part.get('lowpassHz'):
        samples = sosfilt(butter(2, part['lowpassHz'], fs=RATE, output='sos'), samples)
    attack = min(len(samples), round(RATE * .0005))
    fade = min(len(samples), round(RATE * part.get('fadeMs', 10) / 1000))
    samples[:attack] *= np.linspace(0, 1, attack)
    samples[-fade:] *= np.linspace(1, 0, fade)
    peak = np.max(np.abs(samples))
    if peak < 1e-8:
        raise ValueError(f'Silent source: {part}')
    samples *= part.get('weight', 1) / peak
    return samples, {**part, 'sourceFileSha256': sha(data), 'trimStartMs': start * 1000 / rate}


def master(event, sources):
    samples = np.zeros(round(event['duration'] * RATE))
    edits = []
    for part in event['parts']:
        fragment, edit = source_part(part, sources)
        offset = round(part.get('atMs', 0) * RATE / 1000)
        count = min(len(fragment), len(samples) - offset)
        if count <= 0:
            raise ValueError(f'Source outside event duration: {event["id"]}')
        samples[offset:offset + count] += fragment[:count]
        edits.append(edit)
    samples[-round(RATE * .005):] *= np.linspace(1, 0, round(RATE * .005))
    # Weighted DC removal preserves silence and the zero endpoints.
    samples -= samples.sum() * np.abs(samples) / np.abs(samples).sum()
    rms = np.sqrt(np.mean(samples ** 2))
    gain = min(10 ** (event['rmsDB'] / 20) / rms, event['peakCeiling'] / np.max(np.abs(samples)))
    pcm = np.round(samples * gain * 32767).astype('<i2')
    assert pcm[0] == 0 and pcm[-1] == 0
    measured = pcm.astype(float) / 32768
    assert 0 < np.max(np.abs(measured)) <= .115
    assert abs(measured.mean()) < .0001
    output = io.BytesIO()
    with wave.open(output, 'wb') as wav:
        wav.setnchannels(1)
        wav.setsampwidth(2)
        wav.setframerate(RATE)
        wav.writeframes(pcm.tobytes())
    data = output.getvalue()
    return data, {'title': f'noXa — {event["label"]}', 'duration': event['duration'],
                  'peak': float(np.max(np.abs(measured))), 'dc': float(measured.mean()),
                  'rmsDB': float(10 * np.log10(np.mean(measured ** 2))),
                  'targetRmsDB': event['rmsDB'], 'sha256': sha(data)}, edits


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--download', action='store_true')
    args = parser.parse_args()
    spec = json.loads((ROOT / 'tools/effect-recipes.json').read_text())
    sources = load_sources(spec, args.download)
    definitions, metrics, provenance, groups = {}, {}, {}, {}
    for event in spec['events']:
        name = event['id']
        data, metrics[name], provenance[name] = master(event, sources)
        (ASSETS / f'{name}.wav').write_bytes(data)
        definitions[name] = {k: event[k] for k in ['label', 'category', 'character', 'duration', 'priority', 'gain', 'cooldown', 'concurrency']}
        groups.setdefault(event['category'], []).append([name, event['label']])
    urls = '\n'.join(f'    {name}: new URL("./assets/sounds/{name}.wav", import.meta.url).href,' for name in definitions)
    catalog = '// Authored offline by tools/master-sounds.py; finished static noXa assets.\n'
    catalog += 'export const SOUND_DEFINITIONS = ' + json.dumps(definitions, indent=4) + ';\n'
    catalog += 'export const SOUND_URLS = {\n' + urls + '\n};\n'
    catalog += 'export const SOUND_EVENTS = Object.keys(SOUND_DEFINITIONS);\n'
    catalog += 'export const SOUND_EVENT_GROUPS = ' + json.dumps([{'label': k, 'events': v} for k, v in groups.items()], indent=4) + ';\n'
    (ASSETS.parents[1] / 'sound-catalog.js').write_text(catalog, encoding='utf-8')
    (ASSETS / 'metrics.json').write_text(json.dumps(metrics, indent=2) + '\n', encoding='utf-8')
    (ASSETS / 'provenance.json').write_text(json.dumps({'title': 'noXa', 'sources': spec['sources'], 'edits': provenance}, indent=2) + '\n', encoding='utf-8')
    print(f'Mastered {len(definitions)} finished noXa effects from licensed source audio.')


if __name__ == '__main__':
    main()
