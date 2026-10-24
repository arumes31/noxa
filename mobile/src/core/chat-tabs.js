// chat-tabs.js — the open conversations of the wide layout's tab bar, as
// plain data so it can be stored in settings and tested without a DOM.
// State is {order: [key], pinned: [key]}; a key is the route of the
// conversation without "#/": "chat/4", "dm/<uid>", "group/<id>".

export const MAX_TABS = 24;
const SCREENS = new Set(["chat", "dm", "group"]);

/** The tab key of a route, or null when the screen is not a conversation. */
export function tabKey(screen, arg) {
    if (!SCREENS.has(screen) || arg === "" || arg === undefined || arg === null) return null;
    return `${screen}/${arg}`;
}

/** The location hash that opens a tab. */
export function tabHref(key) {
    const split = key.indexOf("/");
    return `#/${key.slice(0, split)}/${encodeURIComponent(key.slice(split + 1))}`;
}

/** Splits a key into {screen, arg}. */
export function parseKey(key) {
    const split = key.indexOf("/");
    return { screen: key.slice(0, split), arg: key.slice(split + 1) };
}

/** Repairs stored state: unknown keys, duplicates and stray pins are dropped. */
export function normalize(state) {
    const valid = key => typeof key === "string" && SCREENS.has(key.split("/")[0]) && key.includes("/") && key.indexOf("/") < key.length - 1;
    const order = [...new Set((Array.isArray(state?.order) ? state.order : []).filter(valid))].slice(0, MAX_TABS);
    const pinned = [...new Set((Array.isArray(state?.pinned) ? state.pinned : []).filter(key => order.includes(key)))];
    return { order, pinned };
}

/** Keys in display order: pinned tabs first, each group in its stored order. */
export function ordered(state) {
    const pins = new Set(state.pinned);
    return [...state.order.filter(key => pins.has(key)), ...state.order.filter(key => !pins.has(key))];
}

/** Adds a tab at the end; at the limit the oldest unpinned tab makes room. */
export function open(state, key) {
    if (!key || state.order.includes(key)) return state;
    let order = [...state.order, key];
    if (order.length > MAX_TABS) {
        const drop = order.find(k => k !== key && !state.pinned.includes(k));
        if (drop) order = order.filter(k => k !== drop);
        else order = order.slice(1);
    }
    return { order, pinned: state.pinned.filter(k => order.includes(k)) };
}

/**
 * Closes a tab. Returns {state, next}: when the active tab closes, next is
 * its right neighbour (or the left one, or null); otherwise next is active.
 */
export function close(state, key, active) {
    const list = ordered(state);
    const index = list.indexOf(key);
    if (index < 0) return { state, next: active };
    const next = key === active ? (list[index + 1] ?? list[index - 1] ?? null) : active;
    return { state: { order: state.order.filter(k => k !== key), pinned: state.pinned.filter(k => k !== key) }, next };
}

/** The tab dir (+1/-1) steps away from active, wrapping around. */
export function step(state, active, dir) {
    const list = ordered(state);
    if (!list.length) return null;
    const index = list.indexOf(active);
    if (index < 0) return dir > 0 ? list[0] : list[list.length - 1];
    return list[(index + dir + list.length) % list.length];
}

export function togglePin(state, key) {
    if (!state.order.includes(key)) return state;
    const pinned = state.pinned.includes(key) ? state.pinned.filter(k => k !== key) : [...state.pinned, key];
    return { order: state.order, pinned };
}

/** Moves a tab by offset within its group (pinned or not). */
export function move(state, key, offset) {
    const list = ordered(state);
    const pinned = state.pinned.includes(key);
    const group = list.filter(k => state.pinned.includes(k) === pinned);
    const index = group.indexOf(key);
    const target = group[index + offset];
    if (index < 0 || !target) return state;
    const a = list.indexOf(key), b = list.indexOf(target);
    [list[a], list[b]] = [list[b], list[a]];
    const pins = new Set(state.pinned);
    return { order: list, pinned: list.filter(k => pins.has(k)) };
}
