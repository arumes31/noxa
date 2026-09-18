// Development-only compatibility entry point. Master source recordings offline.
// Application builds never execute this script or download audio.
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
const root = fileURLToPath(new URL('../', import.meta.url));
const python = process.env.NOXA_AUDIO_PYTHON || fileURLToPath(new URL(
    process.platform === 'win32' ? '../.cache/noxa-audio-env/Scripts/python.exe' : '../.cache/noxa-audio-env/bin/python', import.meta.url));
const result = spawnSync(python, [fileURLToPath(new URL('./master-sounds.py', import.meta.url)), ...process.argv.slice(2)], { cwd: root, stdio: 'inherit' });
if (result.error) console.error('noXa offline audio authoring:', result.error.message);
process.exit(result.status ?? 1);
