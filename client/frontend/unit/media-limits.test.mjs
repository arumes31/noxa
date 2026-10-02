import { test } from "node:test";
import assert from "node:assert/strict";
import { videoConstraints, trackFitsVideoLimits, capVideoEncodings } from "../src/media-limits.js";
import { shareQuality, screenShareConstraints } from "../src/screen-share-quality.js";

test("screen resolutions have finite budgets and honor server bounds", () => {
    for (const [preset, width, height, bitrate] of [["qhd", 2560, 1440, 10000000], ["uhd", 3840, 2160, 20000000]]) {
        const profile = shareQuality(preset);
        assert.deepEqual(profile, { width, height, fps: 30, bitrate });
        assert.deepEqual(screenShareConstraints(profile), { width: { ideal: width, max: width }, height: { ideal: height, max: height }, frameRate: { ideal: 30, max: 30 } });
        assert.deepEqual(screenShareConstraints(profile, { video_max_width: 1280, video_max_height: 720 }).height, { ideal: 720, max: 720 });
    }
});

test("original capture removes size preferences while retaining server and frame-rate limits", () => {
    const profile = shareQuality("original");
    assert.deepEqual(screenShareConstraints(profile), { width: {}, height: {}, frameRate: { ideal: 30, max: 30 } });
    assert.deepEqual(screenShareConstraints(profile, { video_max_width: 1920, video_max_height: 1080 }), {
        width: { max: 1920 }, height: { max: 1080 }, frameRate: { ideal: 30, max: 30 },
    });
});

test("custom capture validates dimensions and frame rate before allocating a bounded budget", () => {
    assert.equal(shareQuality("custom", { width: 3440, height: 1440, fps: 30 }).bitrate, 12000000);
    assert.equal(shareQuality("custom", { width: 8192, height: 8192, fps: 60 }).bitrate, 40000000);
    for (const value of [0, 159, 8193, -1, 1920.5, NaN, Infinity, "1920"]) {
        assert.equal(shareQuality("custom", { width: value, height: 1080, fps: 30 }), null);
        assert.equal(shareQuality("custom", { width: 1920, height: value, fps: 30 }), null);
    }
    assert.equal(shareQuality("custom", { width: 1920, height: 1080, fps: 0 }), null);
});

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
