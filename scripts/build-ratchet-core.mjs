// Rebuild the pinned shared core, or verify committed artifacts with --check.
// Requires Rust 1.96.0 + wasm32-unknown-unknown; ordinary Go/PWA builds do not.
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const dir = path.join(root, 'internal/e2ee/ratchet');
const rust = path.join(dir, 'rust');
const image = 'rust:1.96.0-slim@sha256:c37af730be4fd8104cbf9aedbd6ab259e51ca2d5437817a0f8680edf66ac6c28';
const container = process.platform === 'win32' || process.argv.includes('--container');
const noticeMarker = '\n--- dependency notices ---\n';
let wasm, metadata, notices;
if (container) {
    // Canonical Linux build avoids platform-dependent panic-location strings.
    // Mount only the crate source, read-only; cache and output stay in .tmp.
    const cache = path.join(root, '.tmp/ratchet-build');
    fs.mkdirSync(cache, { recursive: true });
    const flags = ['--cfg', 'getrandom_backend="custom"', '-C', 'link-arg=--max-memory=67108864', '--remap-path-prefix=/src=/noxa/internal/e2ee/ratchet/rust'].join('\x1f');
    // Dependency sources in the writable cache mount are build-controlled, so
    // notices are collected inside the container, where symlinks resolve only
    // within the container's view of the two mounts and never reach host
    // files. Each notice is emitted as "<package dir>\t<name>\t<base64>".
    const script = String.raw`
rustup target add wasm32-unknown-unknown
cargo build --locked --release --target wasm32-unknown-unknown
cargo metadata --locked --format-version 1 > /tmp/metadata.json
cat /tmp/metadata.json
printf '\n--- dependency notices ---\n'
grep -o '"manifest_path":"[^"]*"' /tmp/metadata.json | sed 's/^"manifest_path":"//; s/"$//' | while IFS= read -r manifest; do
  d=$(dirname "$manifest")
  for f in "$d"/*; do
    n=$(basename "$f")
    case "$n" in [Ll][Ii][Cc][Ee][Nn][Ss][Ee]*|[Cc][Oo][Pp][Yy][Ii][Nn][Gg]*|[Nn][Oo][Tt][Ii][Cc][Ee]*) ;; *) continue ;; esac
    [ -f "$f" ] || continue
    printf '%s\t%s\t' "$d" "$n"
    base64 -w0 "$f"
    printf '\n'
  done
done
`;
    const args = ['run', '--rm', '--mount', `type=bind,source=${rust},target=/src,readonly`, '--mount', `type=bind,source=${cache},target=/cargo`, '-w', '/src', '-e', 'CARGO_HOME=/cargo', '-e', 'CARGO_TARGET_DIR=/cargo/target', '-e', `CARGO_ENCODED_RUSTFLAGS=${flags}`, image, 'sh', '-eu', '-c', script];
    const out = execFileSync('docker', args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'inherit'], maxBuffer: 256 << 20 });
    const at = out.indexOf(noticeMarker);
    if (at < 0) throw new Error('ratchet build container produced no dependency notices');
    metadata = JSON.parse(out.slice(0, at));
    const collected = new Map();
    for (const line of out.slice(at + noticeMarker.length).split('\n')) {
        if (!line) continue;
        const [home, name, data] = line.split('\t');
        if (!collected.has(home)) collected.set(home, []);
        collected.get(home).push([name, Buffer.from(data, 'base64').toString('utf8')]);
    }
    notices = manifest => collected.get(path.posix.dirname(manifest)) || [];
    wasm = fs.readFileSync(path.join(cache, 'target/wasm32-unknown-unknown/release/noxa_ratchet_core.wasm'));
} else {
    const cargo = process.env.CARGO || 'cargo';
    const cargoHome = process.env.CARGO_HOME || path.join(process.env.HOME, '.cargo');
    const flags = ['--cfg', 'getrandom_backend="custom"', '-C', 'link-arg=--max-memory=67108864', `--remap-path-prefix=${root}=/noxa`, `--remap-path-prefix=${cargoHome}=/cargo`].join('\x1f');
    const env = { ...process.env, CARGO_ENCODED_RUSTFLAGS: flags, RUSTFLAGS: '' };
    execFileSync(cargo, ['build', '--locked', '--release', '--target', 'wasm32-unknown-unknown'], { cwd: rust, env, stdio: 'inherit' });
    const target = process.env.CARGO_TARGET_DIR || path.join(rust, 'target');
    wasm = fs.readFileSync(path.join(target, 'wasm32-unknown-unknown/release/noxa_ratchet_core.wasm'));
    metadata = JSON.parse(execFileSync(cargo, ['metadata', '--locked', '--format-version', '1'], { cwd: rust, env, encoding: 'utf8' }));
    // A host build already runs dependency build code with host authority;
    // still refuse symlinked notice files rather than following them.
    notices = manifest => {
        const home = path.dirname(manifest);
        const found = [];
        for (const name of fs.readdirSync(home)) {
            const file = path.join(home, name);
            const st = fs.lstatSync(file);
            if (st.isSymbolicLink()) throw new Error(`refusing symlinked dependency notice: ${file}`);
            if (!st.isFile()) continue;
            const fd = fs.openSync(file, fs.constants.O_RDONLY | (fs.constants.O_NOFOLLOW || 0));
            try { found.push([name, fs.readFileSync(fd, 'utf8')]); } finally { fs.closeSync(fd); }
        }
        return found;
    };
}
const hash = createHash('sha256').update(wasm).digest('hex');
let licenses = 'noXa shared ratchet core: dependency notices\nIncludes runtime and build-time Rust dependencies. See Cargo.lock for registry checksums.\n\n';
for (const pkg of metadata.packages.filter(p => p.name !== 'noxa-ratchet-core').sort((a,b) => `${a.name}@${a.version}`.localeCompare(`${b.name}@${b.version}`))) {
    licenses += `${pkg.name} ${pkg.version}\nLicense: ${pkg.license || 'see source'}\nSource: ${pkg.source}\n`;
    const files = notices(pkg.manifest_path).filter(([name]) => /^(LICENSE|COPYING|NOTICE)([._-]|$)/i.test(name)).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
    for (const [name, text] of files) licenses += `\n--- ${name} ---\n${text.replace(/\r\n/g,'\n')}\n`;
    licenses += '\n';
}
// Normalize layout only; retain the complete upstream notice text.
licenses = licenses.replace(/[ \t]+$/gm, '').trimEnd() + '\n';
const files = new Map([
    [path.join(dir,'core.wasm'),wasm],
    [path.join(dir,'core.sha256'),`${hash}\n`],
    [path.join(root,'mobile/src/core/ratchet-integrity.js'),`// Generated by scripts/build-ratchet-core.mjs; do not edit.\nexport const CORE_SHA256 = "${hash}";\n`],
    [path.join(dir,'THIRD_PARTY_LICENSES.txt'),licenses],
]);
for (const [name,data] of files) {
    const expected = Buffer.isBuffer(data) ? data : Buffer.from(data);
    if (process.argv.includes('--check')) {
        if (!fs.existsSync(name) || !fs.readFileSync(name).equals(expected)) throw new Error(`stale ratchet artifact: ${path.relative(root,name)}`);
    } else fs.writeFileSync(name,expected);
}
console.log(`ratchet core ${hash} (${wasm.length} bytes)`);
