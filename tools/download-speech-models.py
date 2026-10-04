"""Development-only download of the pinned, licensed noXa authoring voices."""
import hashlib
import json
from pathlib import Path
from urllib.request import urlretrieve

root = Path(__file__).resolve().parents[1]
provenance = root / 'client/frontend/src/assets/speech/provenance.json'
data = json.loads(provenance.read_text(encoding='utf-8'))
out = root / '.cache/noxa-speech-models'
out.mkdir(parents=True, exist_ok=True)
base = f'https://huggingface.co/{data["repository"]}/resolve/{data["revision"]}/'
previous = json.loads((out / 'provenance.json').read_text(encoding='utf-8')) if (out / 'provenance.json').exists() else {}
for language, model in data['models'].items():
    changed = previous.get('revision') != data['revision'] or previous.get('models', {}).get(language) != model
    for source, target in [(model+'.onnx', language+'.onnx'), (model+'.onnx.json', language+'.onnx.json'),
                           (model.rsplit('/', 1)[0]+'/MODEL_CARD', language+'-MODEL_CARD')]:
        path = out / target
        digest = data.get('checksums', {}).get(target)
        if changed or not path.exists() or (digest and hashlib.sha256(path.read_bytes()).hexdigest() != digest):
            print('Downloading', target, flush=True)
            temporary = path.with_suffix(path.suffix + '.download')
            urlretrieve(base+source, temporary)
            if digest and hashlib.sha256(temporary.read_bytes()).hexdigest() != digest:
                raise ValueError(f'Checksum mismatch: {target}')
            temporary.replace(path)
(out / 'provenance.json').write_text(provenance.read_text(encoding='utf-8'), encoding='utf-8')
