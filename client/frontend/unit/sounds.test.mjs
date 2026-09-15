import assert from "node:assert/strict";
import { test } from "node:test";
import { readFileSync, readdirSync } from "node:fs";
import { SOUND_DEFINITIONS, SOUND_EVENTS } from "../src/sound-catalog.js";
import { SoundEngine, soundVolume } from "../src/sound-engine.js";

function fixture(options = {}) {
    const sources = [];
    const params = () => ({ value: 1, setValueAtTime() {}, linearRampToValueAtTime() {} });
    const node = () => ({ gain: params(), connect(n) { return n; }, disconnect() { this.disconnected = true; } });
    const ctx = {
        state: "running", currentTime: 0, destination: {}, sinkId: "", sampleRate: 48000,
        createGain: node,
        createBufferSource() {
            const s = { ...node(), start() { this.started = true; }, stop() { this.onended?.(); } };
            sources.push(s); return s;
        },
        decodeAudioData: async () => ({ duration: .1 }),
        resume: async () => { ctx.state = "running"; },
        close: async () => { ctx.state = "closed"; },
        setSinkId: async (id) => { ctx.sinkId = id; },
    };
    const state = { settings: { play_sounds: true, sound_volume: 100, event_sounds: {} } };
    let now = 0;
    let dnd = false;
    const engine = new SoundEngine({
        getState: () => state, isDND: () => dnd, createContext: () => ctx,
        load: async () => new ArrayBuffer(8), now: () => now, warn() {}, ...options,
    });
    return { engine, state, ctx, sources, tick: (ms = 1000) => { now += ms; ctx.currentTime += ms / 1000; }, dnd: (v) => { dnd = v; } };
}

test("all 32 events have unique newly generated PCM assets with safe endpoints and levels", () => {
    assert.equal(SOUND_EVENTS.length, 32);
    const directory = new URL("../src/assets/sounds/", import.meta.url);
    assert.equal(readdirSync(directory).filter(x => x.endsWith(".wav")).length, 32);
    const hashes = new Set();
    for (const id of SOUND_EVENTS) {
        const def = SOUND_DEFINITIONS[id];
        assert.ok(def.duration >= .02 && def.duration <= .3);
        const wav = readFileSync(new URL(`${id}.wav`, directory));
        assert.equal(wav.toString("ascii", 0, 4), "RIFF");
        assert.equal(wav.readUInt16LE(22), 1);
        assert.equal(wav.readUInt32LE(24), 48000);
        assert.equal(wav.readUInt16LE(34), 16);
        assert.equal(wav.length, 44 + Math.round(def.duration * 48000) * 2);
        assert.equal(wav.readInt16LE(44), 0);
        assert.equal(wav.readInt16LE(wav.length - 2), 0);
        let peak = 0, sum = 0;
        for (let i = 44; i < wav.length; i += 2) { const s = wav.readInt16LE(i) / 32768; peak = Math.max(peak, Math.abs(s)); sum += s; }
        assert.ok(peak <= .1151, `${id} peak ${peak}`);
        assert.ok(Math.abs(sum / ((wav.length - 44) / 2)) < .0001, `${id} DC`);
        hashes.add(wav.toString("base64"));
    }
    assert.equal(hashes.size, 32);
});

test("global, event, DND and replay gates survive forced previews", async () => {
    const f = fixture(); await f.engine.preload();
    f.state.settings.play_sounds = false;
    assert.equal(f.engine.play("mention"), false);
    assert.equal(f.engine.play("mention", { force: true }), true);
    f.tick(); f.state.settings.event_sounds.dm = false;
    assert.equal(f.engine.play("dm", { force: true }), false);
    f.dnd(true); assert.equal(f.engine.play("poke", { force: true }), false);
    f.dnd(false); f.state.settings.play_sounds = true; f.state.replayingTabID = "old";
    assert.equal(f.engine.play("poke"), false);
    assert.equal(f.engine.play("poke", { force: true }), true);
});

test("volume clamps malformed inputs and preserves zero and 200 percent", () => {
    for (const [input, expected] of [[undefined,1],[null,1],[NaN,1],[Infinity,1],["bad",1],[-1,0],[0,0],[100,1],[200,2],[900,2]]) assert.equal(soundVolume(input), expected);
});

test("bursts are bounded, warnings are admitted and repeated PTT releases resources", async () => {
    const f = fixture(); await f.engine.preload();
    for (let i = 0; i < 100; i++) f.engine.play("user_join");
    assert.equal(f.sources.filter(s => s.started).length, 1);
    for (const name of ["mention", "dm", "poke", "connection_lost"]) f.engine.play(name);
    assert.ok(f.engine.active.size <= 4);
    for (let i = 0; i < 1000; i++) { f.tick(40); f.engine.play(i % 2 ? "ptt_off" : "ptt_on"); }
    assert.ok(f.engine.active.size <= 4);
    for (const source of f.sources) source.onended?.();
    assert.equal(f.engine.active.size, 0);
    assert.ok(f.sources.every(s => s.disconnected));
    await f.engine.dispose(); assert.equal(f.ctx.state, "closed");
});

test("unknown events, failed context creation and failed loads never throw", async () => {
    const f = fixture({ createContext() { throw new Error("unavailable"); } });
    assert.equal(f.engine.play("bogus"), false);
    assert.equal(f.engine.play("toString"), false);
    await f.engine.preload(); assert.equal(f.engine.play("dm"), false);
    const broken = fixture({ load: async () => { throw new Error("missing"); } });
    await broken.engine.preload(); assert.equal(broken.engine.play("ptt_on"), false);
    await broken.engine.dispose();
});

test("preview uses draft volume, and output routing follows selection and falls back", async () => {
    const f = fixture(); await f.engine.preload();
    assert.equal(f.engine.play("dm", { force: true, settings: { sound_volume: 0 } }), false);
    await f.engine.setOutput("speaker"); assert.equal(f.ctx.sinkId, "speaker");
    f.ctx.setSinkId = async (id) => { if (id) throw new Error("removed"); f.ctx.sinkId = id; };
    await f.engine.setOutput("missing"); assert.equal(f.ctx.sinkId, "");
});

test("production triggers only use registered events and obsolete sound paths are absent", () => {
    const sources = ["main.js", "notifications.js", "chat-ui.js", "sounds.js", "settings-ui.js"];
    const text = sources.map(name => readFileSync(new URL("../src/" + name, import.meta.url), "utf8")).join("\n");
    for (const match of text.matchAll(/(?:playEvent|notify)\("([a-z_]+)"/g)) {
        assert.ok(SOUND_EVENTS.includes(match[1]), match[1]);
    }
    assert.doesNotMatch(text, /createOscillator|channel_join\.mp3|playChannelJoin|custom_sounds|function beep|const CUES|const PACKS/);
    assert.ok(!readdirSync(new URL("../src/assets/", import.meta.url)).includes("channel_join.mp3"));
    const defaults = readFileSync(new URL("../../settings.go", import.meta.url), "utf8");
    for (const id of SOUND_EVENTS) assert.ok(defaults.includes('"' + id + '"'), id);
});

test("suspended playback is not queued and a later resumed PTT press works", async () => {
    const f = fixture(); await f.engine.preload();
    f.ctx.state = "suspended";
    let finish;
    f.ctx.resume = () => new Promise(resolve => { finish = () => { f.ctx.state = "running"; resolve(); }; });
    assert.equal(f.engine.play("ptt_on"), false);
    assert.equal(f.sources.length, 0);
    await Promise.resolve();
    finish(); await Promise.resolve();
    assert.equal(f.sources.length, 0);
    assert.equal(f.engine.play("ptt_off"), true);
    await f.engine.dispose();
});

test("dispose during decoding cannot repopulate buffers", async () => {
    let finish;
    const data = new Promise(resolve => { finish = resolve; });
    const f = fixture({ load: () => data });
    const loading = f.engine.preload();
    await f.engine.dispose();
    finish(new ArrayBuffer(8)); await loading;
    assert.equal(f.engine.buffers.size, 0);
    assert.equal(f.engine.play("ptt_on"), false);
});

test("rapid replacement reserves fading slots without stacking live sources", async () => {
    const f = fixture(); await f.engine.preload();
    f.ctx.createBufferSource = () => {
        const source = { connect(n) { return n; }, disconnect() {}, start(at) { this.at = at; }, stop(at) { this.until = at; } };
        return source;
    };
    for (const name of ["mention", "dm", "poke", "announcement"]) f.engine.play(name);
    for (let i = 0; i < 1000; i++) f.engine.play("connection_lost", { force: true });
    assert.equal(f.engine.active.size, 4);
    assert.equal(f.engine.retiring.size, 1);
    const entry = [...f.engine.active].find(e => e.family === "connection_lost");
    assert.equal(entry.startAt, .003);
    assert.ok([...f.engine.retiring].every(e => e.stopAt <= entry.startAt));
    const completed = [...f.engine.retiring][0];
    f.ctx.currentTime = .01;
    f.engine.play("ptt_on");
    assert.equal(f.engine.retiring.has(completed), false, "reclaim fades while ended events wait for the main thread");
    await f.engine.dispose();
    assert.equal(f.engine.retiring.size, 0);
});

test("resume failures are deduplicated and malformed JSON volume is harmless", async () => {
    let attempts = 0;
    const f = fixture(); f.ctx.state = "suspended";
    f.ctx.resume = async () => { attempts++; throw Error("autoplay"); };
    await Promise.all(Array.from({ length: 100 }, () => f.engine.resume()));
    assert.equal(attempts, 1);
    assert.equal(f.engine.warnings.size, 1);
    for (const input of [false, [], {}, { valueOf: null }]) assert.equal(soundVolume(input), 1);
    await f.engine.dispose();
});
