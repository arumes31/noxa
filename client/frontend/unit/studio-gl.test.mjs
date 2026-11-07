import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { StudioGLRenderer, TEXTURE_IDLE_FRAMES, cropToUV, parseHexColor } from "../src/studio-gl.js";
import { StudioEngine } from "../src/studio-compositor.js";

// fakeGL records the calls the renderer makes; every unknown method is a
// no-op returning a fresh object, constants resolve to their names.
function fakeGL(log, state = { lost: false }) {
    const handler = {
        get(target, name) {
            if (name in target) return target[name];
            if (typeof name === "string" && /^[A-Z_0-9]+$/.test(name)) return name;
            return (...args) => { log.push([name, ...args]); return {}; };
        },
    };
    return new Proxy({
        getShaderParameter: () => true,
        getError: () => state.uploadError ? (state.uploadError = false, "INVALID_VALUE") : "NO_ERROR",
        getProgramParameter: () => true,
        getUniformLocation: (program, name) => name,
        getAttribLocation: () => 0,
        isContextLost: () => state.lost,
        getExtension: () => ({ loseContext() { state.lost = true; } }),
        uniform4f: (name, ...values) => log.push(["uniform4f", name, ...values]),
        uniform1f: (name, value) => log.push(["uniform1f", name, value]),
        uniform1i: (name, value) => log.push(["uniform1i", name, value]),
        texImage2D: (...args) => {
            log.push(["texImage2D", args.at(-1)]);
            if (state.failNextUpload) { state.failNextUpload = false; state.uploadError = true; }
        },
        drawArrays: () => log.push(["draw"]),
    }, handler);
}

function fakeDocument({ log, glState, draws }) {
    const context2d = () => ({
        fillRect() {}, clearRect() {}, drawImage: (...args) => draws.push(args), save() {}, restore() {}, beginPath() {}, rect() {},
        clip() {}, fillText: (...args) => draws.push(["fillText", ...args]), measureText: text => ({ width: text.length * 8 }),
    });
    return {
        createElement: () => {
            const listeners = {};
            const canvas = {
                width: 0, height: 0,
                addEventListener: (type, fn) => { listeners[type] = fn; },
                fire: type => listeners[type]?.({ preventDefault() {} }),
                getContext: type => {
                    if (type === "webgl2") return glState.available ? fakeGL(log, glState) : null;
                    return context2d();
                },
                captureStream: () => ({ getVideoTracks: () => [{ readyState: "live", clone() { return this; }, stop() {} }] }),
            };
            return canvas;
        },
    };
}

// outputStream wraps tracks in a MediaStream, which Node lacks.
globalThis.MediaStream ||= class { constructor(tracks) { this.tracks = tracks; } getVideoTracks() { return this.tracks; } };

const registryWith = drawable => ({ setHolder() {}, drawable: () => drawable, dispose() {} });

describe("studio GL helpers", () => {
    it("parses hex colours and rejects anything else as black", () => {
        assert.deepEqual(parseHexColor("#ff8000"), [1, 128 / 255, 0, 1]);
        assert.deepEqual(parseHexColor("#0f0"), [0, 1, 0, 1]);
        assert.deepEqual(parseHexColor("red"), [0, 0, 0, 1]);
        assert.deepEqual(parseHexColor(undefined), [0, 0, 0, 1]);
    });

    it("maps a crop to the texture rectangle that remains", () => {
        assert.deepEqual(cropToUV({ l: 0.1, t: 0.2, r: 0.3, b: 0 }), [0.1, 0.2, 0.7, 1]);
        assert.deepEqual(cropToUV(), [0, 0, 1, 1]);
    });
});

describe("studio GL renderer", () => {
    it("throws without WebGL2 so the engine can fall back", () => {
        const doc = fakeDocument({ log: [], glState: { available: false }, draws: [] });
        assert.throws(() => new StudioGLRenderer({ document: doc, width: 64, height: 36 }), /WebGL2/);
    });

    it("re-uploads live sources every frame but static ones only on a new version", () => {
        const log = [];
        const doc = fakeDocument({ log, glState: { available: true }, draws: [] });
        const gl = new StudioGLRenderer({ document: doc, width: 64, height: 36 });
        const video = { id: "video" }, text = { id: "text" };
        gl.bindSource(video);
        gl.bindSource(video);
        gl.bindSource(text, { live: false, version: 1 });
        gl.bindSource(text, { live: false, version: 1 });
        gl.bindSource(text, { live: false, version: 2 });
        const uploads = log.filter(call => call[0] === "texImage2D").map(call => call[1]?.id);
        assert.deepEqual(uploads, ["video", "video", "text", "text"]);
    });

    it("deletes textures of sources that stopped being drawn", () => {
        const log = [];
        const doc = fakeDocument({ log, glState: { available: true }, draws: [] });
        const gl = new StudioGLRenderer({ document: doc, width: 64, height: 36 });
        const kept = {}, dropped = {};
        gl.bindSource(kept);
        gl.bindSource(dropped);
        for (let i = 0; i <= TEXTURE_IDLE_FRAMES; i++) {
            gl.bindSource(kept);
            gl.endFrame();
        }
        assert.equal(gl.textures.has(kept), true);
        assert.equal(gl.textures.has(dropped), false);
    });
});

describe("studio engine on WebGL", () => {
    const frozenScene = sources => Object.freeze({ id: "s", sources: sources.map(source => Object.freeze(source)) });

    it("draws a cropped source fitted into its box and copies the frame to the program canvas", () => {
        const log = [], draws = [];
        const glState = { available: true, lost: false };
        const video = { videoWidth: 200, videoHeight: 100 };
        const engine = new StudioEngine({ registry: registryWith(video), width: 64, height: 36,
            document: fakeDocument({ log, glState, draws }), now: () => 0, setTimer: () => 1, clearTimer() {} });
        assert.equal(engine.rendererKind, "webgl");
        log.length = 0;
        draws.length = 0;
        // Box: left half (32x36). Cropped media 100x100 -> fitted 32x32 centred vertically.
        engine.take(frozenScene([{ id: "a", kind: "display", capture: "c", visible: true, x: 0, y: 0, w: 0.5, h: 1, crop: { l: 0.25, t: 0, r: 0.25, b: 0 } }]));
        const dest = log.filter(call => call[0] === "uniform4f" && call[1] === "u_dest").at(-1);
        const uv = log.filter(call => call[0] === "uniform4f" && call[1] === "u_src").at(-1);
        assert.deepEqual(dest.slice(2), [0, 2, 32, 32]);
        assert.deepEqual(uv.slice(2), [0.25, 0, 0.75, 1]);
        assert.equal(draws.length, 1, "one copy of the GL frame onto the 2D program canvas");
        assert.equal(draws[0][0], engine.gl.canvas);
    });

    it("draws a missing source as a neutral box", () => {
        const log = [], draws = [];
        const engine = new StudioEngine({ registry: registryWith(null), width: 64, height: 36,
            document: fakeDocument({ log, glState: { available: true }, draws }), now: () => 0, setTimer: () => 1, clearTimer() {} });
        log.length = 0;
        engine.take(frozenScene([{ id: "a", kind: "display", capture: "c", visible: true, x: 0, y: 0, w: 1, h: 1, crop: { l: 0, t: 0, r: 0, b: 0 } }]));
        assert.ok(log.some(call => call[0] === "uniform1i" && call[1] === "u_solid" && call[2] === 1));
        assert.ok(log.some(call => call[0] === "uniform4f" && call[1] === "u_color"));
    });

    it("crossfades through two framebuffers with the progress as alpha", () => {
        const log = [], draws = [];
        let clock = 0;
        const engine = new StudioEngine({ registry: registryWith({ videoWidth: 64, videoHeight: 36 }), width: 64, height: 36,
            document: fakeDocument({ log, glState: { available: true }, draws }), now: () => clock, setTimer: () => 1, clearTimer() {} });
        const scene = id => frozenScene([{ id, kind: "display", capture: id, visible: true, x: 0, y: 0, w: 1, h: 1, crop: { l: 0, t: 0, r: 0, b: 0 } }]);
        engine.take(scene("a"));
        log.length = 0;
        engine.take(scene("b"), { kind: "fade", ms: 400 });
        clock = 100;
        log.length = 0;
        engine.tick();
        const alphas = log.filter(call => call[0] === "uniform1f" && call[1] === "u_alpha").map(call => call[2]);
        assert.deepEqual(alphas.slice(-2), [1, 0.25], "outgoing at full alpha, incoming at the fade position");
        assert.ok(log.some(call => call[0] === "bindFramebuffer"));
    });

    it("falls back to 2D for the rest of the session when the context is lost", () => {
        const log = [], draws = [];
        const glState = { available: true, lost: false };
        const engine = new StudioEngine({ registry: registryWith({ videoWidth: 64, videoHeight: 36 }), width: 64, height: 36,
            document: fakeDocument({ log, glState, draws }), now: () => 0, setTimer: () => 1, clearTimer() {} });
        const track = engine.outputStream().getVideoTracks()[0];
        const glCanvas = engine.gl.canvas;
        glState.lost = true;
        glCanvas.fire("webglcontextlost");
        assert.equal(engine.rendererKind, "2d");
        draws.length = 0;
        engine.take(frozenScene([{ id: "a", kind: "display", capture: "c", visible: true, x: 0, y: 0, w: 1, h: 1, crop: { l: 0, t: 0, r: 0, b: 0 } }]));
        assert.ok(draws.length > 0 && draws.every(call => call[0] !== glCanvas), "2D draws the media directly");
        assert.equal(engine.outputStream().getVideoTracks()[0], track, "the program track is unchanged");
    });

    it("uses 2D when asked to, even with WebGL available", () => {
        const engine = new StudioEngine({ registry: registryWith(null), width: 64, height: 36, renderer: "2d",
            document: fakeDocument({ log: [], glState: { available: true }, draws: [] }), now: () => 0, setTimer: () => 1, clearTimer() {} });
        assert.equal(engine.rendererKind, "2d");
    });
});

describe("studio GL renderer selection", () => {
    it("names software rasterisers", async () => {
        const { isSoftwareRenderer } = await import("../src/studio-gl.js");
        assert.equal(isSoftwareRenderer("ANGLE (Google, Vulkan 1.3.0 (SwiftShader Device (Subzero)), SwiftShader driver)"), true);
        assert.equal(isSoftwareRenderer("llvmpipe (LLVM 15.0.7, 256 bits)"), true);
        assert.equal(isSoftwareRenderer("ANGLE (Microsoft, Microsoft Basic Render Driver Direct3D11)"), true);
        assert.equal(isSoftwareRenderer("ANGLE (Intel, Intel(R) Iris(R) Xe Graphics Direct3D11 vs_5_0 ps_5_0, D3D11)"), false);
        assert.equal(isSoftwareRenderer("ANGLE (NVIDIA, NVIDIA GeForce RTX 4070 Direct3D11)"), false);
    });

    it("auto mode drops a slow GPU path to 2D after the watch window; forced WebGL stays", async () => {
        const { GL_WATCH_FRAMES } = await import("../src/studio-compositor.js");
        let clock = 0;
        const slowNow = () => (clock += 20); // every measured GL frame costs 20 ms > half of 33 ms
        const make = renderer => new StudioEngine({ registry: registryWith({ videoWidth: 64, videoHeight: 36 }), width: 64, height: 36, renderer,
            document: fakeDocument({ log: [], glState: { available: true }, draws: [] }), now: slowNow, setTimer: () => 1, clearTimer() {} });
        const auto = make("auto"), forced = make("webgl");
        for (let i = 0; i < GL_WATCH_FRAMES + 1; i++) { auto.renderProgram(clock); forced.renderProgram(clock); }
        assert.equal(auto.rendererKind, "2d");
        assert.equal(forced.rendererKind, "webgl");
    });
});

describe("studio GL upload errors", () => {
    it("reports a failed first upload and retries it on the next frame", () => {
        const log = [];
        const glState = { available: true, lost: false };
        const doc = fakeDocument({ log, glState, draws: [] });
        const gl = new StudioGLRenderer({ document: doc, width: 64, height: 36 });
        const source = { width: 32, height: 16 };
        glState.failNextUpload = true;
        assert.equal(gl.bindSource(source), false, "an upload that set a GL error is not drawn");
        assert.equal(gl.bindSource(source), true, "the size is checked again until an upload succeeds");
    });

    it("releases the context when the shaders fail to build", () => {
        let released = false;
        const doc = { createElement: () => ({ width: 0, height: 0, addEventListener() {},
            getContext: () => new Proxy({
                getShaderParameter: () => false, getShaderInfoLog: () => "broken", getError: () => "NO_ERROR",
                getExtension: name => name === "WEBGL_lose_context" ? { loseContext() { released = true; } } : null,
                getParameter: () => "GPU",
            }, { get: (target, name) => name in target ? target[name] : (typeof name === "string" && /^[A-Z_0-9]+$/.test(name) ? name : () => ({})) }) }) };
        assert.throws(() => new StudioGLRenderer({ document: doc, width: 64, height: 36 }), /broken/);
        assert.equal(released, true);
    });
});
