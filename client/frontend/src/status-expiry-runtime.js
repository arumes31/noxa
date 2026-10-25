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

// An expired record clears the status on the connection that set it: the
// originating tab when it still exists, else a connected tab to the same
// server. Returns false while no such connection is up (the scheduler then
// retries), and true once the status was cleared or is no longer ours.
async function expire(record) {
    const state = V()?.state;
    if (!state) return false;
    if (record.uniqueID && state.myUniqueID && record.uniqueID !== state.myUniqueID) return true;
    const tabs = await listTabs();
    const target = tabs.find(tab => tab.id === record.tabID && tab.connected)
        || tabs.find(tab => record.server && tab.addr === record.server && tab.connected);
    if (!target) return false;
    if (target.id === state.activeTabID) {
        if (!state.myClientID) return false;
        if (!state.myStatus) return true;
        if (!await setPresence("online", "", capturePresenceScope())) return false;
    } else {
        try {
            const error = await window.go.main.App.SetStatusForTab(target.id, "online", "");
            if (error) return false;
        } catch { return false; }
    }
    V().sysMsg?.(t("status.expiry.cleared"));
    return true;
}

async function listTabs() {
    try { return (await window.go?.main?.App?.ListTabs?.()) || []; } catch { return []; }
}

async function activeTabAddr() {
    const tabID = V()?.state?.activeTabID;
    return (await listTabs()).find(tab => tab.id === tabID)?.addr || "";
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
    const state = V()?.state;
    const record = active.set({ uniqueID: state?.myUniqueID || "", tabID: state?.activeTabID || "", server: "", status, message, expiresAt });
    // The server address lets the expiry survive a closed/reopened tab.
    void activeTabAddr().then(addr => {
        const current = active.current();
        if (addr && current && current.expiresAt === expiresAt) active.set({ ...current, server: addr });
    });
    return record;
}

export function statusExpiryTimeLabel(expiresAt) {
    try { return new Date(expiresAt).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" }); } catch { return ""; }
}
