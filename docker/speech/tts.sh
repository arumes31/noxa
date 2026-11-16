#!/bin/sh
# Runs the glibc-built sherpa-onnx CLI through its private glibc loader: the
# runtime image is musl (Alpine), and no other program sees these libraries.
d=/opt/noxa-speech
exec "$d/lib/ld.so" --library-path "$d/lib" "$d/sherpa-onnx-offline-tts" "$@"
