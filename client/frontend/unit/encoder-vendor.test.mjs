import assert from "node:assert/strict";
import test from "node:test";
import { encoderVendor } from "../src/video-sender-stats.js";
import { vendorLabel } from "../src/encoder-vendor.js";

test("only the coarse GPU vendor is derived from the encoder description", () => {
    assert.equal(encoderVendor("MediaFoundationVideoEncodeAccelerator (NVIDIA H.264 Encoder MFT)"), "nvidia");
    assert.equal(encoderVendor("MediaFoundationVideoEncodeAccelerator (AMDh264Encoder)"), "amd");
    assert.equal(encoderVendor("MediaFoundationVideoEncodeAccelerator (Samdung Encoder)"), "", "no match inside other words");
    assert.equal(encoderVendor("MediaFoundationVideoEncodeAccelerator (AMD AMF H264 Encoder)"), "amd");
    assert.equal(encoderVendor("MediaFoundationVideoEncodeAccelerator (Radeon Video Encoder)"), "amd");
    assert.equal(encoderVendor("MediaFoundationVideoEncodeAccelerator (Intel® Quick Sync Video H.264 Encoder MFT)"), "intel");
    for (const value of ["libvpx", "openh264", "ExternalEncoder", "", null, undefined, 42]) assert.equal(encoderVendor(value), "");
});

test("vendor labels are appended only for known vendors", () => {
    assert.equal(vendorLabel("nvidia"), " · NVIDIA");
    assert.equal(vendorLabel("amd"), " · AMD");
    assert.equal(vendorLabel("intel"), " · Intel");
    for (const value of ["", "toString", "constructor", "unknown", undefined]) assert.equal(vendorLabel(value), "");
});
