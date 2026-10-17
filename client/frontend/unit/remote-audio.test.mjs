import assert from "node:assert/strict";
import { getEventListeners } from "node:events";
import { registerHooks } from "node:module";
import test from "node:test";

// Exercise playback ownership with real EventTargets, without browser audio or
// the unrelated UI/native dependencies imported by the playback controller.
const dependencies = new Map(Object.entries({
    "positional-audio.js": "export class SpatialVoice { attach() {} detach() {} }",
    "audio.js": `
        export const makeLimiter = ctx => ctx.createGain();
        export const personalVolumeGain = value => value;
        export const registerUserChain = () => {}, unregisterUserChain = () => {};
        export const createAudioLevelSampler = () => () => 0;
        export const getUserShareVolume = () => 1, isUserShareMuted = () => false;
        export const onShareAudioChange = () => {}, setDucking = () => {};
        export const attachUserNormalizer = (ctx, id, src) => src;
        export const detachUserNormalizer = () => {}, detachAllUserNormalizers = () => {};
        export const resumeAudioPlayback = async () => {};
        export const createRemoteAudioSource = (ctx, track) => ctx.source(track);
    `,
    "video.js": "export const parseTrackID = () => ({});",
    "i18n.js": "export const t = key => key;",
    "microphone-recovery.js": "export const watchAudioOutput = () => {};",
}).map(([name, source]) => [new URL(`../src/${name}`, import.meta.url).href, source]));
registerHooks({ load(url, context, next) {
    if (dependencies.has(url)) return { format: "module", source: dependencies.get(url), shortCircuit: true };
    return next(url, context);
} });
const { createRemoteAudio } = await import("../src/remote-audio.js");

function fixture(t, shared) {
    const sources = [], failures = [], nodes = [];
    const node = () => {
        const created = { gain: { value: 1 }, disconnected: false, connect() {}, disconnect() { this.disconnected = true; } };
        nodes.push(created);
        return created;
    };
    class Context {
        state = "running";
        destination = node();
        createGain = node;
        source(track) {
            const value = { src: node(), playback: {
                srcObject: { getAudioTracks: () => [track] }, paused: false,
                pause() { this.paused = true; }, play: async () => {},
            } };
            sources.push(value);
            return value;
        }
        async close() { this.state = "closed"; }
    }
    for (const [name, value] of Object.entries({ window: { AudioContext: Context, addEventListener() {} }, document: { addEventListener() {} } })) {
        const previous = Object.getOwnPropertyDescriptor(globalThis, name);
        Object.defineProperty(globalThis, name, { value, configurable: true });
        t.after(() => { if (previous) Object.defineProperty(globalThis, name, previous); else delete globalThis[name]; });
    }
    t.mock.method(globalThis, "setInterval", () => 0);
    const state = { settings: { voice_limiter: false }, trackUsers: new Map() };
    const audio = createRemoteAudio({ state, voiceEpoch: () => 1, toast: message => failures.push(message), sysMsg: message => failures.push(message) });
    t.after(() => { audio.detachRemoteAudio(); assert.deepEqual(failures, []); });
    const track = id => Object.assign(new EventTarget(), { id, kind: "audio", readyState: "live" });
    return {
        audio, sources, track, nodes,
        attach: (track, key = "publisher") => shared ? audio.attachShareAudio(track, key) : audio.attachRemoteAudio(track),
        detach: track => shared ? audio.detachShareAudio("publisher") : audio.detachRemoteTrack(track),
        attached: track => shared ? audio.shareAudio.get("publisher")?.track === track : audio.hasRemoteAudio(track),
    };
}

for (const shared of [false, true]) {
    const label = shared ? "shared audio" : "microphone";
    test(`${label} releases ended listeners and old playback on repeated live-track repair`, t => {
        const f = fixture(t, shared), track = f.track("same-track");
        for (let i = 0; i < 20; i++) {
            f.attach(track);
            assert.equal(getEventListeners(track, "ended").length, 1);
            assert.equal(f.attached(track), true);
            f.detach(track);
            assert.equal(getEventListeners(track, "ended").length, 0);
            assert.equal(f.attached(track), false);
            assert.equal(f.sources.at(-1).playback.srcObject, null);
            assert.equal(f.sources.at(-1).playback.paused, true);
            assert.equal(f.sources.at(-1).src.disconnected, true);
            assert.equal(track.readyState, "live", "repair must not stop the receiver track");
        }
    });

    if (!shared) {
        // Chromium derives a receiver's track.id from the a=msid it was created
        // with and keeps it when the media section is reused. After a speaker
        // leaves and rejoins, two LIVE receivers share an id: attaching one must
        // never tear down the other (one listener lost one speaker that way).
        test("microphone chains are keyed by receiver track object, not track.id", t => {
            const f = fixture(t, false), reused = f.track("speaker-a"), rejoined = f.track("speaker-a");
            f.audio.attachRemoteAudio(reused, null, "speaker-b"); // section reused for B
            f.audio.attachRemoteAudio(rejoined, null, "speaker-a"); // A back on a new section
            assert.equal(f.audio.hasRemoteAudio(reused, "speaker-b"), true, "B's chain survived A's attach");
            assert.equal(f.audio.hasRemoteAudio(rejoined, "speaker-a"), true);
            assert.equal(f.audio.hasRemoteAudio(reused, "speaker-a"), false, "attribution is per receiver");
            assert.equal(f.sources[0].src.disconnected, false);
            f.audio.detachRemoteTrack(rejoined);
            assert.equal(f.audio.hasRemoteAudio(reused, "speaker-b"), true, "detaching A leaves B playing");
            reused.readyState = "ended";
            reused.dispatchEvent(new Event("ended"));
            assert.equal(f.audio.hasRemoteAudio(reused), false);
        });
    }

    if (shared) test(`${label} ignores stale ended callbacks after a binding replacement`, t => {
        const f = fixture(t, shared), old = f.track("reused-id"), replacement = f.track("reused-id");
        f.attach(old);
        const stale = getEventListeners(old, "ended")[0];
        f.attach(replacement);
        assert.equal(getEventListeners(old, "ended").length, 0);
        stale(); // A callback already queued before the old binding was detached.
        old.dispatchEvent(new Event("ended"));
        assert.equal(f.attached(replacement), true);
        assert.equal(f.sources.at(-1).src.disconnected, false);
        assert.equal(getEventListeners(replacement, "ended").length, 1);
        f.audio.detachRemoteAudio();
        assert.equal(getEventListeners(replacement, "ended").length, 0);
        stale();
        assert.equal(f.attached(replacement), false);
    });

    test(`${label} ended event cleans only its current playback and listener`, t => {
        const f = fixture(t, shared), track = f.track("one"), other = f.track("two");
        f.attach(track);
        f.attach(other, "other-publisher");
        const [endedSource, healthySource] = f.sources;
        track.readyState = "ended";
        track.dispatchEvent(new Event("ended"));
        assert.equal(f.attached(track), false);
        assert.equal(getEventListeners(track, "ended").length, 0);
        assert.equal(endedSource.playback.srcObject, null);
        assert.equal(endedSource.src.disconnected, true);
        assert.equal(healthySource.src.disconnected, false);
        assert.equal(getEventListeners(other, "ended").length, 1);
        f.audio.detachRemoteAudio();
        assert.equal(getEventListeners(other, "ended").length, 0);
    });
}

// Multiview (design 45) ducks non-focused stream audio through the per-
// publisher screen-share gain. Voice chains must never follow it.
test("multiview stage gains scale only screen-share audio and never voice", t => {
    const f = fixture(t, true);
    f.audio.attachRemoteAudio(f.track("voice-a"), { client_id: "alice", unique_id: "u-alice" }, "alice");
    f.audio.attachRemoteAudio(f.track("voice-b"), { client_id: "bob", unique_id: "u-bob" }, "bob");
    const voiceNodes = f.nodes.slice();
    const voiceGains = voiceNodes.map(node => node.gain.value);
    f.audio.attachShareAudio(f.track("share-a"), "alice");
    f.audio.attachShareAudio(f.track("share-b"), "bob");
    f.audio.setShareAudioStageGains(new Map([["alice", 1], ["bob", 0.15]]));
    assert.equal(f.audio.shareAudio.get("alice").gain.gain.value, 1);
    assert.equal(f.audio.shareAudio.get("bob").gain.gain.value, 0.15);
    assert.deepEqual(voiceNodes.map(node => node.gain.value), voiceGains, "voice and master gains are untouched");
    // A share that (re)attaches while ducked keeps the stage factor.
    f.audio.attachShareAudio(f.track("share-b2"), "bob");
    assert.equal(f.audio.shareAudio.get("bob").gain.gain.value, 0.15);
    // Local mute still wins; volume and ducking multiply.
    f.audio.shareAudio.get("alice").volume = 50;
    f.audio.applyShareAudio("alice");
    assert.equal(f.audio.shareAudio.get("alice").gain.gain.value, 0.5);
    f.audio.setShareAudioStageGains(new Map([["alice", 0.15], ["bob", 1]]));
    assert.equal(f.audio.shareAudio.get("alice").gain.gain.value, 0.5 * 0.15);
    f.audio.setShareAudioStageGains(new Map([["alice", Number.NaN], ["bob", 7]]));
    assert.equal(f.audio.shareAudio.get("alice").gain.gain.value, 0.5, "invalid factors fall back to full gain");
    assert.equal(f.audio.shareAudio.get("bob").gain.gain.value, 1, "factors never amplify");
    f.audio.setShareAudioStageGains(new Map());
    assert.equal(f.audio.shareAudio.get("bob").gain.gain.value, 1, "leaving multiview restores full gain");
    assert.deepEqual(voiceNodes.map(node => node.gain.value), voiceGains);
});
