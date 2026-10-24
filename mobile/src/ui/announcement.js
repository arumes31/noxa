// announcement.js — the server announcement as a banner on Home and the chat
// screens. Dismissing hides that announcement on this device until the
// server posts a different one.
import { t } from "../i18n.js";
import { h } from "./dom.js";
import { icon } from "./icons.js";
import "./rich-strings.js";
import "./rich-text.css";

const KEY = "noxa.announcement.dismissed";

function fingerprint(text) {
    // FNV-1a: stable and short; nothing secret is derived from it.
    let hash = 0x811c9dc5;
    for (let i = 0; i < text.length; i++) hash = Math.imul(hash ^ text.charCodeAt(i), 0x01000193) >>> 0;
    return hash.toString(36);
}

function dismissed(text) {
    try { return localStorage.getItem(KEY) === fingerprint(text); } catch { return false; }
}

const slots = new WeakMap(); // session -> Set of mounted slots

function fill(slot, session) {
    const text = session?.announcement?.text || "";
    if (!text || dismissed(text)) {
        slot.replaceChildren();
        return;
    }
    slot.replaceChildren(h("div", { class: "announcement", role: "status" },
        h("span", { class: "announcement-label" }, t("rich.announcement")),
        h("span", { class: "announcement-text" }, text),
        h("button", { class: "icon-btn", type: "button", "aria-label": t("rich.dismissAnnouncement"), onclick: () => {
            try { localStorage.setItem(KEY, fingerprint(text)); } catch { /* private window */ }
            refresh(session);
        } }, icon("close"))));
}

/** Re-renders every banner of a session, dropping the unmounted ones. */
function refresh(session) {
    const set = slots.get(session);
    for (const slot of set || []) {
        if (slot.isConnected) fill(slot, session);
        else if (slot.dataset.mounted) set.delete(slot);
    }
}

/** A banner slot that follows session.announcement; empty when none. */
export function announcementBanner(session) {
    const slot = h("div", { class: "announcement-slot" });
    if (!session) return slot;
    if (!slots.has(session)) {
        slots.set(session, new Set());
        session.addEventListener("announcement", () => refresh(session));
    }
    const set = slots.get(session);
    for (const old of set) if (!old.isConnected && old.dataset.mounted) set.delete(old);
    set.add(slot);
    fill(slot, session);
    // Screens attach the slot right after building it.
    queueMicrotask(() => { slot.dataset.mounted = "1"; });
    return slot;
}
