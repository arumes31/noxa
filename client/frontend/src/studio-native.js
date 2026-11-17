// studio-native.js — the Studio in Native capture mode (Windows desktop).
//
// Go's D3D11 compositor composes the program: display sources (Windows
// Graphics Capture) and cameras (Media Foundation) are native layers with
// their crop and position. Text, images and sources without a picture are
// drawn here, each run of them between two native layers into one
// transparent full-canvas overlay image, so the z-order stays exact. An
// overlay is rendered and sent only when its content changes.

const UNIT = value => Math.round(value * 10000) / 10000;

/**
 * Splits a program scene: visible display and camera sources that resolve
 * to a native capture become native layers; everything else visible joins
 * the overlay run above the last native layer.
 * resolve(source) returns { kind: "display", source: { monitor, window } }
 * or { kind: "camera", camera }, or null when the source has no native
 * capture (it is drawn as a neutral box in its overlay).
 */
export function nativeProgram(scene, resolve) {
    const layers = [], groups = [];
    let run = null;
    for (const source of scene?.sources || []) {
        if (!source.visible) continue;
        const native = source.kind === "display" || source.kind === "camera" ? resolve(source) : null;
        if (!native) {
            if (!run) { run = []; groups.push(run); layers.push({ kind: "overlay", overlay: groups.length - 1 }); }
            run.push(source);
            continue;
        }
        run = null;
        const { l, t, r, b } = source.crop || {};
        const layer = { kind: native.kind, x: UNIT(source.x), y: UNIT(source.y), w: UNIT(source.w), h: UNIT(source.h),
            crop: { l: l || 0, t: t || 0, r: r || 0, b: b || 0 } };
        if (native.kind === "display") Object.assign(layer, { source: native.source, audio: !!source.audio });
        else layer.camera = native.camera;
        layers.push(layer);
    }
    return { layers, groups };
}

/**
 * The content key of the overlay images: equal keys draw equal pixels.
 * ready(asset) tells whether an image asset has loaded (it draws neutral
 * until then, so loading changes the key).
 */
export function overlayKey(groups, { width, height, ready = () => true }) {
    return JSON.stringify([width, height, groups.map(group => group.map(s => [s.id, s.kind, s.x, s.y, s.w, s.h, s.crop,
        s.kind === "text" ? [s.text, s.size, s.color, s.background] : s.kind === "image" ? [s.asset, !!ready(s.asset)] : s.capture || ""]))]);
}

/** The scene message for Go (UpdateNativeStudioForTab / StartNativeScreenShareForTab). */
export function nativeScenePayload(take, layers, transition, overlays) {
    const fade = transition?.kind === "fade";
    return { take: String(take), layers, transition: { kind: fade ? "fade" : "cut", ms: fade ? Math.round(transition.ms) || 0 : 0 }, overlays };
}

/**
 * Renders each overlay group into a transparent width x height PNG (base64,
 * as Go's []byte expects) with the engine's own 2D drawing, so text and
 * images look the same as in the browser program.
 */
export function renderOverlays(engine, groups, width, height, document = globalThis.document) {
    return groups.map(group => {
        const canvas = document.createElement("canvas");
        canvas.width = width; canvas.height = height;
        const ctx = canvas.getContext("2d");
        for (const source of group) engine.drawSource(ctx, source, width, height, false);
        const png = canvas.toDataURL("image/png");
        canvas.width = 0; canvas.height = 0;
        return png.slice(png.indexOf(",") + 1);
    });
}

/**
 * Builds one throttled sender of the live program: send(force) compares the
 * program's layers and overlay key with the last message and only renders
 * and sends what changed; non-forced updates are coalesced to one per
 * `every` ms. build() returns { take, layers, groups, transition, key } or
 * null; post(payload) delivers it.
 */
export function createNativeSync({ build, render, post, every = 250, now = () => Date.now(), setTimer = setTimeout, clearTimer = clearTimeout }) {
    let last = "", lastAt = -Infinity, timer = null;
    // payload renders the current program, or null when nothing changed.
    const payload = (always = false) => {
        const program = build();
        if (!program) return null;
        const signature = JSON.stringify([program.take, program.layers, program.key]);
        if (signature === last && !always) return null;
        last = signature;
        lastAt = now();
        return nativeScenePayload(program.take, program.layers, program.transition, render(program.groups));
    };
    const flush = () => {
        timer = null;
        const message = payload();
        if (message) post(message);
    };
    return {
        /** The full current program (the first scene of a share), marked as sent. */
        initial: () => payload(true),
        send(force = false) {
            if (force) { clearTimer(timer); flush(); return; }
            if (timer) return;
            timer = setTimer(flush, Math.max(0, every - (now() - lastAt)));
        },
        reset() { clearTimer(timer); timer = null; last = ""; lastAt = -Infinity; },
    };
}
