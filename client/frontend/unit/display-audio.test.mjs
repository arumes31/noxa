import test from "node:test";
import assert from "node:assert/strict";
import { displayAudioOptions, validateDisplayAudio } from "../src/display-audio.js";

function capture(surface, labels) {
    const video = { kind: "video", getSettings: () => ({ displaySurface: surface }), stop() { this.stopped = true; } };
    const audio = labels.map(label => ({ kind: "audio", label, readyState: "live", stop() { this.stopped = true; } }));
    const tracks = [video, ...audio];
    return { video, audio, stream: { getVideoTracks: () => [video], getAudioTracks: () => tracks.filter(t => t.kind === "audio"),
        getTracks: () => [...tracks], removeTrack: track => tracks.splice(tracks.indexOf(track), 1) } };
}

test("application capture excludes monitors and system audio", () => {
    assert.deepEqual(displayAudioOptions("application"), {
        audio: true, windowAudio: "window", systemAudio: "exclude", monitorTypeSurfaces: "exclude", surfaceSwitching: "exclude",
    });
});

test("application audio accepts only a confirmed application track from a window", () => {
    const { stream, audio } = capture("window", ["Application Audio"]);
    assert.equal(validateDisplayAudio(stream, "application"), "application");
    assert.equal(audio[0].stopped, undefined);
});

for (const [surface, labels] of [["window", ["System Audio"]], ["window", [""]], ["window", []],
    ["monitor", ["Application Audio"]], [undefined, ["Application Audio"]], ["browser", ["Tab audio"]],
    ["window", ["Application Audio", "System Audio"]]]) {
    test(`application capture drops ${surface}/${labels.join("+") || "missing audio"} without stopping video`, () => {
        const { stream, video, audio } = capture(surface, labels);
        assert.equal(validateDisplayAudio(stream, "application"), "none");
        assert.equal(video.stopped, undefined);
        assert.deepEqual(stream.getAudioTracks(), []);
        assert.ok(audio.every(track => track.stopped));
    });
}

test("ended application audio is removed without stopping video", () => {
    const { stream, video, audio } = capture("window", ["Application Audio"]);
    audio[0].readyState = "ended";
    assert.equal(validateDisplayAudio(stream, "application"), "none");
    assert.deepEqual(stream.getAudioTracks(), []);
    assert.equal(video.stopped, undefined);
});

test("no audio mode stops and removes unexpected audio without ending video", () => {
    const { stream, video, audio } = capture("monitor", ["System Audio"]);
    validateDisplayAudio(stream, "none");
    assert.deepEqual(stream.getAudioTracks(), []);
    assert.equal(audio[0].stopped, true);
    assert.equal(video.stopped, undefined);
});

test("system capture keeps the explicitly requested audio", () => {
    const { stream, audio } = capture("monitor", ["System Audio"]);
    validateDisplayAudio(stream, "system");
    assert.equal(audio[0].stopped, undefined);
    assert.equal(displayAudioOptions("system").windowAudio, "system");
    assert.equal(displayAudioOptions("none").audio, false);
});
