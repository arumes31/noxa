# libopus for native share audio

`opus.dll` is the reference libopus 1.6.1 (BSD-3-Clause, see
`COPYING-libopus`), cross-compiled with mingw-w64 by
`tools/libopus/build.sh` and embedded into the Windows client. It is not
committed: CI builds it from the pinned release tarball (SHA-256 checked) and
the build must reproduce `opus.dll.sha256` exactly. The client loads it only
when the embedded bytes match that pin. Without the DLL the client still
builds; native screen shares then run without audio.
