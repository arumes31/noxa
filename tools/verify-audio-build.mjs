// Offline build check: every registered recording must be present byte-for-byte.
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { createHash } from "node:crypto";
import { SOUND_URLS } from "../client/frontend/src/sound-catalog.js";
import { SPEECH_ASSETS } from "../client/frontend/src/speech-catalog.js";

const directory = new URL("../client/frontend/dist/assets/", import.meta.url);
const hash = bytes => createHash("sha256").update(bytes).digest("hex");
const bundledFiles = readdirSync(directory).filter(name => name.endsWith(".wav"));
const bundled = new Set(bundledFiles.map(name => hash(readFileSync(new URL(name, directory)))));
const sources = [...Object.values(SOUND_URLS), ...Object.values(SPEECH_ASSETS).flatMap(clips => Object.values(clips).map(clip => clip.url))];
for (const url of sources) assert.ok(bundled.has(hash(readFileSync(new URL(url)))), `Missing bundled audio: ${url}`);
assert.equal(bundledFiles.length, sources.length, "Duplicate or obsolete WAVs in production bundle");
assert.equal(bundled.size, sources.length, "Production WAVs must have unique content");
const notice = "noxa-audio-licenses.txt";
assert.equal(hash(readFileSync(new URL(`../client/frontend/public/${notice}`, import.meta.url))),
    hash(readFileSync(new URL(`../client/frontend/dist/${notice}`, import.meta.url))), "Missing bundled audio licensing notice");
console.log(`Verified all ${sources.length} noXa recordings and audio licensing notice in the production bundle.`);
