"""Offline Qwen3-TTS Base authoring using the selected synthetic AN06 reference.

Writes raw WAVs, mastered candidates and results.json to a development directory.
Never called by the application or its ordinary build. Review complete speech
before copying candidates into the static speech assets and publishing metrics.
"""
import argparse
import importlib.util
import json
import subprocess
import wave
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('noxa_speech_authoring', ROOT / 'tools/generate-speech.py')
speech = importlib.util.module_from_spec(spec)
spec.loader.exec_module(speech)


def write_json(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + '.new')
    temporary.write_text(json.dumps(data, indent=2, ensure_ascii=False, allow_nan=False) + '\n', encoding='utf-8')
    temporary.replace(path)


def verify_file(path, digest, size=None):
    if not path.is_file() or (size is not None and path.stat().st_size != size):
        raise ValueError(f'Missing or incomplete pinned file: {path}')
    if speech.sha256(path) != digest:
        raise ValueError(f'Checksum mismatch: {path}')


def render_text(text, pronunciation_aliases):
    for spelling, pronunciation in pronunciation_aliases.items():
        text = text.replace(spelling, pronunciation)
    return text


def requests_for(lines, settings, events, seed_offset, pronunciation_aliases=None):
    aliases = pronunciation_aliases or {}
    return [{'id': event, 'text': render_text(text, aliases), 'language': settings['language'],
             'voice_ref': str((ROOT / settings['reference']['path']).resolve()),
             'reference_text': settings['reference']['text'],
             'seed': settings['seed_base'] + index * settings['seed_step'] + seed_offset,
             'max_tokens': settings['max_tokens']}
            for index, (event, text) in enumerate(lines.items()) if event in events]


def find_raw(directory, event):
    paths = list(directory.rglob('*.wav'))
    matches = [path for path in paths if path.stem == event or path.parent.name == event]
    if len(matches) != 1:
        raise ValueError(f'{event}: expected one raw WAV, found {len(matches)} in {directory}')
    return matches[0]


def master_raw(source, destination, settings, max_tokens):
    import numpy as np
    with wave.open(str(source), 'rb') as wav:
        if wav.getnchannels() != 1 or wav.getsampwidth() != 2 or wav.getcomptype() != 'NONE':
            raise ValueError(f'Expected mono PCM16 raw speech: {source}')
        rate = wav.getframerate()
        frames = wav.getnframes()
        encoded = wav.readframes(frames)
    if rate != 24000:
        raise ValueError(f'Expected 24 kHz Qwen raw speech: {source}')
    if len(encoded) != frames * 2:
        raise ValueError(f'Incomplete WAV frames: {source}')
    # Qwen uses 12 Hz acoustic tokens. A reached token budget is a suspected
    # truncated utterance even when the native process returned success.
    raw_duration = frames / rate
    if raw_duration >= (max_tokens - 1) / 12:
        raise ValueError(f'Raw speech reached token limit ({raw_duration:.3f}s): {source}')
    raw = np.frombuffer(encoded, dtype='<i2')
    if np.any((raw == -32768) | (raw == 32767)):
        raise ValueError(f'Clipped raw speech: {source}')
    samples = raw.astype(np.float64) / 32768
    pcm, measured = speech.master_samples(samples, rate, settings, 'de')
    speech.write_recording(destination, pcm, rate)
    return {**measured, 'sha256': speech.sha256(destination),
            'rawSha256': speech.sha256(source), 'rawDuration': raw_duration}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--runtime', type=Path, default=ROOT / '.cache/noxa-accent-local/runtime-fast/bin/audiocpp_cli.exe')
    parser.add_argument('--model', type=Path, default=ROOT / '.cache/noxa-speech-models/qwen3-tts-12hz-1.7b-base-q8_0_v2.gguf')
    parser.add_argument('--events', nargs='+', help='Select canonical event IDs; default: all German announcements')
    parser.add_argument('--output', type=Path, default=ROOT / '.cache/noxa-an06-speech')
    parser.add_argument('--raw-dir', type=Path, help='Native output directory; default: OUTPUT/raw')
    parser.add_argument('--import-raw', type=Path, help='Master existing raw WAVs instead of running inference')
    parser.add_argument('--import-manifest', type=Path, help='Required raw-manifest.json binding imported WAVs to their original requests')
    parser.add_argument('--timeout-seconds', type=int, help='Native session timeout; default: 300 seconds per selected event')
    parser.add_argument('--prepare-only', action='store_true', help='Write pinned request manifest without inference or mastering')
    parser.add_argument('--seed-offset', type=int, default=0, help='Record an explicit retry seed offset')
    args = parser.parse_args()
    provenance = json.loads((ROOT / 'client/frontend/src/assets/speech/provenance.json').read_text(encoding='utf-8'))
    lines = json.loads((ROOT / 'tools/speech-lines.json').read_text(encoding='utf-8'))['de']
    settings = provenance['authoring']['de']
    provider = provenance['providers']['de']
    selected = set(args.events or lines)
    if selected - set(lines):
        parser.error('Unknown speech events: ' + ', '.join(sorted(selected - set(lines))))
    if args.seed_offset < 0:
        parser.error('--seed-offset must be nonnegative')
    if args.prepare_only and args.import_raw:
        parser.error('--prepare-only and --import-raw are mutually exclusive')
    if bool(args.import_raw) != bool(args.import_manifest):
        parser.error('--import-raw requires --import-manifest, and vice versa')
    timeout = args.timeout_seconds if args.timeout_seconds is not None else settings['timeout_seconds_per_event'] * len(selected)
    if not 1 <= timeout <= 43200:
        parser.error('--timeout-seconds must be between 1 and 43200')
    # Prevent this candidate generator from publishing into production by mistake.
    assets = (ROOT / 'client/frontend/src/assets').resolve()
    for directory in (args.output, args.raw_dir):
        if directory is not None and directory.resolve().is_relative_to(assets):
            parser.error('Use a development output directory outside production assets')
    if ((args.output / 'results.json').exists() or (args.output / 'raw-manifest.json').exists() or
            list((args.output / 'mastered').glob('*.wav'))):
        parser.error('Output already contains a generated run; use a fresh output directory')
    reference = ROOT / settings['reference']['path']
    verify_file(reference, settings['reference']['sha256'])
    aliases = provenance.get('pronunciation_aliases', {}).get('de', {})
    requests = requests_for(lines, settings, selected, args.seed_offset, aliases)
    args.output.mkdir(parents=True, exist_ok=True)
    request_path = args.output / 'requests.json'
    write_json(request_path, {'requests': requests})
    authoring = {
        'provider': provider, 'settings': settings, 'referenceSha256': speech.sha256(reference),
        'seed_offset': args.seed_offset, 'requests': requests,
        'timeout_seconds': timeout,
        'mode': 'import' if args.import_raw else ('prepare' if args.prepare_only else 'generate')}
    write_json(args.output / 'authoring.json', authoring)
    if args.prepare_only:
        print(f'Prepared {len(requests)} German AN06 requests: {request_path}', flush=True)
        return
    # Fail before expensive inference when the authoring environment is incomplete.
    import numpy
    raw_directory = args.import_raw or args.raw_dir or args.output / 'raw'
    raw_manifest = None
    if args.import_raw:
        raw_manifest = json.loads(args.import_manifest.read_text(encoding='utf-8'))
        for field in ('provider', 'settings', 'referenceSha256'):
            if raw_manifest.get(field) != authoring[field]:
                raise ValueError(f'Imported raw provenance mismatch: {field}')
        recorded = {request['id']: request for request in raw_manifest['requests']}
        if len(recorded) != len(raw_manifest['requests']):
            raise ValueError('Duplicate request IDs in imported raw manifest')
        for request in requests:
            original = recorded.get(request['id'], {})
            if any(original.get(key) != value for key, value in request.items()):
                raise ValueError(f'Imported raw request mismatch: {request["id"]}')
            verify_file(find_raw(raw_directory, request['id']), original['rawSha256'])
    if not args.import_raw:
        verify_file(args.model, provider['sha256'], provider['size'])
        runtime = args.runtime / 'audiocpp_cli.exe' if args.runtime.is_dir() else args.runtime
        if not runtime.is_file():
            raise ValueError(f'Missing audio.cpp runtime: {runtime}')
        verify_file(runtime, settings['runtime']['executable_sha256'])
        raw_directory.mkdir(parents=True, exist_ok=True)
        for request in requests:
            existing = [p for p in raw_directory.rglob('*.wav')
                        if p.stem == request['id'] or p.parent.name == request['id']]
            if existing:
                raise ValueError(f'Raw output already exists for {request["id"]}; use a fresh directory or --import-raw')
        command = [str(runtime.resolve()), '--task', settings['task'], '--family', provider['engine'],
                   '--model', str(args.model.resolve()), '--backend', 'cpu', '--threads', str(settings['threads']),
                   '--request-sequence', str(request_path.resolve()), '--out-dir', str(raw_directory.resolve()),
                   '--batch-manifest-out', str((args.output / 'native-manifest.json').resolve()),
                   '--metrics', '--log-file', str((args.output / 'native.log').resolve())]
        print(f'Generating {len(requests)} AN06 sentences using {settings["threads"]} CPU threads.', flush=True)
        with (args.output / 'native.stdout').open('w', encoding='utf-8') as log:
            subprocess.run(command, cwd=runtime.parent, stdout=log, stderr=subprocess.STDOUT,
                           check=True, timeout=timeout)
        native = json.loads((args.output / 'native-manifest.json').read_text(encoding='utf-8'))['requests']
        if len(native) != len(requests) or {item['id'] for item in native} != selected:
            raise ValueError('Native batch manifest does not contain every selected event exactly once')
        if any(item['sample_rate'] != 24000 or item['channels'] != 1 or item['samples'] <= 0 for item in native):
            raise ValueError('Native batch manifest contains invalid speech audio')
        raw_manifest = {**authoring, 'requests': [
            {**request, 'rawSha256': speech.sha256(find_raw(raw_directory, request['id']))}
            for request in requests]}
        write_json(args.output / 'raw-manifest.json', raw_manifest)
    results = []
    for request in requests:
        event = request['id']
        source = find_raw(raw_directory, event)
        destination = args.output / 'mastered' / (event + '.wav')
        measured = master_raw(source, destination, provenance['authoring'], request['max_tokens'])
        results.append({'event': event, 'language': 'de', 'title': f'noXa — {event}',
                        'text': lines[event], 'renderText': request['text'],
                        'seed': request['seed'], 'file': str(destination),
                        **measured, 'reviewStatus': 'pending-transcript-and-listening-review'})
        write_json(args.output / 'results.json', results)
        print('de', event, round(measured['duration'], 3), flush=True)
    if len({result['sha256'] for result in results}) != len(results):
        raise ValueError('Duplicate mastered recordings')
    print(f'{len(results)} mastered AN06 candidates ready for transcript/listening review: {args.output / "results.json"}', flush=True)


if __name__ == '__main__':
    main()
