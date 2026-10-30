import assert from "node:assert/strict";
import test from "node:test";
import { SoundEngine } from "../src/sound-engine.js";
import { createWaitingMelody, startUpdateMusic } from "../src/update-music.js";

const flush = () => new Promise(resolve => setImmediate(resolve));
function fixture() {
    const state = { settings: { play_sounds: true, sound_volume: 100 } };
    let tick, stopped = false, dnd = false;
    const changes = [], saved = new Map(), sources = [];
    const ctx = {
        state: "running", currentTime: 0, destination: {},
        createBuffer: (_channels, length, rate) => { const samples = new Float32Array(length); return { duration: length / rate, getChannelData: () => samples }; },
        createGain: () => ({ gain: { value: 1, setValueAtTime() {}, linearRampToValueAtTime() {} }, connect() {}, disconnect() {} }),
        createBufferSource: () => { const source = { connect: gain => gain, disconnect() {}, start() {}, stop() { source.onended?.(); } }; sources.push(source); return source; },
        resume: async () => {}, setSinkId: async () => {},
    };
    const engine = new SoundEngine({ definitions: {}, urls: {}, getState: () => state, isDND: () => dnd, createContext: () => ctx, load: () => assert.fail("music must not fetch"), warn() {} });
    const start = () => startUpdateMusic({ engine, storage: { getItem: key => saved.get(key), setItem: (key, value) => saved.set(key, value) },
        schedule: fn => { tick = fn; return 1; }, unschedule: () => { stopped = true; }, onChange: value => changes.push(value) });
    return { state, engine, ctx, changes, saved, sources, start, tick: async () => { tick(); await flush(); }, stopped: () => stopped, dnd: value => { dnd = value; } };
}

test("original melody has bounded quiet samples and click-free loop endpoints", () => {
    const f = fixture(), buffer = createWaitingMelody(f.ctx), samples = buffer.getChannelData(0);
    let peak = 0, energy = 0;
    for (const sample of samples) { assert.ok(Number.isFinite(sample)); peak = Math.max(peak, Math.abs(sample)); energy += sample * sample; }
    assert.equal(buffer.duration, 20); assert.equal(samples[0], 0); assert.equal(samples.at(-1), 0);
    assert.ok(peak < .1 && peak > .03); assert.ok(energy / samples.length > .00001);
});

test("music loops quietly, persists its own mute and stops without leaving sources", async () => {
    const f = fixture(), music = f.start(); await flush();
    assert.equal(f.engine.active.size, 1); assert.equal(f.sources[0].loop, true);
    assert.equal([...f.engine.active][0].baseVolume, .18);
    music.toggle(); assert.equal(f.engine.active.size, 0); assert.equal(f.changes.at(-1).muted, true);
    music.stop(); assert.equal(f.stopped(), true); assert.equal(f.engine.buffers.size, 0);
    const retry = f.start(); await flush(); assert.equal(f.engine.active.size, 0);
    retry.toggle(); await flush(); assert.equal(f.engine.active.size, 1);
    retry.stop(); await f.tick(); assert.equal(f.engine.active.size, 0);
});

test("music respects global sound, microphone, headset, DND and volume changes", async () => {
    const f = fixture(), music = f.start(); await flush();
    for (const property of ["muted", "deafened"]) {
        f.state[property] = true; await f.tick(); assert.equal(f.engine.active.size, 0);
        f.state[property] = false; await f.tick(); assert.equal(f.engine.active.size, 1);
    }
    for (const property of ["play_sounds", "effects_enabled"]) {
        f.state.settings[property] = false; await f.tick(); assert.equal(f.engine.active.size, 0);
        f.state.settings[property] = true; await f.tick(); assert.equal(f.engine.active.size, 1);
    }
    f.dnd(true); await f.tick(); assert.equal(f.engine.active.size, 0);
    f.dnd(false); f.state.settings.sound_volume = 50; await f.tick();
    assert.equal([...f.engine.active][0].baseVolume, .09);
    f.state.settings.sound_volume = 0; await f.tick(); assert.equal(f.engine.active.size, 0);
    music.stop();
});

test("finishing download during output setup cannot start music later", async () => {
    const f = fixture(); let finish;
    f.ctx.setSinkId = () => new Promise(resolve => { finish = resolve; });
    const music = f.start(); await flush(); music.stop(); finish(); await flush();
    assert.equal(f.engine.active.size, 0); assert.equal(f.sources.length, 0); assert.equal(f.engine.buffers.size, 0);
});

test("unavailable audio and suspended autoplay never block the download", async () => {
    const f = fixture(); f.engine.createContext = () => { throw Error("no device"); };
    const music = f.start(); await flush(); assert.equal(f.engine.active.size, 0); music.stop();
    const g = fixture(); g.ctx.state = "suspended"; g.ctx.resume = () => new Promise(() => {});
    const waiting = g.start(); await flush(); assert.equal(g.engine.active.size, 0);
    waiting.stop(); g.ctx.state = "running"; await g.tick(); assert.equal(g.engine.active.size, 0);
});
