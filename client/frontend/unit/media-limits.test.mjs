import { test } from "node:test";
import assert from "node:assert/strict";
import { videoConstraints, trackFitsVideoLimits, capVideoEncodings } from "../src/media-limits.js";

const limits = { video_max_width: 320, video_max_height: 200, video_max_bitrate: 1000000 };
test("capture bounds reduce either dimension without stretching the preferred aspect ratio", () => {
    assert.deepEqual(videoConstraints(640, 360, 30, limits), {
        width: { ideal: 320, max: 320 }, height: { ideal: 180, max: 200 }, frameRate: { ideal: 30 },
    });
    assert.equal(videoConstraints(360, 640, 60, limits).width.ideal, 112);
    assert.deepEqual(videoConstraints(640, 360, 30), {
        width: { ideal: 640 }, height: { ideal: 360 }, frameRate: { ideal: 30 },
    });
});
test("bounded tracks require actual positive dimensions within both limits", () => {
    for (const settings of [{}, { width: 321, height: 180 }, { width: 100, height: 201 }, { width: 0, height: 1 }]) {
        assert.equal(trackFitsVideoLimits({ getSettings: () => settings }, limits), false);
    }
    assert.equal(trackFitsVideoLimits({}, limits), false);
    assert.equal(trackFitsVideoLimits({ getSettings: () => ({ width: 320, height: 180 }) }, limits), true);
    assert.equal(trackFitsVideoLimits({}, {}), true);
});
test("camera layers and screen share divide one budget with RTP headroom", () => {
    const camera = [{ rid: "f" }, { rid: "h", scaleResolutionDownBy: 2 }, { rid: "q", scaleResolutionDownBy: 4 }];
    const screen = [{}];
    capVideoEncodings([{ encodings: camera }, { encodings: screen, preset: 1500000 }], limits, 0);
    assert.equal(camera.reduce((n, e) => n + e.maxBitrate, 0), 424998);
    assert.equal(screen[0].maxBitrate, 425000);
    assert.deepEqual(camera.map(e => e.scaleResolutionDownBy), [1, 2, 4]);
    capVideoEncodings([{ encodings: camera }], limits, 150000);
    assert.equal(camera[0].maxBitrate, 150000);
    assert.deepEqual(camera.map(e => e.active), [true, false, false]);
    capVideoEncodings([{ encodings: camera }], {}, 0);
    assert.deepEqual(camera.map(e => e.scaleResolutionDownBy), [1, 2, 4]);
    assert.equal(camera[0].maxBitrate, undefined);
    assert.equal(camera[1].active, true);
});
test("screen preset and tiny server budgets never multiply per layer", () => {
    const sources = [{ encodings: [{}, {}], preset: 100000 }];
    capVideoEncodings(sources, {}, 0);
    assert.deepEqual(sources[0].encodings.map(e => e.maxBitrate), [50000, 50000]);
    capVideoEncodings(sources, { video_max_bitrate: 1 }, 0);
    assert.equal(sources[0].encodings.some(e => e.active), false);
    capVideoEncodings([], limits, 0);
});

test("low-bandwidth mode divides its own ceiling across camera and screen", () => {
    const sources = [{ encodings: [{}, {}, {}] }, { encodings: [{}], preset: 1500000 }];
    capVideoEncodings(sources, {}, 150000);
    assert.equal(sources.flatMap(s => s.encodings).filter(e => e.active).reduce((n, e) => n + e.maxBitrate, 0), 150000);
});
