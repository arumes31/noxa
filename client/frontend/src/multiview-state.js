// multiview-state.js — pure stage/multiview state (design 45 stream stage).
//
// No DOM and no media here: the stream stage (stream-stage.js) renders this
// state, video-grid.js keeps owning tiles, receivers and quality. Keys are the
// receiver registry's "publisher|slot" ids, never browser track ids.

export const STAGE_MODES = Object.freeze(["grid", "focus", "stack"]);
export const AUDIO_FOLLOW = "follow";
// Non-focused stream audio plays at 15 % in multiview. This only scales the
// per-publisher screen-share gain; voice chains are never touched.
export const DUCK_LEVEL = 0.15;
export const HOTKEY_TILES = 5;
export const SPLIT_DEFAULT = 0.62;
export const SPLIT_MIN = 0.25;
export const SPLIT_MAX = 0.85;

const MODE_KEY = "noxa.stage.mode";
const THEATER_KEY = "noxa.stage.theater";
const PANEL_KEY = "noxa.stage.panel";
const SPLIT_KEY = "noxa.stage.split";

// stageKind maps the number of visible watched tiles to the stage surface.
export function stageKind(count) {
    return count >= 2 ? "multi" : count === 1 ? "single" : "none";
}

export function publisherOf(key) {
    const text = String(key);
    const cut = text.lastIndexOf("|");
    return cut < 0 ? text : text.slice(0, cut);
}

function read(storage, key) {
    try { return storage?.getItem(key) ?? null; } catch { return null; }
}

function write(storage, key, value) {
    try { storage?.setItem(key, value); } catch { /* private window or blocked storage */ }
}

// clampSplit bounds the stage share of the split view. Non-numbers reset.
export function clampSplit(value) {
    const number = Number(value);
    if (!Number.isFinite(number) || number <= 0) return SPLIT_DEFAULT;
    return Math.min(SPLIT_MAX, Math.max(SPLIT_MIN, number));
}

// shareAudioGains decides each publisher's screen-audio factor. The source
// publisher (the focused tile when audio follows focus) plays at full gain,
// every other watched publisher is ducked. A single tile or no source leaves
// all publishers untouched, so leaving multiview restores normal playback.
export function shareAudioGains(keys, source, duck = DUCK_LEVEL) {
    const gains = new Map();
    const publishers = [...new Set(keys.map(publisherOf))];
    if (keys.length < 2 || !source || !keys.includes(source)) return gains;
    const playing = publisherOf(source);
    for (const publisher of publishers) gains.set(publisher, publisher === playing ? 1 : duck);
    return gains;
}

// hotkeyIndex returns 0..4 for an unmodified 1-5 key press outside text
// entry, otherwise -1. Number-row and numpad digits both count.
export function hotkeyIndex(event) {
    if (!event || event.defaultPrevented || event.repeat || event.ctrlKey || event.altKey || event.metaKey || event.shiftKey) return -1;
    const target = event.target;
    if (target && (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName || ""))) return -1;
    const code = String(event.code || "");
    const digit = /^(Digit|Numpad)[1-5]$/.test(code) ? code.slice(-1) : /^[1-5]$/.test(event.key || "") ? event.key : "";
    return digit ? Number(digit) - 1 : -1;
}

// gridShape picks columns/rows for the grid layout: 2 → side by side,
// 3-4 → 2×2, 5-6 → 3×2, more → 3 columns that scroll.
export function gridShape(count) {
    if (count <= 1) return { cols: 1, rows: 1 };
    if (count === 2) return { cols: 2, rows: 1 };
    if (count <= 4) return { cols: 2, rows: 2 };
    if (count <= 6) return { cols: 3, rows: 2 };
    return { cols: 3, rows: Math.ceil(count / 3) };
}

// createStageState holds layout mode, order, focus, pins and the audio
// source. Mode, theater and panel persist per app session (sessionStorage,
// keyed by server tab); the split size persists across sessions.
export function createStageState({ session, local, scope = () => "" } = {}) {
    let order = [];
    let focus = null;
    let audio = AUDIO_FOLLOW;
    const pinned = new Set();
    const scoped = key => `${key}:${scope() || "default"}`;

    function mode() {
        const value = read(session, scoped(MODE_KEY));
        return STAGE_MODES.includes(value) ? value : "grid";
    }

    function setMode(next) {
        if (!STAGE_MODES.includes(next)) return mode();
        write(session, scoped(MODE_KEY), next);
        return next;
    }

    function theater() { return read(session, scoped(THEATER_KEY)) === "1"; }
    function setTheater(on) { write(session, scoped(THEATER_KEY), on ? "1" : "0"); return !!on; }

    function panel() {
        const value = read(session, scoped(PANEL_KEY)) || "";
        const [tab, collapsed] = value.split(":");
        return { tab: tab === "viewers" ? "viewers" : "chat", collapsed: collapsed === "1" };
    }

    function setPanel({ tab, collapsed }) {
        const current = panel();
        const next = { tab: tab === undefined ? current.tab : tab === "viewers" ? "viewers" : "chat",
            collapsed: collapsed === undefined ? current.collapsed : !!collapsed };
        write(session, scoped(PANEL_KEY), `${next.tab}:${next.collapsed ? "1" : "0"}`);
        return next;
    }

    function split() { return clampSplit(read(local, SPLIT_KEY)); }
    function setSplit(value) {
        const next = clampSplit(value);
        write(local, SPLIT_KEY, String(Math.round(next * 1000) / 1000));
        return next;
    }
    function resetSplit() {
        try { local?.removeItem(SPLIT_KEY); } catch { /* ignore */ }
        return SPLIT_DEFAULT;
    }

    // setTiles reconciles with the currently visible tiles: known tiles keep
    // their position, new ones append, pinned tiles lead. Focus and the
    // explicit audio source fall back when their tile disappears.
    function setTiles(keys) {
        const present = new Set(keys.map(String));
        order = order.filter(key => present.has(key));
        for (const key of present) if (!order.includes(key)) order.push(key);
        for (const key of [...pinned]) if (!present.has(key)) pinned.delete(key);
        order = [...order.filter(key => pinned.has(key)), ...order.filter(key => !pinned.has(key))];
        if (!present.has(focus)) focus = order[0] ?? null;
        if (audio !== AUDIO_FOLLOW && !present.has(audio)) audio = AUDIO_FOLLOW;
        return order.slice();
    }

    function setFocus(key) {
        if (order.includes(String(key))) focus = String(key);
        return focus;
    }

    // focusIndex is the 1-5 hotkey: the n-th tile in display order.
    function focusIndex(index) {
        const key = index >= 0 && index < HOTKEY_TILES ? order[index] : undefined;
        return key === undefined ? null : setFocus(key);
    }

    function togglePin(key) {
        key = String(key);
        if (!order.includes(key)) return false;
        if (pinned.has(key)) pinned.delete(key); else pinned.add(key);
        setTiles(order);
        return pinned.has(key);
    }

    // swap exchanges a tile with the focused one and focuses it, so the
    // filmstrip tile moves to the large view and the old focus takes its slot.
    function swap(key) {
        key = String(key);
        const from = order.indexOf(key), to = order.indexOf(focus);
        if (from < 0) return focus;
        if (to >= 0 && from !== to) [order[from], order[to]] = [order[to], order[from]];
        focus = key;
        return focus;
    }

    function setAudio(value) {
        audio = value === AUDIO_FOLLOW || !order.includes(String(value)) ? AUDIO_FOLLOW : String(value);
        return audio;
    }

    function audioSource() { return audio === AUDIO_FOLLOW ? focus : audio; }

    return {
        mode, setMode, theater, setTheater, panel, setPanel, split, setSplit, resetSplit,
        setTiles, setFocus, focusIndex, togglePin, swap, setAudio, audioSource,
        gains: (duck = DUCK_LEVEL) => shareAudioGains(order, audioSource(), duck),
        get order() { return order.slice(); },
        get focus() { return focus; },
        get audio() { return audio; },
        isPinned: key => pinned.has(String(key)),
    };
}
