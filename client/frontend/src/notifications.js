import { escapeHTML as escapeTranslation } from "./markdown.js";
import { t } from "./i18n.js";
// notifications.js — wave-9 notification dispatch (383-391, 215): a central
// notify() honoring the matrix (385), per-channel overrides (386/391),
// muted channels (387), and DND (347/348, still badging silently). Also the
// buddy-online watcher (383), keyword highlights (388), channel watch
// (389), and the alpha notice (215).
import { playAlert, playEvent } from "./sounds.js";
import { closeDialog, isCurrentServerDialog, mountDialog, mountServerDialog } from "./modal.js";
import { showIncomingPoke } from "./poke-ui.js";

const V = () => window.__noxa;
const App = () => window.go.main.App;

// MATRIX_EVENTS are the rows of the notification matrix (385).
export const MATRIX_EVENTS = [
    ["mention", "mention"],
    ["keyword", "keyword highlight"],
    ["dm", "direct message"],
    ["channel_message", "channel message"],
    ["whisper", "voice whisper"],
    ["poke", "poke"],
    ["join_leave", "join/leave (your channel)"],
    ["buddy_online", "watched contact online"],
    ["kick", "kick/ban"],
    ["announcement", "announcement"],
    ["channel_watch", "channel watch"],
];

// TOAST_CATEGORY maps a matrix event to the toast category the toast filter
// keys on. Only join/leave is "social" — the notify_join_leave toggle must
// not swallow mentions, DMs, pokes, kicks or announcements.
const TOAST_CATEGORY = {
    join_leave: "social",
};

// defaultMatrixRow is the fallback output set for an event (defaults: all on
// except native for chatty events). (32) whisper defaults to sound + flash +
// native: an incoming voice whisper is addressed at you personally. (385) the
// settings grid seeds unset rows from here, so the checkboxes a user sees are
// the ones dispatch actually uses.
export function defaultMatrixRow(event) {
    const direct = event === "mention" || event === "poke" || event === "dm" || event === "whisper";
    return { toast: true, sound: true, flash: event !== "join_leave" && event !== "buddy_online", native: direct };
}

// matrixRow returns the effective outputs for an event.
function matrixRow(event) {
    return V().state.settings?.notify_matrix?.[event] || defaultMatrixRow(event);
}

// channelKey builds the per-channel override key ("addr#channelID").
function channelKey(channelID) {
    const addr = V().state.lastConnect?.addr || "";
    return addr + "#" + channelID;
}

// channelOverride returns the override for a channel (or null).
export function channelOverride(channelID) {
    return V().state.settings?.channel_notify?.[channelKey(channelID)] || null;
}

export async function saveChannelOverride(channelID, ov) {
    const s = V().state.settings;
    s.channel_notify = s.channel_notify || {};
    s.channel_notify[channelKey(channelID)] = ov;
    await App().SaveSettings(s);
    V().renderTree();
}

// overrideAllows applies an inherit|on|off override for an event class.
// className is "messages" | "mentions" | "joins".
function overrideAllows(channelID, className, defaultOn) {
    if (!channelID) return defaultOn;
    const ov = channelOverride(channelID);
    if (!ov) return defaultOn;
    if (ov.muted) return false;
    const v = ov[className];
    if (v === "on") return true;
    if (v === "off") return false;
    return defaultOn;
}

// Read-only policy check used by live chat announcements so assistive output
// follows the same DND, channel override and matrix preferences as toasts.
export function notificationOutputAllowed(event, ctx = {}, output = "toast") {
    if (window.__noxaPolish?.dndActive?.()) return false;
    if (!overrideAllows(ctx.channelID, ctx.className || "messages", true)) return false;
    return !!matrixRow(event)[output];
}

// notify is the single dispatch point for user-facing notifications:
// DND → record-only; muted/overridden channels → filtered; matrix → which
// outputs fire. ctx: {channelID, uid, className ("messages"|"mentions"|"joins"),
// noSound, soundEvent, speechEvent, announce}. soundEvent chooses a more specific cue but
// never bypasses this event's matrix row or channel override.
// poke: {sender, message} replaces the toast with an incoming-poke popup.
export function notify(event, text, ctx = {}) {
    // (346) always record in the notification center (even under DND).
    window.__noxaPolish?.recordNotification(event, text, ctx);
    // Restore poke history without reopening acknowledged popups on tab replay.
    const incomingPoke = event === "poke" && ctx.poke;
    if (incomingPoke && V().state.replayingTabID) return;
    if (window.__noxaPolish?.dndActive?.()) return;
    if (!overrideAllows(ctx.channelID, ctx.className || "messages", true)) return;
    const row = matrixRow(event);
    if (row.toast) {
        if (incomingPoke) showIncomingPoke(incomingPoke);
        else {
            const kind = ctx.kind || "info";
            V().toast(text, kind, ctx.category || TOAST_CATEGORY[event] || "alert", { announce: false, record: false });
            if (ctx.announce !== false) V().announceLive(text, kind === "warn" ? "assertive" : "polite");
        }
    }
    if (row.sound && !ctx.noSound) playEventSound(event, ctx.soundEvent, ctx.speechEvent);
    if (row.flash) App().FlashWindow();
    if (row.native && !document.hasFocus()) App().Notify("noXa " + event, text.slice(0, 200));
}

// Precise action cues share the notification matrix's policy.
function playEventSound(event, soundEvent = event, speechEvent) {
    if (window.__noxaPolish?.dndActive?.()) return;
    const settings = V().state.settings;
    // Preserve the matrix and replay gates. The audio dispatcher applies each
    // action's speech preference or remaining effect preference independently.
    if (V().state.replayingTabID || settings?.event_sounds?.[event] === false) return;
    speechEvent ||= { user_join: "user_join", user_leave: "user_leave", user_move_in: "user_join", user_move_out: "user_leave" }[soundEvent];
    if (speechEvent) playAlert(speechEvent, { effect: soundEvent });
    else playEvent(soundEvent);
}

// ---------------------------------------------------------------------------
// Buddy online (383)
// ---------------------------------------------------------------------------

let buddyNotified = new Set(); // uniqueIDs already toasted this connect

export function resetBuddyWatch() {
    buddyNotified = new Set();
}

// checkBuddyOnline fires once per connect when a watched contact is online.
export function checkBuddyOnline() {
    const s = V().state.settings;
    if (!s) return;
    for (const c of s.contacts || []) {
        if (!c.notify_online || buddyNotified.has(c.unique_id)) continue;
        const online = V().state.clients.find((x) => x.unique_id === c.unique_id);
        if (online) {
            buddyNotified.add(c.unique_id);
            notify("buddy_online", (c.label || online.nickname || "contact") + " is online", { className: "joins", kind: "info" });
        }
    }
}

// ---------------------------------------------------------------------------
// Keyword highlights (388)
// ---------------------------------------------------------------------------

// matchKeyword returns the matched keyword when the text contains one
// (case-insensitive whole-word match), else "".
export function matchKeyword(text) {
    const addr = V().state.lastConnect?.addr || "";
    const words = V().state.settings?.keywords?.[addr] || [];
    const lower = text.toLowerCase();
    for (const w of words) {
        if (!w) continue;
        const needle = w.toLowerCase();
        let i = lower.indexOf(needle);
        while (i >= 0) {
            const before = i === 0 || /[\s\p{P}]/u.test(lower[i - 1]);
            const afterIdx = i + needle.length;
            const after = afterIdx >= lower.length || /[\s\p{P}]/u.test(lower[afterIdx]);
            if (before && after) return w;
            i = lower.indexOf(needle, i + 1);
        }
    }
    return "";
}

// ---------------------------------------------------------------------------
// Channel watch (389)
// ---------------------------------------------------------------------------

let watchCounts = new Map(); // override key -> last seen user count

// checkChannelWatch fires when a watched channel crosses 0→threshold.
export function checkChannelWatch() {
    const addr = V().state.lastConnect?.addr || "";
    const overrides = V().state.settings?.channel_notify || {};
    for (const [key, ov] of Object.entries(overrides)) {
        if (!ov.watch_threshold) continue;
        // keys are "addr#channelID": a threshold set on another server must not
        // fire against a same-numbered channel here.
        const cut = key.lastIndexOf("#");
        if (cut < 0 || key.slice(0, cut) !== addr) continue;
        const channelID = Number(key.slice(cut + 1));
        const count = V().state.clients.filter((c) => c.channel_id === channelID).length;
        const prev = watchCounts.get(key) ?? count;
        if (prev < ov.watch_threshold && count >= ov.watch_threshold) {
            const ch = V().state.channels.find((c) => c.ChannelID === channelID);
            notify("channel_watch", `#${ch ? ch.Name : channelID} now has ${count} user(s)`, { channelID, className: "joins", kind: "info" });
        }
        watchCounts.set(key, count);
    }
}

// ---------------------------------------------------------------------------
// Server rules gate (216)
// ---------------------------------------------------------------------------

let rulesOverlay = null;
let rulesHash = "";

// resetServerRules removes a gate when its server tab is closed or switched
// away from. If the newly active tab also has pending rules, its journaled
// server_rules frame immediately creates the correct gate again.
export function resetServerRules() {
    if (rulesOverlay) closeDialog(rulesOverlay);
    rulesOverlay = null;
    rulesHash = "";
}

export function showServerRules(json) {
    let rules;
    try {
        rules = typeof json === "string" ? JSON.parse(json) : json;
    } catch {
        V().toast(t("desktop.server.sent.malformed.rules"), "warn");
        return;
    }
    if (!rules?.text || !rules?.hash) {
        resetServerRules(); // an empty ServerRules frame is the accept ack
        return;
    }

    // The rules gate owns the first-join decision. Do not leave a lower-priority
    // startup reminder hidden underneath it.
    document.querySelector(".alpha-notice")?.remove();
    document.querySelector(".identity-backup-nag")?.remove();
    resetServerRules();
    rulesHash = rules.hash;
    const tabID = V().state.activeTabID;

    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay server-rules-gate";
    overlay.dataset.blocking = "true";
    const dlg = document.createElement("div");
    dlg.className = "dlg dlg-wide";
    const title = document.createElement("h3");
    title.textContent = t("desktop.server.rules");
    const intro = document.createElement("p");
    intro.className = "dlg-text";
    intro.textContent = t("desktop.you.must.accept.these.rules.before.joining.channels.or.using");
    const body = document.createElement("div");
    body.className = "dlg-text server-rules-text";
    body.textContent = rules.text; // operator text is data, never HTML
    const status = document.createElement("div");
    status.className = "set-hint";
    const buttons = document.createElement("div");
    buttons.className = "dlg-buttons";
    const decline = document.createElement("button");
    decline.className = "dlg-cancel";
    decline.textContent = t("desktop.decline.and.disconnect");
    const accept = document.createElement("button");
    accept.className = "dlg-ok";
    accept.textContent = t("desktop.accept");
    buttons.appendChild(decline);
    buttons.appendChild(accept);
    dlg.appendChild(title);
    dlg.appendChild(intro);
    dlg.appendChild(body);
    dlg.appendChild(status);
    dlg.appendChild(buttons);
    overlay.appendChild(dlg);
    let responseTimer = null;
    mountServerDialog(overlay, {
        onClose: () => {
            if (responseTimer) clearTimeout(responseTimer);
            responseTimer = null;
            if (rulesOverlay === overlay) {
                rulesOverlay = null;
                rulesHash = "";
            }
        },
    });
    rulesOverlay = overlay;

    decline.onclick = async () => {
        if (!isCurrentServerDialog(overlay)) return;
        decline.disabled = true;
        accept.disabled = true;
        status.textContent = t("desktop.disconnecting");
        try {
            await App().DisconnectTab(tabID);
        } catch (err) {
            if (!isCurrentServerDialog(overlay)) return;
            status.textContent = String(err);
            decline.disabled = false;
            accept.disabled = false;
            return;
        }
        if (isCurrentServerDialog(overlay)) resetServerRules();
    };
    accept.onclick = async () => {
        if (!isCurrentServerDialog(overlay)) return;
        decline.disabled = true;
        accept.disabled = true;
        status.textContent = t("desktop.recording.acceptance");
        const acceptedHash = rules.hash;
        let err;
        try {
            err = await App().AcceptServerRulesForTab(tabID, acceptedHash);
        } catch (error) {
            err = String(error);
        }
        if (!isCurrentServerDialog(overlay)) return;
        if (err) {
            status.textContent = err;
            decline.disabled = false;
            accept.disabled = false;
            return;
        }
        // Success is acknowledged by an empty ServerRules frame. A timeout
        // keeps a dropped response from stranding the user behind dead buttons.
        responseTimer = setTimeout(() => {
            if (rulesOverlay === overlay && rulesHash === acceptedHash) {
                status.textContent = t("desktop.no.response.from.server.try.again");
                decline.disabled = false;
                accept.disabled = false;
            }
        }, 10000);
    };
}

// ---------------------------------------------------------------------------
// Alpha notice (215)
// ---------------------------------------------------------------------------

export function maybeAlphaNotice(force = false) {
    const s = V().state.settings;
    const ver = V().state.clientVersion || "";
    if (!s || !ver || (!force && s.alpha_dismissed === ver)) return;
    if (document.querySelector(".server-rules-gate")) return;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay alpha-notice";
    overlay.innerHTML = `
        <div class="dlg">
            <h3>${escapeTranslation(t("desktop.noxa.is.alpha.software"))}</h3>
            <div class="dlg-text">
                <p>${escapeTranslation(t("runtime.alphaHelp", { version: ver }))}</p>
                <label class="dlg-label"><input type="checkbox" class="alpha-skip" /> ${escapeTranslation(t("desktop.don.t.show.again.for.this.version"))}</label>
            </div>
            <div class="dlg-buttons"><button class="dlg-ok">${escapeTranslation(t("desktop.got.it"))}</button></div>
        </div>`;
    overlay.querySelector(".dlg-ok").onclick = async () => {
        if (overlay.querySelector(".alpha-skip").checked) {
            s.alpha_dismissed = ver;
            await App().SaveSettings(s);
        }
        overlay.remove();
    };
    mountDialog(overlay);
}

// ---------------------------------------------------------------------------
// Identity backup reminder (353)
// ---------------------------------------------------------------------------

// backupNagSnoozed is deliberately session-scoped and NOT persisted: an
// identity that was never exported dies with the machine, so "Later" only
// silences the reminder until the next start — it comes back until an export
// actually happened.
let backupNagSnoozed = false;

// maybeIdentityBackupNag prompts to export the active identity while it has
// never been backed up (353).
export async function maybeIdentityBackupNag(force = false) {
    if (backupNagSnoozed && !force) return;
    if (document.querySelector(".server-rules-gate")) return;
    if (document.querySelector(".identity-backup-nag")) return;
    let pending = false;
    try {
        pending = await App().IdentityBackupPending();
    } catch {
        return;
    }
    if (!pending) return;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay identity-backup-nag";
    overlay.innerHTML = `
        <div class="dlg">
            <h3>${escapeTranslation(t("desktop.back.up.your.identity"))}</h3>
            <div class="dlg-text">
                <p>${escapeTranslation(t("desktop.your.identity.key.has.never.been.exported.it"))} <b>${escapeTranslation(t("desktop.is"))}</b> ${escapeTranslation(t("desktop.your.account.on.every.server.you.have.joined.if.this"))}</p>
                <p>${escapeTranslation(t("desktop.export.it.once.and.keep.the.file.somewhere.safe"))}</p>
            </div>
            <div class="dlg-buttons">
                <button class="dlg-ok">${escapeTranslation(t("desktop.export.now"))}</button>
                <button class="dlg-cancel">${escapeTranslation(t("desktop.later"))}</button>
            </div>
        </div>`;
    overlay.querySelector(".dlg-ok").onclick = async () => {
        const err = await App().ExportIdentity("");
        if (err) {
            V().toast(t("desktop.export.failed") + err, "warn");
            return;
        }
        overlay.remove();
        if (await App().IdentityBackupPending()) V().toast(t("desktop.identity.not.exported.yet"), "warn");
        else V().toast(t("desktop.identity.exported.keep.the.file.safe"));
    };
    overlay.querySelector(".dlg-cancel").onclick = () => {
        backupNagSnoozed = true;
        overlay.remove();
    };
    mountDialog(overlay);
}

export function initNotifications() {
    window.__noxaNotify = {
        notify, checkBuddyOnline, resetBuddyWatch, matchKeyword,
        checkChannelWatch, channelOverride, saveChannelOverride, maybeAlphaNotice,
        maybeIdentityBackupNag, resetServerRules, notificationOutputAllowed,
    };
    window.runtime.EventsOn("server_rules", showServerRules);
    const badge = document.getElementById("alpha-badge");
    if (badge) badge.onclick = () => maybeAlphaNotice(true);
    setTimeout(() => maybeAlphaNotice(false), 1200);
    // (353) after the alpha notice so two modals never stack on first run.
    setTimeout(() => maybeIdentityBackupNag(false), 3000);
}
