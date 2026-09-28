import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { cameraConstraints, foregroundAlpha, cameraEffect, cameraBlurStrength, cameraBackgroundImage, applyCameraPreview } from "../src/camera-capture.js";

test("custom backgrounds reject external input and mirror only a local preview", () => {
    assert.equal(cameraBlurStrength({camera_blur_strength: 500}), 30);
    assert.equal(cameraBlurStrength({camera_blur_strength: -5}), 2);
    assert.equal(cameraBlurStrength({}), 14);
    assert.throws(() => cameraBackgroundImage({camera_background_image: "https://example.com/image.jpg"}));
    assert.equal(cameraBackgroundImage({camera_background_image: "data:image/png;base64,AAAA"}), "data:image/png;base64,AAAA");
    const preview = {style: {}};
    applyCameraPreview(preview, {camera_mirror_preview: true});
    assert.equal(preview.style.transform, "scaleX(-1)");
    applyCameraPreview(preview, {camera_mirror_preview: false});
    assert.equal(preview.style.transform, "");
});

test("camera selection is exact and dimensions honor server limits", () => {
    const constraints = cameraConstraints({ camera_device_id: "usb-camera", camera_fps: 60 }, { video_max_width: 320, video_max_height: 180 });
    assert.deepEqual(constraints.deviceId, { exact: "usb-camera" });
    assert.equal(constraints.width.max, 320);
    assert.equal(constraints.height.max, 180);
    assert.equal(constraints.frameRate.ideal, 60);
    assert.equal(cameraConstraints({}).deviceId, undefined);
});

test("foreground mask preserves the person and hides background with soft edges", () => {
    assert.equal(foregroundAlpha(0), 0);
    assert.equal(foregroundAlpha(1), 255);
    assert.ok(foregroundAlpha(0.5) > 0 && foregroundAlpha(0.5) < 255);
    assert.equal(foregroundAlpha(NaN), 0);
});

test("camera effects accept only bundled modes and backgrounds", () => {
    assert.deepEqual(cameraEffect({}), { mode: "none", background: "slate" });
    assert.deepEqual(cameraEffect({ camera_background: "replace", camera_background_scene: "warm" }), { mode: "replace", background: "warm" });
    assert.deepEqual(cameraEffect({ camera_background: "https://remote", camera_background_scene: "https://remote" }), { mode: "none", background: "slate" });
});

test("offline runtime assets match the pinned package and model checksum", async () => {
    const digest = async path => createHash("sha256").update(await readFile(new URL(path, import.meta.url))).digest("hex");
    for (const [bundled, installed] of [
        ["vision_bundle.js", "vision_bundle.cjs"],
        ["vision_wasm_nosimd_internal.js", "wasm/vision_wasm_nosimd_internal.js"],
        ["vision_wasm_nosimd_internal.wasm", "wasm/vision_wasm_nosimd_internal.wasm"],
    ]) {
        assert.equal(await digest("../public/camera/" + bundled), await digest("../node_modules/@mediapipe/tasks-vision/" + installed), "refresh bundled runtime when updating MediaPipe");
    }
    assert.equal(await digest("../public/camera/selfie_segmenter.tflite"), "191ac9529ae506ee0beefa6b2c945a172dab9d07d1e802a290a4e4038226658b");
});
