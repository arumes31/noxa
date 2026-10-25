// Wires the status expiry schedule to the live client: localStorage keeps the
// record across restarts, the timer clears the status while running, and
// focus/visibility changes re-check after sleep or a long background period.
import { t } from "./i18n.js";
import { createStatusExpiryScheduler } from "./status-expiry.js";
import { capturePresenceScope, setPresence } from "./presence.js";

const STORAGE_KEY = "noxa.status.expiry";
const V = () => window.__noxa;
let scheduler = null;

function load() {
    try { return JSON.parse(localStorage.getItem(STORAGE_KEY) || "null"); } catch { return null; }
}
function save(record) {
    try {
        if (record) localStorage.setItem(STORAGE_KEY, JSON.stringify(record));
        else localStorage.removeItem(STORAGE_KEY);
    } catch { /* Storage is optional. */ }
}

// An expired record only acts on the account that set it; otherwise the
// server already started this connection with a fresh status.
async function expire(record) {
    const state = V()?.state;
    if (!state?.myClientID || !state.myStatus || (record.uniqueID && record.uniqueID !== state.myUniqueID)) return;
    if (await setPresence("online", "", capturePresenceScope())) V().sysMsg(t("status.expiry.cleared"));
}

export function initStatusExpiry() {
    if (scheduler) return scheduler;
    scheduler = createStatusExpiryScheduler({ load, save, onExpire: record => void expire(record) });
    const check = () => scheduler.check();
    document.addEventListener("visibilitychange", () => { if (document.visibilityState === "visible") check(); });
    window.addEventListener("focus", check);
    const timer = setInterval(check, 30_000);
    window.addEventListener("beforeunload", () => clearInterval(timer), { once: true });
    return scheduler;
}

// armStatusExpiry records when the status just set should clear itself; 0
// (or a cleared status) cancels any pending expiry.
export function armStatusExpiry({ status, message, expiresAt }) {
    const active = initStatusExpiry();
    if (!expiresAt || !status || status === "online") { active.clear(); return null; }
    return active.set({ uniqueID: V()?.state?.myUniqueID || "", status, message, expiresAt });
}

export function statusExpiryTimeLabel(expiresAt) {
    try { return new Date(expiresAt).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" }); } catch { return ""; }
}
