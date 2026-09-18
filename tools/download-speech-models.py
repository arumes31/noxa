"""Development-only download of the pinned, licensed noXa authoring voices."""
import json
from pathlib import Path
from urllib.request import urlretrieve

root = Path(__file__).resolve().parents[1]
provenance = root / 'client/frontend/src/assets/speech/provenance.json'
data = json.loads(provenance.read_text())
out = root / '.cache/noxa-speech-models'
out.mkdir(parents=True, exist_ok=True)
base = f'https://huggingface.co/{data["repository"]}/resolve/{data["revision"]}/'
for language, model in data['models'].items():
    for source, target in [(model+'.onnx', language+'.onnx'), (model+'.onnx.json', language+'.onnx.json'),
                           (model.rsplit('/', 1)[0]+'/MODEL_CARD', language+'-MODEL_CARD')]:
        path = out / target
        if not path.exists():
            print('Downloading', target, flush=True)
            urlretrieve(base+source, path)
(out / 'provenance.json').write_text(provenance.read_text())
