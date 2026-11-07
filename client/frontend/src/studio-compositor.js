// studio-compositor.js — the Studio's draft preview and program renderer.
//
// Two separate canvases: the PREVIEW shows the editable draft scene, the
// PROGRAM shows only the immutable snapshot applied by take(). The program
// canvas feeds ONE stable captureStream track for the whole Studio session;
// a Take or transition changes pixels, never the track, so the encrypted
// screen publication (Task 2) is never renegotiated or replaced by a scene
// change. Sources that are missing, ended, revoked or muted render as a
// neutral box; a stale video frame is never drawn.

import { captureRefs } from "./studio-state.js";
import { StudioGLRenderer, cropToUV } from "./studio-gl.js";

export const STUDIO_FPS = 30;
export const PREVIEW_FPS = 15;
export const WEAK_PREVIEW_FPS = 5;
// Frames the WebGL program path is observed before a slow GPU drops to 2D.
export const GL_WATCH_FRAMES = 60;

// studioClock drives ticks from a dedicated worker (page timers are throttled
// to ~1 Hz in hidden/minimised windows; worker timers are not), falling back
// to a page interval. Returns a stop function that terminates the worker.
export function studioClock(fn, period) {
    try {
        // Literal form so the bundler emits the worker (see media-e2ee.js).
        const worker = new Worker(new URL("./studio-clock.worker.js", import.meta.url), { type: "module" });
        let fallback = null;
        worker.onmessage = () => fn();
        // A script that fails to load or run reports asynchronously: fall
        // back to the page timer so the program never freezes.
        worker.onerror = () => { worker.terminate(); if (fallback === null) fallback = setInterval(fn, period); };
        worker.postMessage({ period });
        return () => { worker.terminate(); clearInterval(fallback); fallback = -1; };
    } catch {
        const id = setInterval(fn, period);
        return () => clearInterval(id);
    }
}
const NEUTRAL = "#11161c";
const BACKGROUND = "#000000";

// outputSize: a stable 16:9 program size that respects the server's existing
// video limits (default 1280x720; never larger than the server allows).
export function outputSize(limits) {
    let width = 1280, height = 720;
    const maxWidth = limits?.video_max_width || 0, maxHeight = limits?.video_max_height || 0;
    // Either limit alone also scales the program down (16:9 kept).
    if (maxWidth > 0 || maxHeight > 0) {
        const scale = Math.min(1, maxWidth > 0 ? maxWidth / width : 1, maxHeight > 0 ? maxHeight / height : 1);
        width = Math.max(2, Math.floor(width * scale / 2) * 2);
        height = Math.max(2, Math.floor(height * scale / 2) * 2);
    }
    return { width, height };
}

// fitRect places content of (cw x ch) inside a box, preserving its aspect.
export function fitRect(box, cw, ch) {
    if (!(cw > 0 && ch > 0)) return box;
    const scale = Math.min(box.w / cw, box.h / ch);
    const w = cw * scale, h = ch * scale;
    return { x: box.x + (box.w - w) / 2, y: box.y + (box.h - h) / 2, w, h };
}

function wrapLines(ctx, text, width) {
    const lines = [];
    for (const paragraph of text.split("\n")) {
        let line = "";
        for (const word of paragraph.split(/(\s+)/)) {
            const candidate = line + word;
            if (line && ctx.measureText(candidate).width > width) { lines.push(line.trimEnd()); line = word.trimStart(); }
            else line = candidate;
        }
        lines.push(line);
    }
    return lines;
}

export class StudioEngine {
    constructor({ registry, assets, width, height, fps = STUDIO_FPS, document = globalThis.document,
        now = () => performance.now(), setTimer = (fn, ms) => studioClock(fn, ms), clearTimer = stop => stop?.(), label = () => "",
        renderer = "auto" }) {
        this.registry = registry;
        this.assets = assets;
        this.width = width;
        this.height = height;
        this.fps = fps;
        this.document = document;
        this.now = now;
        this.clearTimer = clearTimer;
        this.label = label;
        this.program = this.canvas(width, height);
        this.programContext = this.program.getContext("2d", { alpha: false });
        // (20) The program composites on the GPU when WebGL2 is available;
        // the 2D path stays the fallback (unavailable or lost context).
        this.gl = null;
        this.textCanvases = new Map();
        // "auto" refuses software WebGL and drops to 2D when GPU frames stay
        // slow; "webgl" forces the GPU path (tests and diagnostics).
        this.forceGL = renderer === "webgl";
        this.glFrames = 0;
        this.glCost = 0;
        if (renderer !== "2d") {
            try {
                this.gl = new StudioGLRenderer({ document, width, height, onLost: () => this.dropGL(), allowSoftware: this.forceGL });
            } catch {
                this.gl = null;
            }
        }
        this.preview = this.canvas(Math.max(2, Math.round(width / 2)), Math.max(2, Math.round(height / 2)));
        this.previewContext = this.preview.getContext("2d", { alpha: false });
        this.layers = null;
        this.live = null;
        this.outgoing = null;
        this.transition = null;
        this.draft = null;
        this.captureTrack = null;
        this.previewEvery = Math.round(fps / PREVIEW_FPS);
        this.ticks = 0;
        this.cost = 0;
        this.disposed = false;
        this.timer = setTimer(() => this.tick(), 1000 / fps);
        this.tick();
    }

    canvas(width, height) {
        const canvas = this.document.createElement("canvas");
        canvas.width = width;
        canvas.height = height;
        return canvas;
    }

    // setDraft shows the selected draft scene in the preview; every draft
    // scene keeps its captures (holder "draft").
    setDraft(scene, scenes = [scene]) {
        if (this.disposed) return;
        this.draft = scene ? structuredClone(scene) : null;
        this.registry.setHolder("draft", captureRefs(scenes));
        this.renderPreview();
    }

    // releaseDraft gives up every draft-only capture (panel closed while the
    // program stays live). Program captures keep their own holder.
    releaseDraft() {
        if (this.disposed) return;
        this.draft = null;
        this.registry.setHolder("draft", []);
        this.renderPreview();
    }

    // take puts a frozen snapshot on air: cut, or crossfade over ms.
    // A Take during a running crossfade completes the old one at once
    // (no third picture, no stuck layer) and starts from the current live.
    take(snapshot, transition = { kind: "cut", ms: 0 }) {
        if (this.disposed) return;
        if (!Object.isFrozen(snapshot)) throw new Error("program snapshots must be immutable");
        const previous = this.live;
        this.outgoing = null;
        this.transition = null;
        this.live = snapshot;
        if (previous && transition.kind === "fade" && transition.ms > 0) {
            this.outgoing = previous;
            this.transition = { start: this.now(), ms: transition.ms };
        }
        // Retain the old program before dropping its live holder. It may
        // already have been removed from every draft scene.
        this.registry.setHolder("outgoing", captureRefs([this.outgoing]));
        this.registry.setHolder("live", captureRefs([this.live]));
        this.renderProgram(this.now());
    }

    get transitioning() { return !!this.transition; }

    /** "webgl" or "2d": the renderer currently drawing the program. */
    get rendererKind() { return this.gl?.usable ? "webgl" : "2d"; }

    // dropGL switches the program to the 2D path for the rest of the session
    // (lost context or a failed frame). The output track is unaffected.
    dropGL() {
        const gl = this.gl;
        this.gl = null;
        this.textCanvases.clear();
        if (gl && !gl.lost) { try { gl.dispose(); } catch { /* already gone */ } }
    }

    // outputStream hands the publication its own clones of the one stable
    // program track and of the single routed audio track. The publication
    // owns (and stops) the clones; the Studio keeps the originals, so a
    // failed or stopped publication never kills the Studio's own output.
    outputStream(audioTrack = null) {
        if (!this.captureTrack || this.captureTrack.readyState === "ended") {
            this.captureTrack = this.program.captureStream(this.fps).getVideoTracks()[0];
        }
        return new MediaStream([this.captureTrack.clone(), audioTrack?.readyState === "live" ? audioTrack.clone() : null].filter(Boolean));
    }

    tick() {
        if (this.disposed) return;
        const started = this.now();
        this.renderProgram(started);
        if (++this.ticks % this.previewEvery === 0) this.renderPreview();
        // Weak devices: the preview slows down; the program rate never does.
        const spent = this.now() - started;
        this.cost = this.cost * 0.9 + spent * 0.1;
        const budget = 1000 / this.fps;
        this.previewEvery = Math.round(this.fps / (this.cost > budget * 0.5 ? WEAK_PREVIEW_FPS : PREVIEW_FPS));
    }

    // transitionProgress returns the crossfade position, or null when no
    // transition runs (a finished one is cleared here).
    transitionProgress(time) {
        if (!this.transition) return null;
        const progress = Math.min(1, Math.max(0, (time - this.transition.start) / this.transition.ms));
        if (progress < 1) return progress;
        this.transition = null;
        this.outgoing = null;
        this.registry.setHolder("outgoing", []);
        return null;
    }

    renderProgram(time) {
        if (this.gl?.usable) {
            try {
                const started = this.now();
                this.renderProgramGL(time);
                this.watchGL(this.now() - started);
                return;
            } catch {
                this.dropGL(); // draw this frame (and later ones) in 2D
            }
        }
        this.renderProgram2D(time);
    }

    // watchGL: in auto mode, a GPU path that keeps spending more than half a
    // frame on the main thread (an undetected software rasteriser, a stalled
    // driver) is abandoned for 2D after GL_WATCH_FRAMES frames.
    watchGL(spent) {
        if (this.forceGL) return;
        this.glFrames++;
        this.glCost = this.glFrames === 1 ? spent : this.glCost * 0.9 + spent * 0.1;
        if (this.glFrames >= GL_WATCH_FRAMES && this.glCost > 1000 / this.fps * 0.5) this.dropGL();
    }

    renderProgramGL(time) {
        const gl = this.gl;
        const progress = this.transitionProgress(time);
        const used = new Set();
        if (progress !== null) {
            gl.target(0);
            this.drawSceneGL(this.outgoing, used);
            gl.target(1);
            this.drawSceneGL(this.live, used);
            gl.target(null);
            gl.drawFramebuffer(0, 1);
            gl.drawFramebuffer(1, progress);
        } else {
            gl.target(null);
            this.drawSceneGL(this.live, used);
        }
        gl.endFrame();
        for (const id of this.textCanvases.keys()) if (!used.has(id)) this.textCanvases.delete(id);
        if (!gl.usable) throw new Error("WebGL context lost");
        this.programContext.drawImage(gl.canvas, 0, 0);
    }

    drawSceneGL(scene, used) {
        if (!scene) return;
        for (const source of scene.sources) {
            if (source.visible) this.drawSourceGL(source, used);
        }
    }

    drawSourceGL(source, used) {
        const gl = this.gl, width = this.width, height = this.height;
        const box = { x: source.x * width, y: source.y * height, w: source.w * width, h: source.h * height };
        if (!(box.w > 0 && box.h > 0)) return;
        if (source.kind === "text") {
            const text = this.textCanvas(source, box, height);
            used.add(source.id);
            if (text && gl.bindSource(text.canvas, { live: false, version: text.version })) gl.drawTexture(box, [0, 0, 1, 1]);
            return;
        }
        const media = source.kind === "image" ? this.assets?.bitmap(source.asset) : this.registry.drawable(source.capture);
        const mediaWidth = media?.videoWidth || media?.width || 0, mediaHeight = media?.videoHeight || media?.height || 0;
        if (!media || !mediaWidth || !mediaHeight) {
            gl.fill(box, NEUTRAL);
            return;
        }
        const { l, t, r, b } = source.crop;
        const target = fitRect(box, mediaWidth * (1 - l - r), mediaHeight * (1 - t - b));
        if (!gl.bindSource(media, { live: source.kind !== "image" })) {
            gl.fill(box, NEUTRAL);
            return;
        }
        gl.drawTexture(target, cropToUV(source.crop));
    }

    // textCanvas renders a text source into a canvas the size of its box
    // (which also clips it), re-rendered only when its content or size
    // changes. The version tells the GPU renderer to upload it again.
    textCanvas(source, box, height) {
        const w = Math.max(1, Math.round(box.w)), h = Math.max(1, Math.round(box.h));
        const key = JSON.stringify([source.text, source.size, source.color, source.background, w, h, height]);
        let entry = this.textCanvases.get(source.id);
        if (entry?.key === key) return entry;
        const canvas = entry?.canvas || this.canvas(w, h);
        canvas.width = w;
        canvas.height = h;
        const ctx = canvas.getContext("2d");
        if (!ctx) return null;
        ctx.clearRect(0, 0, w, h);
        this.drawText(ctx, source, { x: 0, y: 0, w, h }, height);
        entry = { key, canvas, version: (entry?.version || 0) + 1 };
        this.textCanvases.set(source.id, entry);
        return entry;
    }

    renderProgram2D(time) {
        const ctx = this.programContext;
        if (this.transition) {
            const progress = this.transitionProgress(time);
            if (progress !== null) {
                this.layers ||= [this.canvas(this.width, this.height), this.canvas(this.width, this.height)];
                const [from, to] = this.layers;
                this.drawScene(from.getContext("2d", { alpha: false }), this.outgoing, this.width, this.height, false);
                this.drawScene(to.getContext("2d", { alpha: false }), this.live, this.width, this.height, false);
                ctx.globalAlpha = 1;
                ctx.drawImage(from, 0, 0);
                ctx.globalAlpha = progress;
                ctx.drawImage(to, 0, 0);
                ctx.globalAlpha = 1;
                return;
            }
        }
        this.drawScene(ctx, this.live, this.width, this.height, false);
    }

    renderPreview() {
        this.drawScene(this.previewContext, this.draft, this.preview.width, this.preview.height, true);
    }

    drawScene(ctx, scene, width, height, preview) {
        ctx.globalAlpha = 1;
        ctx.fillStyle = BACKGROUND;
        ctx.fillRect(0, 0, width, height);
        if (!scene) return;
        for (const source of scene.sources) {
            if (source.visible) this.drawSource(ctx, source, width, height, preview);
        }
    }

    drawSource(ctx, source, width, height, preview) {
        const box = { x: source.x * width, y: source.y * height, w: source.w * width, h: source.h * height };
        ctx.save();
        ctx.beginPath();
        ctx.rect(box.x, box.y, box.w, box.h);
        ctx.clip();
        if (source.kind === "text") this.drawText(ctx, source, box, height);
        else {
            const media = source.kind === "image" ? this.assets?.bitmap(source.asset) : this.registry.drawable(source.capture);
            const mediaWidth = media?.videoWidth || media?.width || 0, mediaHeight = media?.videoHeight || media?.height || 0;
            if (!media || !mediaWidth || !mediaHeight) this.drawNeutral(ctx, box, preview, source);
            else {
                const { l, t, r, b } = source.crop;
                const sx = l * mediaWidth, sy = t * mediaHeight;
                const sw = mediaWidth * (1 - l - r), sh = mediaHeight * (1 - t - b);
                const target = fitRect(box, sw, sh);
                ctx.drawImage(media, sx, sy, sw, sh, target.x, target.y, target.w, target.h);
            }
        }
        ctx.restore();
    }

    drawText(ctx, source, box, height) {
        if (source.background) { ctx.fillStyle = source.background; ctx.fillRect(box.x, box.y, box.w, box.h); }
        const size = Math.max(4, source.size * height / 720);
        // System fonts only: no remote font is ever requested.
        ctx.font = `600 ${size}px system-ui, "Segoe UI", sans-serif`;
        ctx.fillStyle = source.color;
        ctx.textBaseline = "top";
        const padding = size * 0.25;
        let y = box.y + padding;
        for (const line of wrapLines(ctx, source.text, box.w - padding * 2)) {
            if (y > box.y + box.h) break;
            ctx.fillText(line, box.x + padding, y);
            y += size * 1.2;
        }
    }

    drawNeutral(ctx, box, preview, source) {
        ctx.fillStyle = NEUTRAL;
        ctx.fillRect(box.x, box.y, box.w, box.h);
        if (!preview) return;
        const text = this.label(source);
        if (!text) return;
        ctx.fillStyle = "#8a9dad";
        ctx.font = `${Math.max(9, Math.min(16, box.h / 6))}px system-ui, sans-serif`;
        ctx.textBaseline = "middle";
        ctx.fillText(text, box.x + 6, box.y + box.h / 2, Math.max(1, box.w - 12));
    }

    // dispose stops the render timer, the program track and every capture
    // and releases canvas backing stores (GPU memory).
    dispose() {
        if (this.disposed) return;
        this.disposed = true;
        this.clearTimer(this.timer);
        this.timer = null;
        this.captureTrack?.stop();
        this.captureTrack = null;
        this.registry.dispose();
        this.assets?.dispose();
        this.dropGL();
        this.live = this.outgoing = this.draft = this.transition = null;
        for (const canvas of [this.program, this.preview, ...(this.layers || [])]) { canvas.width = 0; canvas.height = 0; }
        this.layers = null;
    }
}
