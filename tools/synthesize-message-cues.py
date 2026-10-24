"""Render the two tonal message cues the user selected (audition C33 and D39).

Original synthesized sounds: harmonic sine partials with fast attacks and short
exponential decays; no inharmonic (metallic) partials, long rings or low thumps.
The output is deterministic. master-sounds.py preserves the files byte-for-byte as
approved recordings and verifies their SHA-256, so rerun this only to change them,
then update the hashes in effect-recipes.json.

Requires numpy and scipy.
"""
import io
from pathlib import Path
import wave

import numpy as np
from scipy.signal import butter, sosfilt

ROOT = Path(__file__).resolve().parents[1]
ASSETS = ROOT / 'client/frontend/src/assets/sounds'
RATE = 48000
PEAK = 0.112  # below the engine's 0.115 asset ceiling


def note(name):
    semis = {'C': -9, 'D': -7, 'E': -5, 'F': -4, 'G': -2, 'A': 0, 'B': 2}[name[0]] + (1 if '#' in name else 0)
    return 440 * 2 ** ((semis + 12 * (int(name[-1]) - 4)) / 12)


def tone(f0, dur, f1=None, glide=0.0, decay=0.08, partials=((1, 1), (2, .22), (3, .06)), attack=.004):
    t = np.arange(round(dur * RATE)) / RATE
    f1 = f0 if f1 is None else f1
    freq = f0 + (f1 - f0) * (1 - (1 - np.clip(t / glide, 0, 1)) ** 2) if glide > 0 else np.full(len(t), f0)
    phase = 2 * np.pi * np.cumsum(freq) / RATE
    out = sum(a * np.sin(h * phase) for h, a in partials)
    env = np.minimum(1, t / attack) * np.exp(-t / decay)
    release = min(len(t), round(.012 * RATE))
    env[-release:] *= np.linspace(1, 0, release)
    return out * env


def pop(f, dur=.09, drop=1.9, decay=.035):
    # A bubble pop: a sine falling quickly from drop*f to f.
    return tone(f * drop, dur, f1=f, glide=.018, decay=decay, partials=((1, 1), (2, .12)), attack=.0015)


def mix(*parts, tail=.02):
    out = np.zeros(max(round(at * RATE) + len(x) for at, x in parts) + round(tail * RATE))
    for at, x in parts:
        i = round(at * RATE)
        out[i:i + len(x)] += x
    return out


def master(x):
    x = sosfilt(butter(2, 120, 'highpass', fs=RATE, output='sos'), x)
    x = sosfilt(butter(2, 9000, fs=RATE, output='sos'), x)
    edge = round(.003 * RATE)
    x[:edge] *= np.linspace(0, 1, edge)
    x[-edge:] *= np.linspace(1, 0, edge)
    # Weighted DC removal preserves silence and the zero endpoints.
    x -= x.sum() * np.abs(x) / np.abs(x).sum()
    pcm = np.round(x * PEAK / np.max(np.abs(x)) * 32767).astype('<i2')
    pcm[0] = pcm[-1] = 0
    output = io.BytesIO()
    with wave.open(output, 'wb') as wav:
        wav.setnchannels(1)
        wav.setsampwidth(2)
        wav.setframerate(RATE)
        wav.writeframes(pcm.tobytes())
    return output.getvalue()


CUES = {
    # C33: double bubble pop, 700 then 980 Hz.
    'channel_message': mix((0, pop(700, decay=.028)), (.06, pop(980))),
    # D39: three notes down then up, E6, C6, G6.
    'dm': mix((0, tone(note('E6'), .08, decay=.03)), (.07, tone(note('C6'), .08, decay=.03)),
              (.14, tone(note('G6'), .24, decay=.1))),
}

if __name__ == '__main__':
    import hashlib
    for name, samples in CUES.items():
        data = master(samples)
        (ASSETS / f'{name}.wav').write_bytes(data)
        print(name, (len(data) - 44) // 2 / RATE, hashlib.sha256(data).hexdigest())
