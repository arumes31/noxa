// fullscreen-hud-state.js — pure state for the in-app fullscreen stream HUD
// (design 45 "Fullscreen HUD", "Fullscreen (idle)", "Fullscreen multiview",
// "Fullscreen whisper").
//
// No DOM, no media and no voice here: fullscreen-hud.js renders this state.
// Timers are injected so the unit tests drive them without real time.
import {
    OVERLAY_ANCHORS, anchorPlacement, gameUIPreset, placementAllowed,
    placementOf, placementRect, presetZones, zonesHit,
} from "./overlay-zones.js";

export const HUD_HIDE_MS = 2500;
export const PEEK_MS = 6000;
export const PEEK_MAX = 3;
export const QUICK_MESSAGES = 4;
export const QUICK_IDLE_MS = 15000;
export const PING_GOOD_MS = 100;
export const PING_FAIR_MS = 250;

const defaultClock = {
    now: () => Date.now(),
    schedule: (callback, ms) => setTimeout(callback, ms),
    cancel: handle => clearTimeout(handle),
};

// createAutoHide is the HUD visibility timer. Pointer movement or a key shows
// the HUD and restarts a 2.5 s countdown; while something holds it (pointer
// over the dock, keyboard focus inside it, an open menu) it stays visible and
// the countdown restarts on release.
export function createAutoHide({ delay = HUD_HIDE_MS, clock = defaultClock, onChange = () => {} } = {}) {
    let visible = false, timer = null;
    const holds = new Set();

    function set(next) {
        if (next === visible) return;
        visible = next;
        onChange(visible);
    }

    function arm() {
        if (timer !== null) clock.cancel(timer);
        timer = null;
        if (holds.size) return;
        timer = clock.schedule(() => { timer = null; if (!holds.size) set(false); }, delay);
    }

    return {
        activity() { set(true); arm(); },
        hold(reason) { holds.add(reason); set(true); if (timer !== null) { clock.cancel(timer); timer = null; } },
        release(reason) { if (holds.delete(reason) && visible) arm(); },
        hide() { holds.clear(); if (timer !== null) clock.cancel(timer); timer = null; set(false); },
        reset() { holds.clear(); if (timer !== null) clock.cancel(timer); timer = null; visible = false; },
        get visible() { return visible; },
        get held() { return holds.size > 0; },
    };
}

// createChatPeek queues new channel messages under the speaker list. Each
// one stays for ~6 s; at most three are shown, the oldest makes room first.
export function createChatPeek({ duration = PEEK_MS, max = PEEK_MAX, clock = defaultClock, onChange = () => {} } = {}) {
    let items = [];
    const timers = new Map();
    let seq = 0;

    function drop(id) {
        const timer = timers.get(id);
        if (timer !== undefined) clock.cancel(timer);
        timers.delete(id);
        const before = items.length;
        items = items.filter(item => item.id !== id);
        return items.length !== before;
    }

    return {
        push(message) {
            const key = message.key ? String(message.key) : "";
            if (key && items.some(item => item.key === key)) return null;
            const item = { ...message, key, id: ++seq, at: clock.now() };
            items.push(item);
            while (items.length > max) drop(items[0].id);
            timers.set(item.id, clock.schedule(() => { timers.delete(item.id); if (drop(item.id)) onChange(items.slice()); }, duration));
            onChange(items.slice());
            return item.id;
        },
        dismiss(id) { if (drop(id)) onChange(items.slice()); },
        clear() {
            for (const timer of timers.values()) clock.cancel(timer);
            timers.clear();
            if (!items.length) return;
            items = [];
            onChange([]);
        },
        get items() { return items.slice(); },
    };
}

// createQuickReply is the fullscreen whisper / DM bubble. An incoming direct
// message opens the bubble for its sender unless the user is already replying
// to someone else; then it waits as unread. The chat drawer (C) reuses the
// bubble with the voice channel as target.
export function createQuickReply({ keep = QUICK_MESSAGES, idle = QUICK_IDLE_MS, clock = defaultClock, onChange = () => {} } = {}) {
    let target = null;           // { kind: "dm", uid, name } | { kind: "channel", id, name }
    let messages = [];
    let engaged = false;         // the reply field has focus or a draft
    let timer = null;
    const threads = new Map();   // uid -> { name, messages, unread }

    const emit = () => onChange(snapshot());

    function snapshot() {
        return {
            open: !!target,
            target: target ? { ...target } : null,
            messages: messages.slice(),
            unread: [...threads].filter(([uid, thread]) => thread.unread > 0 && !(target?.kind === "dm" && target.uid === uid))
                .map(([uid, thread]) => ({ uid, name: thread.name, count: thread.unread, at: thread.at })),
        };
    }

    function armIdle() {
        if (timer !== null) clock.cancel(timer);
        timer = null;
        if (!target || engaged) return;
        timer = clock.schedule(() => { timer = null; if (!engaged) close(); }, idle);
    }

    function thread(uid, name) {
        let item = threads.get(uid);
        if (!item) { item = { name, messages: [], unread: 0, at: 0 }; threads.set(uid, item); }
        if (name) item.name = name;
        return item;
    }

    function openDM(uid, name) {
        const item = thread(uid, name);
        item.unread = 0;
        target = { kind: "dm", uid, name: item.name };
        messages = item.messages.slice(-keep);
    }

    function close() {
        if (timer !== null) clock.cancel(timer);
        timer = null;
        target = null;
        messages = [];
        engaged = false;
        emit();
    }

    return {
        // receive records an incoming direct message and opens it if nothing
        // else is being answered. Returns "open" or "queued".
        receive({ uid, name, text, at = clock.now() }) {
            if (!uid) return "ignored";
            const item = thread(uid, name);
            item.messages = [...item.messages, { text, self: false, at }].slice(-keep);
            item.at = at;
            const showing = target?.kind === "dm" && target.uid === uid;
            if (showing) {
                messages = item.messages.slice();
                armIdle();
                emit();
                return "open";
            }
            if (target && engaged) {
                item.unread++;
                emit();
                return "queued";
            }
            // An untouched bubble gives way to the newer sender; the
            // conversation it showed stays reachable as unread (Alt+R).
            if (target?.kind === "dm") {
                const previous = threads.get(target.uid);
                if (previous && previous.messages.at(-1)?.self === false) previous.unread = Math.max(1, previous.unread);
            }
            openDM(uid, name);
            armIdle();
            emit();
            return "open";
        },
        // sent appends the user's own reply to the open conversation.
        sent(text, at = clock.now()) {
            if (!target) return;
            const message = { text, self: true, at };
            if (target.kind === "dm") {
                const item = thread(target.uid, target.name);
                item.messages = [...item.messages, message].slice(-keep);
                messages = item.messages.slice();
            } else {
                messages = [...messages, message].slice(-keep);
            }
            emit();
        },
        // openLatest answers the newest waiting conversation (Alt+R).
        openLatest() {
            const waiting = [...threads].filter(([uid, item]) => item.unread > 0 && !(target?.kind === "dm" && target.uid === uid))
                .sort((a, b) => b[1].at - a[1].at);
            const pick = waiting[0] || (target ? null : [...threads].sort((a, b) => b[1].at - a[1].at)[0]);
            if (!pick) return target ? { ...target } : null;
            openDM(pick[0], pick[1].name);
            engaged = false;
            armIdle();
            emit();
            return { ...target };
        },
        openChannel({ id, name, recent = [] }) {
            target = { kind: "channel", id, name };
            messages = recent.slice(-keep);
            engaged = false;
            armIdle();
            emit();
        },
        channelMessage(message) {
            if (target?.kind !== "channel") return;
            messages = [...messages, message].slice(-keep);
            armIdle();
            emit();
        },
        engage(on) {
            engaged = !!on;
            armIdle();
        },
        close,
        reset() {
            if (timer !== null) clock.cancel(timer);
            timer = null;
            target = null;
            messages = [];
            engaged = false;
            threads.clear();
        },
        get state() { return snapshot(); },
        get engaged() { return engaged; },
    };
}

// pingQuality matches the connection pill: good < 100 ms, fair < 250 ms.
export function pingQuality(ms) {
    if (!Number.isFinite(ms) || ms < 0) return "";
    return ms < PING_GOOD_MS ? "good" : ms < PING_FAIR_MS ? "fair" : "poor";
}

// hudSpeakers lists the voice members the HUD shows. Active HUD: everyone
// in the voice channel except you, speakers first. Idle HUD: only who is
// talking or whispering right now. `limit` caps the list; the rest is a count.
export function hudSpeakers(members, { self = "", active = false, limit = 6 } = {}) {
    const talking = member => !!(member.speaking || member.whispering);
    const list = members.filter(member => String(member.id) !== String(self) && (active || talking(member)))
        .sort((a, b) => Number(talking(b)) - Number(talking(a)) || String(a.name).localeCompare(String(b.name)));
    return { shown: list.slice(0, limit), more: Math.max(0, list.length - limit) };
}

// The top and bottom centre belong to the telemetry pill and the dock, so
// a speaker list that has to move only goes to a left or right anchor.
const SIDE_ANCHORS = OVERLAY_ANCHORS.filter(anchor => anchor.col !== 1).map(anchor => anchor.id);

function nearestSideAnchor(placement, zones, box) {
    const centre = rect => [rect.x + rect.w / 2, rect.y + rect.h / 2];
    const [fx, fy] = centre(placementRect(placement, box));
    let best = "", bestDistance = Infinity;
    for (const id of SIDE_ANCHORS) {
        const rect = placementRect(anchorPlacement(id), box);
        if (zonesHit(rect, zones).length) continue;
        const [ax, ay] = centre(rect);
        const distance = (ax - fx) ** 2 + (ay - fy) ** 2;
        if (distance < bestDistance) { best = id; bestDistance = distance; }
    }
    return best;
}

const rectsOverlap = (a, b) => a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;

// Bubble candidates by speaker side: the opposite side first, at mid height
// like the concept, then its corners, then the centre edges.
const BUBBLE_ORDER = {
    left: ["center-right", "top-right", "bottom-right", "top-center", "bottom-center", "center-left", "top-left", "bottom-left"],
    right: ["center-left", "top-left", "bottom-left", "top-center", "bottom-center", "center-right", "top-right", "bottom-right"],
    center: ["center-right", "center-left", "top-right", "top-left", "bottom-right", "bottom-left"],
};

function anchorRect(id, box) {
    return placementRect(anchorPlacement(id), box);
}

// hudLayout places the persistent HUD parts clear of the game UI preset.
//  - speakers follow the gaming overlay's own anchor (Settings → Fullscreen
//    overlay), moved to the nearest free anchor when that anchor sits on a
//    no-go zone and "Avoid game UI zones" is on;
//  - the chat peek grows away from the screen edge the speakers hug;
//  - the whisper / quick-reply bubble takes the first free anchor on the
//    opposite side that does not cover the speakers;
//  - the top pill (telemetry, whisper) drops below a top-centre zone.
// Boxes and results are percentages of the fullscreen surface.
export function hudLayout({ settings = {}, speakers = { w: 18, h: 40 }, bubble = { w: 26, h: 30 }, pill = { w: 34, h: 6 } } = {}) {
    const preset = gameUIPreset(settings.gaming_overlay_game_ui);
    const avoid = settings.gaming_overlay_avoid_zones !== false;
    const zones = avoid ? presetZones(preset) : [];
    let placement = placementOf(settings);
    let moved = false;
    if (!placementAllowed(placement, preset, speakers, avoid)) {
        const free = nearestSideAnchor(placement, zones, speakers);
        if (free) { placement = anchorPlacement(free); moved = true; }
    }
    const speakerRect = placementRect(placement, speakers);
    const anchor = OVERLAY_ANCHORS.find(item => item.position === placement.position &&
        (item.position !== "custom" || (item.x === placement.x && item.y === placement.y)))?.id || "custom";
    const centreX = speakerRect.x + speakerRect.w / 2;
    const side = centreX < 40 ? "left" : centreX > 60 ? "right" : "center";
    const vertical = speakerRect.y + speakerRect.h / 2 > 60 ? "bottom" : speakerRect.y + speakerRect.h / 2 < 25 ? "top" : "middle";

    let bubbleAnchor = "";
    let bubbleRect = null;
    for (const id of BUBBLE_ORDER[side]) {
        const rect = anchorRect(id, bubble);
        if (zonesHit(rect, zones).length || rectsOverlap(rect, speakerRect)) continue;
        bubbleAnchor = id;
        bubbleRect = rect;
        break;
    }
    if (!bubbleRect) {
        bubbleAnchor = side === "right" ? "center-left" : "center-right";
        bubbleRect = anchorRect(bubbleAnchor, bubble);
    }

    const band = { x: 50 - pill.w / 2, y: 0, w: pill.w, h: pill.h };
    const topInset = zones.filter(zone => rectsOverlap(zone, band)).reduce((max, zone) => Math.max(max, zone.y + zone.h), 0);

    return {
        preset, avoid, moved, anchor, side, vertical,
        speakers: speakerRect,
        // Bottom anchors stack the peek above the speakers (column-reverse).
        peekAbove: vertical === "bottom",
        bubble: { anchor: bubbleAnchor, ...bubbleRect },
        topInset,
    };
}

// telemetryParts formats the telemetry pill from a receiver sample
// (video-grid's stats poll) and the control-connection ping.
export function telemetryParts(sample, pingMs, format) {
    const parts = {};
    if (sample?.height > 0) {
        const fps = Number.isFinite(sample.fps) && sample.fps > 0 ? Math.round(sample.fps) : 0;
        parts.video = fps ? format.videoFps(sample.height, fps) : format.video(sample.height);
    }
    if (Number.isFinite(sample?.bitrate) && sample.bitrate > 0) parts.bitrate = format.rate(sample.bitrate);
    if (Number.isFinite(pingMs) && pingMs >= 0) {
        parts.ping = format.ping(Math.round(pingMs));
        parts.quality = pingQuality(pingMs);
    }
    return parts;
}
