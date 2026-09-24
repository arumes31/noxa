"""Publish a stationary noXa positional-audio sample for local testing."""
import argparse
import json
import os
from pathlib import Path
import tempfile
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--path", type=Path, required=True, help="Input path copied from noXa playback settings")
parser.add_argument("--x", type=float, default=0)
parser.add_argument("--y", type=float, default=0)
parser.add_argument("--z", type=float, default=0)
parser.add_argument("--context", default="noxa-positional-demo")
args = parser.parse_args()
args.path.parent.mkdir(parents=True, exist_ok=True)
payload = json.dumps({"x": args.x, "y": args.y, "z": args.z, "context": args.context,
                      "forward": [0, 0, -1], "up": [0, 1, 0]}, allow_nan=False)
try:
    while True:
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=args.path.parent,
                                         prefix="input-", suffix=".tmp", delete=False) as stream:
            stream.write(payload)
            temporary = Path(stream.name)
        try:
            os.replace(temporary, args.path)
        finally:
            temporary.unlink(missing_ok=True)
        time.sleep(0.25)
except KeyboardInterrupt:
    pass  # The last sample expires automatically; do not delete another writer's file.
