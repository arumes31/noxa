"""Development-only download of the pinned, licensed noXa authoring voices."""
import argparse
import hashlib
import json
from pathlib import Path
from urllib.request import urlretrieve

def sha256(path):
    digest = hashlib.sha256()
    with path.open('rb') as source:
        for chunk in iter(lambda: source.read(8 * 1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def download(url, path, digest=None, size=None, changed=False):
    if path.exists() and (size is None or path.stat().st_size == size):
        if (digest and sha256(path) == digest) or (not digest and not changed):
            return
    print('Downloading', path.name, flush=True)
    temporary = path.with_suffix(path.suffix + '.download')
    urlretrieve(url, temporary)
    if size is not None and temporary.stat().st_size != size:
        raise ValueError(f'Size mismatch: {path.name}')
    if digest and sha256(temporary) != digest:
        raise ValueError(f'Checksum mismatch: {path.name}')
    temporary.replace(path)


def main():
    root = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--languages', nargs='+', choices=['en', 'de'], default=['en', 'de'])
    parser.add_argument('--out', type=Path, default=root / '.cache/noxa-speech-models')
    args = parser.parse_args()
    provenance = root / 'client/frontend/src/assets/speech/provenance.json'
    data = json.loads(provenance.read_text(encoding='utf-8'))
    args.out.mkdir(parents=True, exist_ok=True)
    previous_path = args.out / 'provenance.json'
    previous = json.loads(previous_path.read_text(encoding='utf-8')) if previous_path.exists() else {}
    for language in args.languages:
        provider = data['providers'][language]
        base = f'https://huggingface.co/{provider["repository"]}/resolve/{provider["revision"]}/'
        if provider['engine'] == 'piper':
            model = provider['model']
            old_provider = previous.get('providers', {}).get(language)
            legacy_match = (previous.get('repository') == provider['repository'] and
                            previous.get('revision') == provider['revision'] and
                            previous.get('models', {}).get(language) == model)
            changed = old_provider != provider and not legacy_match
            for source, target in [(model+'.onnx', language+'.onnx'),
                                   (model+'.onnx.json', language+'.onnx.json'),
                                   (model.rsplit('/', 1)[0]+'/MODEL_CARD', language+'-MODEL_CARD')]:
                download(base+source, args.out / target, data['checksums'].get(target), changed=changed)
        elif provider['engine'] == 'qwen3_tts':
            download(base+provider['file'], args.out / Path(provider['file']).name,
                     provider['sha256'], provider['size'])
        else:
            raise ValueError(f'Unknown offline speech provider: {provider["engine"]}')
    temporary = previous_path.with_suffix('.json.new')
    temporary.write_text(provenance.read_text(encoding='utf-8'), encoding='utf-8')
    temporary.replace(previous_path)


if __name__ == '__main__':
    main()
