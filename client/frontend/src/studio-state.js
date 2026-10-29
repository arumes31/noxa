// studio-state.js — the desktop Studio's validated scene model.
//
// Pure data: no DOM, no media. Every update returns a new studio object, so a
// program snapshot taken by take() can never change when the draft is edited.
// Capture sources carry a runtime-only `capture` reference (an id into the
// capture registry, never a MediaStream); serializeStudio() drops it, so a
// restored layout always needs an explicit recapture.

export const MAX_SCENES = 8;
export const MAX_SOURCES = 8;
export const TRANSITION_MIN_MS = 100;
export const TRANSITION_MAX_MS = 1500;
export const TRANSITION_DEFAULT_MS = 300;
export const MAX_TEXT_LENGTH = 280;
export const MAX_NAME_LENGTH = 48;
export const MIN_SIZE = 0.02;
export const MAX_CROP = 0.45;
export const SOURCE_KINDS = Object.freeze(["display", "camera", "image", "text"]);
export const CAPTURE_KINDS = Object.freeze(["display", "camera"]);
export const STUDIO_STORAGE_KEY = "noxa.studio.v1";

export class StudioStateError extends Error {
    constructor(code) {
        super(code);
        this.name = "StudioStateError";
        this.code = code;
    }
}

const ID = /^[a-z]{1,3}[0-9]{1,6}$/;
const ASSET = /^a-[0-9a-f]{16,64}$/;
const COLOR = /^#[0-9a-f]{6}$/;
// Control characters (except line breaks) and bidi overrides never reach a
// name or text source; text is drawn as canvas text, never parsed as markup.
// eslint-disable-next-line no-control-regex
const UNSAFE_TEXT = /[\u0000-\u0009\u000b-\u001f\u007f-\u009f‪-‮⁦-⁩]/g;

const clamp = (value, min, max, fallback) => {
    const number = typeof value === "number" && Number.isFinite(value) ? value : fallback;
    return Math.min(max, Math.max(min, number));
};
const round = value => Math.round(value * 10000) / 10000;

export function cleanText(value, max = MAX_TEXT_LENGTH, multiline = true) {
    let text = typeof value === "string" ? value : "";
    text = text.replace(/\r\n?/g, "\n").replace(UNSAFE_TEXT, "");
    if (!multiline) text = text.replace(/\n/g, " ").trim();
    return [...text].slice(0, max).join("");
}

function cleanName(value, fallback) {
    return cleanText(value, MAX_NAME_LENGTH, false) || fallback;
}

export function clampTransition(value = {}) {
    const kind = value?.kind === "cut" ? "cut" : "fade";
    const ms = Math.round(clamp(value?.ms, TRANSITION_MIN_MS, TRANSITION_MAX_MS, TRANSITION_DEFAULT_MS));
    return { kind, ms };
}

// normalizeGeometry keeps a source inside the canvas with a minimum size and
// a crop that always leaves at least 10% of the picture in each direction.
export function normalizeGeometry(source = {}) {
    const w = round(clamp(source.w, MIN_SIZE, 1, 0.5));
    const h = round(clamp(source.h, MIN_SIZE, 1, 0.5));
    const x = round(clamp(source.x, 0, 1 - w, 0));
    const y = round(clamp(source.y, 0, 1 - h, 0));
    const crop = source.crop || {};
    return {
        x, y, w, h,
        crop: {
            l: round(clamp(crop.l, 0, MAX_CROP, 0)), t: round(clamp(crop.t, 0, MAX_CROP, 0)),
            r: round(clamp(crop.r, 0, MAX_CROP, 0)), b: round(clamp(crop.b, 0, MAX_CROP, 0)),
        },
    };
}

// normalizeSource validates one source. Unknown kinds or fields are dropped;
// `runtime` keeps the capture reference for in-memory state only.
export function normalizeSource(raw, { runtime = true } = {}) {
    if (!raw || typeof raw !== "object" || !SOURCE_KINDS.includes(raw.kind) || !ID.test(String(raw.id))) return null;
    const source = { id: raw.id, kind: raw.kind, name: cleanName(raw.name, raw.kind), visible: raw.visible !== false, ...normalizeGeometry(raw) };
    if (raw.kind === "display") {
        source.surface = raw.surface === "window" ? "window" : "monitor";
        source.audio = raw.audio === true;
    } else if (raw.kind === "image") {
        if (!ASSET.test(String(raw.asset))) return null;
        source.asset = raw.asset;
    } else if (raw.kind === "text") {
        source.text = cleanText(raw.text);
        source.color = COLOR.test(String(raw.color)) ? raw.color : "#edf4f8";
        source.background = COLOR.test(String(raw.background)) ? raw.background : "";
        source.size = Math.round(clamp(raw.size, 8, 160, 48));
    }
    if (runtime && CAPTURE_KINDS.includes(raw.kind) && typeof raw.capture === "string" && raw.capture) source.capture = raw.capture;
    return source;
}

function normalizeScene(raw, options) {
    if (!raw || typeof raw !== "object" || !ID.test(String(raw.id))) return null;
    const seen = new Set();
    const sources = [];
    for (const item of Array.isArray(raw.sources) ? raw.sources : []) {
        const source = normalizeSource(item, options);
        if (!source || seen.has(source.id)) continue;
        seen.add(source.id);
        sources.push(source);
        if (sources.length === MAX_SOURCES) break;
    }
    return { id: raw.id, name: cleanName(raw.name, "Scene"), sources };
}

export function createStudio() {
    return { v: 1, next: 2, scenes: [{ id: "sc1", name: "Scene 1", sources: [] }], selected: "sc1", transition: clampTransition() };
}

// parseStudio accepts untrusted persisted JSON (or an object) and returns a
// valid studio. Anything malformed falls back to a fresh studio; runtime
// capture references never survive parsing.
export function parseStudio(input) {
    let raw = input;
    if (typeof input === "string") {
        if (input.length > 64 * 1024) return createStudio();
        try { raw = JSON.parse(input); } catch { return createStudio(); }
    }
    if (!raw || typeof raw !== "object" || raw.v !== 1) return createStudio();
    const scenes = [];
    const ids = new Set();
    for (const item of Array.isArray(raw.scenes) ? raw.scenes : []) {
        const scene = normalizeScene(item, { runtime: false });
        if (!scene || ids.has(scene.id)) continue;
        // Source IDs are unique across the studio.
        scene.sources = scene.sources.filter(source => !ids.has(source.id) && ids.add(source.id));
        ids.add(scene.id);
        scenes.push(scene);
        if (scenes.length === MAX_SCENES) break;
    }
    if (!scenes.length) return createStudio();
    const highest = Math.max(1, ...[...ids].map(id => Number(id.replace(/^[a-z]+/, "")) || 0));
    const next = Math.max(highest + 1, Number.isSafeInteger(raw.next) && raw.next <= 999999 ? raw.next : 0);
    const selected = scenes.some(scene => scene.id === raw.selected) ? raw.selected : scenes[0].id;
    return { v: 1, next, scenes, selected, transition: clampTransition(raw.transition) };
}

// serializeStudio is the only persisted form: metadata, text and local asset
// IDs. No capture reference, track, handle, key or permission is included.
export function serializeStudio(studio) {
    return JSON.stringify({
        v: 1, next: studio.next, selected: studio.selected, transition: clampTransition(studio.transition),
        scenes: studio.scenes.map(scene => ({ id: scene.id, name: scene.name,
            sources: scene.sources.map(source => normalizeSource(source, { runtime: false })) })),
    });
}

function allocate(studio, prefix) {
    if (studio.next > 999999) throw new StudioStateError("ids_exhausted");
    return [`${prefix}${studio.next}`, studio.next + 1];
}

export function selectedScene(studio) {
    return studio.scenes.find(scene => scene.id === studio.selected) || studio.scenes[0];
}

function sceneIndex(studio, sceneID) {
    const index = studio.scenes.findIndex(scene => scene.id === sceneID);
    if (index < 0) throw new StudioStateError("scene_missing");
    return index;
}

function withScene(studio, sceneID, update) {
    const index = sceneIndex(studio, sceneID);
    const scenes = studio.scenes.slice();
    scenes[index] = update(scenes[index]);
    return { ...studio, scenes };
}

export function addScene(studio, name) {
    if (studio.scenes.length >= MAX_SCENES) throw new StudioStateError("scene_limit");
    const [id, next] = allocate(studio, "sc");
    const scene = { id, name: cleanName(name, `Scene ${studio.scenes.length + 1}`), sources: [] };
    return { ...studio, next, scenes: [...studio.scenes, scene], selected: id };
}

export function renameScene(studio, sceneID, name) {
    return withScene(studio, sceneID, scene => ({ ...scene, name: cleanName(name, scene.name) }));
}

export function removeScene(studio, sceneID) {
    const index = sceneIndex(studio, sceneID);
    if (studio.scenes.length === 1) throw new StudioStateError("last_scene");
    const scenes = studio.scenes.filter(scene => scene.id !== sceneID);
    const selected = studio.selected === sceneID ? scenes[Math.min(index, scenes.length - 1)].id : studio.selected;
    return { ...studio, scenes, selected };
}

export function selectScene(studio, sceneID) {
    sceneIndex(studio, sceneID);
    return { ...studio, selected: sceneID };
}

export function setTransition(studio, transition) {
    return { ...studio, transition: clampTransition({ ...studio.transition, ...transition }) };
}

// addSource appends a source on top of the scene's z-order.
export function addSource(studio, sceneID, raw) {
    const scene = studio.scenes[sceneIndex(studio, sceneID)];
    if (scene.sources.length >= MAX_SOURCES) throw new StudioStateError("source_limit");
    const [id, next] = allocate(studio, "src");
    const source = normalizeSource({ x: 0, y: 0, w: 1, h: 1, ...raw, id });
    if (!source) throw new StudioStateError("source_invalid");
    return { studio: { ...withScene(studio, sceneID, item => ({ ...item, sources: [...item.sources, source] })), next }, source };
}

export function updateSource(studio, sceneID, sourceID, patch) {
    return withScene(studio, sceneID, scene => {
        const index = scene.sources.findIndex(source => source.id === sourceID);
        if (index < 0) throw new StudioStateError("source_missing");
        const current = scene.sources[index];
        // Kind, ID and asset are fixed; a capture reference changes only by recapture.
        const merged = { ...current, ...patch, crop: { ...current.crop, ...(patch?.crop || {}) },
            id: current.id, kind: current.kind, asset: current.asset, capture: Object.hasOwn(patch || {}, "capture") ? patch.capture : current.capture };
        const source = normalizeSource(merged);
        if (!source) throw new StudioStateError("source_invalid");
        const sources = scene.sources.slice();
        sources[index] = source;
        return { ...scene, sources };
    });
}

export function removeSource(studio, sceneID, sourceID) {
    return withScene(studio, sceneID, scene => {
        if (!scene.sources.some(source => source.id === sourceID)) throw new StudioStateError("source_missing");
        return { ...scene, sources: scene.sources.filter(source => source.id !== sourceID) };
    });
}

// moveSource changes z-order: "up"/"down" by one, "top"/"bottom" to the end.
// Index 0 is drawn first (bottom).
export function moveSource(studio, sceneID, sourceID, direction) {
    return withScene(studio, sceneID, scene => {
        const sources = scene.sources.slice();
        const index = sources.findIndex(source => source.id === sourceID);
        if (index < 0) throw new StudioStateError("source_missing");
        const target = { up: index + 1, down: index - 1, top: sources.length - 1, bottom: 0 }[direction];
        if (target === undefined) throw new StudioStateError("direction_invalid");
        const bounded = Math.min(sources.length - 1, Math.max(0, target));
        const [item] = sources.splice(index, 1);
        sources.splice(bounded, 0, item);
        return { ...scene, sources };
    });
}

function deepFreeze(value) {
    if (value && typeof value === "object" && !Object.isFrozen(value)) {
        Object.freeze(value);
        for (const item of Object.values(value)) deepFreeze(item);
    }
    return value;
}

// take returns the immutable program snapshot of the selected draft scene.
// Later draft edits produce new objects and cannot reach this snapshot.
export function take(studio) {
    const scene = selectedScene(studio);
    return deepFreeze(structuredClone(scene));
}

// captureRefs lists the capture registry IDs a set of scenes still needs.
export function captureRefs(scenes) {
    const refs = new Set();
    for (const scene of scenes || []) {
        if (!scene) continue;
        for (const source of scene.sources || []) if (source.capture) refs.add(source.capture);
    }
    return refs;
}

// assetRefs lists local image assets a studio still references.
export function assetRefs(studio, extra = []) {
    const refs = new Set();
    for (const scene of [...studio.scenes, ...extra]) {
        if (!scene) continue;
        for (const source of scene.sources) if (source.kind === "image") refs.add(source.asset);
    }
    return refs;
}
