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

export const STUDIO_FPS = 30;
export const PREVIEW_FPS = 15;
export const WEAK_PREVIEW_FPS = 5;

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
        now = () => performance.now(), setTimer = (fn, ms) => studioClock(fn, ms), clearTimer = stop => stop?.(), label = () => "" }) {
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

    renderProgram(time) {
        const ctx = this.programContext;
        if (this.transition) {
            const progress = Math.min(1, Math.max(0, (time - this.transition.start) / this.transition.ms));
            if (progress >= 1) {
                this.transition = null;
                this.outgoing = null;
                this.registry.setHolder("outgoing", []);
            } else {
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
        this.live = this.outgoing = this.draft = this.transition = null;
        for (const canvas of [this.program, this.preview, ...(this.layers || [])]) { canvas.width = 0; canvas.height = 0; }
        this.layers = null;
    }
}
