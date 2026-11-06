// presence-dot.js — a status dot on a member's avatar. Each state has its own
// shape as well as colour, so it reads without colour vision; the row's
// accessible name already states the presence in words.
const STATES = new Set(["online", "away", "busy", "offline"]);

/** Presence state for a member: invisible members look offline. */
export function presenceState(status) {
    if (status === "away" || status === "busy") return status;
    if (status === "invisible" || status === "offline") return "offline";
    return "online";
}

/** Wraps avatar (or its speaking wrapper) with a status dot. */
export function withPresenceDot(avatar, status, doc = document) {
    const wrap = doc.createElement("span");
    wrap.className = "presence-wrap";
    const dot = doc.createElement("span");
    const state = presenceState(status);
    dot.className = "presence-dot";
    dot.dataset.presence = STATES.has(state) ? state : "online";
    dot.setAttribute("aria-hidden", "true");
    wrap.append(avatar, dot);
    return wrap;
}
