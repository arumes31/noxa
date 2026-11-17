#!/bin/sh
# Builds client/internal/nativecapture/libopus/opus.dll reproducibly from the
# pinned official libopus release (mingw-w64, no cgo in the client build).
# Run from the repository root:
#   docker run --rm -v "$PWD:/src" -w /src debian@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc sh tools/libopus/build.sh
# The output must hash to client/internal/nativecapture/libopus/opus.dll.sha256.
set -eu
VERSION=1.6.1
TARBALL_SHA256=6ffcb593207be92584df15b32466ed64bbec99109f007c82205f0194572411a1
OUT=client/internal/nativecapture/libopus

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends ca-certificates curl make \
  gcc-mingw-w64-x86-64-win32=12.2.0-14+deb12u1+25.2+b1 \
  binutils-mingw-w64-x86-64=2.40-2+10.4 \
  mingw-w64-x86-64-dev=10.0.0-3 >/dev/null

work=$(mktemp -d)
curl -fsSL "https://downloads.xiph.org/releases/opus/opus-$VERSION.tar.gz" -o "$work/opus.tar.gz"
echo "$TARBALL_SHA256  $work/opus.tar.gz" | sha256sum -c -
tar -xzf "$work/opus.tar.gz" -C "$work"
cd "$work/opus-$VERSION"
export SOURCE_DATE_EPOCH=0
./configure -q --host=x86_64-w64-mingw32 --disable-static --enable-shared \
  --disable-doc --disable-extra-programs --disable-stack-protector \
  CFLAGS="-O2 -U_FORTIFY_SOURCE -D_FORTIFY_SOURCE=0 -ffile-prefix-map=$work=." \
  LDFLAGS="-Wl,--no-insert-timestamp -static-libgcc" >/dev/null
make -s -j"$(nproc)" >/dev/null
x86_64-w64-mingw32-strip -o /src/$OUT/opus.dll .libs/libopus-0.dll
cd /src
# Only system DLLs may be imported.
imports=$(x86_64-w64-mingw32-objdump -p $OUT/opus.dll | sed -n 's/^\tDLL Name: //p' | sort -u | tr '\n' ' ')
echo "imports: $imports"
for dll in $imports; do
  case "$dll" in KERNEL32.dll|msvcrt.dll) ;; *) echo "unexpected import $dll" >&2; exit 1 ;; esac
done
sha256sum $OUT/opus.dll
(cd $OUT && sha256sum -c opus.dll.sha256)
