// noxa client frontend — voice ops console (vanilla JS).
// Wails bridge: window.go.main.App.<Method>(...) calls the Go backend;
// window.runtime.EventsOn(name, cb) receives backend events.

import "@fontsource-variable/sora";
import "./streams.css";
import "./polls.css";
import "./conversations.css";
import { conversationChanged, filterConversations, privateGroupViewToken } from "./conversations.js";
import "./private-calls.css";
import { privateCallChanged, privateCallSignal, stopPrivateCall } from "./private-calls.js";
import { refreshPolls } from "./polls.js";
import { SpatialVoice } from "./positional-audio.js";
import "@fontsource-variable/outfit";
import "@fontsource-variable/jetbrains-mono";
import { initMenu } from "./menu.js";
import { initSettingsUI } from "./settings-ui.js";
import { initClientInfo } from "./clientinfo.js";
import { initUpdater, startupAutoCheck } from "./updater.js";
import { playEvent, playAlert, clearSpeech, initSounds, updateSoundOutput, updateConversationDucking, soundEngine, speechQueue } from "./sounds.js";
import {
    startMicMeter, stopMicMeter, pttRelease, makeLimiter,
    getUserVolume, isUserMuted, refreshUserAudio, registerUserChain, unregisterUserChain,
    setDucking, attachUserNormalizer, detachUserNormalizer, detachAllUserNormalizers,
    captureConstraints, markCaptureProfile, applyCaptureProfile, resumeAudioPlayback, createRemoteAudioSource,
    syncMuteButton, renderMicStatus,
} from "./audio.js";
import {
    initVideo, videoTrackAdded, videoTrackRemoved, videoSpeaking,
    videoRefreshNames, clearVideoGrid, shareToggle, setLowBandwidth, isLowBandwidth,
    parseTrackID, SLOT_SCREEN_AUDIO, cameraToggle, resetCameraState, clearRegionBox,
    renegotiate, answerRemoteOffer, applyVideoLimits,
} from "./video.js";
import * as chatUI from "./chat-ui.js";
import { startStreamSession, stopStreamSession, streamSessionIsCurrent, receiveStreamTrack, receiveShareAudio } from "./stream-controls.js";
import { isCurrentPublication } from "./stream-publication.js";
import { initPermsUI } from "./roles-access-ui.js";
import { roleChip } from "./role-presentation.js";
import { initFilesUI } from "./files-ui.js";
import { initTabs } from "./tabs.js";
import { initSocialUI } from "./social-ui.js";
import { initMetaUI } from "./meta-ui.js";
import { initPolishUI } from "./polish-ui.js";
import { imageDataURL, setSafeImage } from "./safe-media.js";
import { initNotifications } from "./notifications.js";
import { setLanguage, currentLanguage, applyStaticLabels, t } from "./i18n.js";
import { extractPresentedFingerprint } from "./security.js";
import { isActivationKey } from "./a11y.js";
import { createLiveAnnouncementQueue } from "./live-announcer.js";
import { dialogFocusableSelector, initModalSystem, mountServerDialog } from "./modal.js";
import { parseRuntimeObject } from "./runtime-json.js";
import { icon } from "./icons.js";
import { initWorkspace, renderWorkspace, renderMember, renderVoiceHints } from "./workspace-ui.js";
import { createTrayVoiceSync } from "./tray-state.js";
import { capturePresenceScope, presenceIsCurrent, restorePresenceOnActivity, setPresence } from "./presence.js";
import { captureMediaScope, mediaScopeIsCurrent, setWhisperRouting } from "./media-controls.js";

const P = () => window.__noxaPerms;
window.__noxaChat = chatUI;

const $ = (id) => document.getElementById(id);
const publishTrayVoice = createTrayVoiceSync((...flags) => window.go.main.App.SetTrayVoiceState(...flags));

function syncTrayVoice() {
    const me = state.clients.find((client) => client.client_id === state.myClientID);
    const speaking = !!(state.pc && !state.replayingTabID && state.myChannelID &&
        me?.channel_id === state.myChannelID && me.is_speaking && !state.muted);
    publishTrayVoice(speaking, state.muted, state.deafened);
}

const liveAnnouncements = createLiveAnnouncementQueue({
    resolveRegion: (priority) => $(priority === "assertive" ? "alert-announcer" : "chat-announcer"),
});

const state = {
    channels: [],   // flat: {ChannelID, ParentID, Name, HasIcon, Topic, MaxClients, OpusBitrate, OpusFEC, OpusDTX, OpusStereo, SlowModeSeconds}
    clients: [],    // flat: {client_id, unique_id, nickname, channel_id, is_speaking, priority_speaker}
    myClientID: "",
    myUniqueID: "",
    myNickname: "",
    myChannelID: 0,
    isGuest: true,        // anonymous sessions cannot perform account-only actions
    authorizationModel: "", // negotiated per session; "pending" while a tab's identity is loading
    sessionGeneration: 0, // invalidates session snapshots when the connection drops
    myPriority: false, // own priority-speaker flag
    selectedClientID: "",
    pc: null,
    localStream: null,
    screenSharing: false,
    muted: false,
    deafened: false,
    pttActive: false,
    collapsedChannels: new Set(), // (302) collapsed channel IDs
    expandedVirtual: new Set(),   // (349) user-expanded channels in windowed mode
    treeFilter: "",               // (319) live tree filter
    multiSelect: new Set(),       // (306) ctrl/shift-selected client IDs
    myStatus: "",                 // (307) own presence status
    canSetInvisible: false,       // recipient-specific role eligibility
    micState: "unknown", // ok | none | denied
    lastWhispererUID: "", // (33) last user who whispered to me (voice or DM)
    whisperArmed: false,  // (33) whisper-reply hotkey is overriding the whisper list
    whisperTargetUID: "", // confirmed reply target, independent of later incoming whispers
    whisperPrev: null,    // (33) {clients, channels, active} the hotkey replaced
    avatars: new Map(),  // unique_id -> data url | null
    avatarPending: new Set(),
    serverGeneration: 0, // invalidates async responses when the active server tab changes
    settings: null,
    activeTabID: "", // frontend-observed active tab; guards async finalizers against tab switches
    replayingTabID: "", // journal replay is view restoration, not fresh activity
    pendingInitialChannelCueTabID: "", // new tab's first own-channel cue waits for replay
    lastConnect: null,   // {addr, nick, pw, spw, bookmark} for reconnect-on-loss
    lastSuccessfulConnect: null, // in-memory only; keeps manual tray reconnect available after Disconnect
    tabConnects: new Map(), // (281) tab ID -> its own lastConnect record
    pendingBookmark: null, // (334) {name, addr} of the bookmark loaded into the login dialog
    reconnectAttempts: 0,
    reconnectTimer: null,
    reconnectInFlight: false,
    vadMonitor: null,
    voiceMonitorCtx: null,
    trackUsers: new Map(), // media track ID -> {client_id, unique_id, nickname} (per-publisher tracks; wave-3 video tiles)
    shareStream: null,     // getDisplayMedia result while screen sharing
    shareAudioSender: null, // RTCRtpSender of the optional share system-audio track
    shareVideoTransceiver: null, // dedicated screen source, independent of camera
    shareStarting: null,
    shareStopping: false,
    shareAudioTransceiver: null, // dedicated system-audio source for this share
    regionBox: null,       // (71) crop target element, alive for the whole cropped share
};

// ---------------------------------------------------------------------------
// Toasts
// ---------------------------------------------------------------------------

// (385) "social" means join/leave only: the notify_join_leave toggle is
// labelled "Toasts for join/leave", so untagged callers default to "alert"
// and are never swallowed by it.
function announceLive(text, priority = "polite") {
    return liveAnnouncements.announce(text, priority);
}

function toast(text, kind = "info", category = "alert", options = {}) {
    if (typeof category === "object") {
        options = category;
        category = "alert";
    }
    const s = state.settings;
    if (s) {
        if (category === "social" && !s.notify_join_leave) return;
        if (category === "conn" && !s.notify_connection) return;
    }
    // (346) record into the notification center; (347/348) DND suppresses
    // visible toasts but still records them silently.
    if (options.record !== false) window.__noxaPolish?.recordNotification(kind, text, {});
    if (!options.bypassDND && window.__noxaPolish?.dndActive?.()) return;
    if (options.announce !== false) announceLive(text, kind === "warn" ? "assertive" : "polite");
    const el = document.createElement("div");
    el.className = "toast " + kind;
    el.textContent = text;
    // The static live regions own announcements. Keep this visual copy out of
    // the accessibility tree so the same update is never spoken twice.
    el.setAttribute("aria-hidden", "true");
    $("toasts").appendChild(el);
    setTimeout(() => {
        el.classList.add("out");
        setTimeout(() => el.remove(), 300);
    }, 4000);
}

// ---------------------------------------------------------------------------
// Appearance (294-297): theme, accent, user CSS, UI font, compact mode.
// Applied at startup and after settings changes.
// ---------------------------------------------------------------------------

function applyAppearance() {
    const s = state.settings || {};
    const root = document.documentElement;
    // (336) language applies live (menus + static labels rebuild).
    const previousLanguage = currentLanguage();
    setLanguage(s.language || "system");
    root.lang = currentLanguage();
    applyStaticLabels();
    if (previousLanguage !== currentLanguage()) window.dispatchEvent(new Event("noxa-language-changed"));
    syncMuteButton($("voice-mute"), state.muted);
    renderVoiceStatus();
    initMenu();
    // (294/295) theme sets the full variable palette via [data-theme].
    root.dataset.theme = s.theme || "dark";
    // (344) reduce motion: toggle + media query both gate animations.
    root.dataset.reduceMotion = s.reduce_motion ? "1" : "";
    // (296) custom accent overrides the theme's accent.
    if (s.accent_color) root.style.setProperty("--accent", s.accent_color);
    else root.style.removeProperty("--accent");
    // (296) scoped user CSS overrides.
    let userStyle = document.getElementById("user-css");
    if (s.user_css) {
        if (!userStyle) {
            userStyle = document.createElement("style");
            userStyle.id = "user-css";
            document.head.appendChild(userStyle);
        }
        userStyle.textContent = s.user_css;
    } else if (userStyle) {
        userStyle.remove();
    }
    // (297) UI font family + base size.
    const fonts = {
        outfit: '"Outfit Variable", sans-serif',
        sora: '"Sora Variable", sans-serif',
        jetbrains: '"JetBrains Mono Variable", monospace',
    };
    root.style.setProperty("--font-body", fonts[s.ui_font] || fonts.outfit);
    root.style.fontSize = (s.ui_font_size || 14) + "px";
    // (293) restore compact mode.
    const compact = !!s.compact_mode;
    if (compact) window.__noxaFiles?.activateWorkspaceTab?.("chat", { focus: false });
    document.body.classList.toggle("compact", compact);
    if (compact) window.__noxaFiles?.restoreVisibleWorkspaceFocus?.();
}

// ---------------------------------------------------------------------------
// Login / connection
// ---------------------------------------------------------------------------

(async () => {
    try {
        state.settings = await window.go.main.App.GetSettings();
        void updateSoundOutput();
        // (88) reflect the persisted low-bandwidth mode in the voice bar.
        if (state.settings?.low_bandwidth) setLowBandwidth(true, false);
    } catch {
        state.settings = null;
    }
    applyAppearance();
    // initTabs renders before the asynchronous native settings load completes.
    window.__noxaTabs.renderRecents();
    try {
        const uid = await window.go.main.App.IdentityUID();
        if (uid) $("login-identity").textContent = uid.slice(0, 12) + "…";
    } catch { /* identity display is best-effort */ }
    try {
        const v = await window.go.main.App.ClientVersionShort();
        $("login-version").textContent = "noXa " + v;
        state.clientVersion = v;
    } catch { /* version display is best-effort */ }
    document.querySelector(".login-card").classList.add("in");
    startupAutoCheck();
})();

function showLogin() {
    // (334) the dialog is retargetable, so a stash from an earlier bookmark
    // must never identify the next login — a same-address login can be a
    // different account (callers loading a bookmark stash after this call).
    state.pendingBookmark = null;
    $("login-overlay").classList.remove("hidden");
    $("login-overlay").setAttribute("aria-hidden", "false");
    $("app").classList.add("hidden");
    $("app").setAttribute("aria-hidden", "true");
    $("conn-lock").classList.add("hidden");
    requestAnimationFrame(() => $("login-addr").focus({ preventScroll: true }));
}

function showWorkspace(focus = true) {
    const login = $("login-overlay");
    const leavingLogin = !login.classList.contains("hidden") || login.contains(document.activeElement);
    login.classList.add("hidden");
    login.setAttribute("aria-hidden", "true");
    $("app").classList.remove("hidden");
    $("app").setAttribute("aria-hidden", "false");
    if (focus || leavingLogin) requestAnimationFrame(() => $("center").focus({ preventScroll: true }));
}

// The login is a full-screen application state rather than a body-level
// modal. Keep its keyboard focus local while it is visible and explicitly
// place focus there on initial load and every return to login.
$("login-overlay").addEventListener("keydown", (event) => {
    if (event.key !== "Tab" || $("login-overlay").classList.contains("hidden")) return;
    const items = [...$("login-overlay").querySelectorAll(dialogFocusableSelector)]
        .filter((element) => !element.disabled && element.tabIndex >= 0 && element.getClientRects().length > 0);
    if (items.length === 0) return;
    const index = items.indexOf(document.activeElement);
    const next = event.shiftKey
        ? (index <= 0 ? items.length - 1 : index - 1)
        : (index < 0 || index === items.length - 1 ? 0 : index + 1);
    event.preventDefault();
    items[next].focus();
});

document.querySelector(".login-card").addEventListener("submit", (event) => {
    event.preventDefault();
    connectFromLogin();
});

async function connectFromLogin() {
    const submit = $("login-connect");
    if (submit.disabled) return;
    const submitLabel = submit.textContent;
    submit.disabled = true;
    submit.textContent = "CONNECTING…";
    document.querySelector(".login-card").setAttribute("aria-busy", "true");
    const addr = $("login-addr").value.trim();
    const nick = $("login-nick").value.trim();
    const pw = $("login-accountpw").value;
    const spw = $("login-serverpw").value;
    // A bridge call can outlive a tab switch. Only the tab/generation that
    // initiated this login may announce its eventual failure.
    const requestServerGeneration = state.serverGeneration;
    const requestTabID = state.activeTabID;
    const playCurrentConnectionFailure = () => {
        if (requestServerGeneration === state.serverGeneration && requestTabID === state.activeTabID) {
            playEvent("connection_failed");
        }
    };
    $("login-error").textContent = "";
    // (334) a nickname override replaces the login nickname, so addr+nickname
    // no longer identifies the bookmark this login came from: forward the name
    // the bookmark menu stashed, unless the dialog was retargeted since.
    const bookmark = state.pendingBookmark?.addr === addr ? state.pendingBookmark.name : "";
    try {
        const { error: err, tabID } = await connectBookmarkTabWithID(
            bookmark, addr, nick, pw, spw);
        if (err) {
            // (4a) TOFU fingerprint mismatch: prominent warning + explicit
            // trust action — never silently accepted.
            if (err.startsWith("tls fingerprint mismatch")) {
                playCurrentConnectionFailure();
                showFingerprintWarning(addr, err);
                return;
            }
            playCurrentConnectionFailure();
            $("login-error").textContent = err;
            return;
        }
        // (334) consumed: lastConnect carries the name for reconnects from
        // here on. Clearing only on success keeps the retry after a rejected
        // password identifiable.
        state.pendingBookmark = null;
        if ($("login-addr").value.trim() === addr && $("login-nick").value.trim() === nick &&
            $("login-accountpw").value === pw) $("login-accountpw").value = "";
        const connection = { addr, nick, pw, spw, bookmark };
        const ownsActiveTab = await rememberTabConnect(connection, null, tabID);
        if (!ownsActiveTab) return;
        const finalizationGeneration = state.serverGeneration;
        const sessionGeneration = state.sessionGeneration;
        // A legacy binding has no tab ID, so retain the direct warning path.
        // Current bindings check the exact tab from tabs.js after activation.
        const clockWarning = tabID ? "" : await certificateClockWarning(addr);
        state.reconnectAttempts = 0;
        let session;
        try {
            session = await window.go.main.App.SessionInfoForTab(tabID);
        } catch { return; }
        if (state.serverGeneration !== finalizationGeneration) return;
        if (!await tabIsActive(tabID)) return;
        if (state.serverGeneration !== finalizationGeneration || state.sessionGeneration !== sessionGeneration || !session.connected) return;
        state.myNickname = nick;
        state.myClientID = session.client_id;
        state.isGuest = session.is_guest;
        state.authorizationModel = session.authorization_model || "";
        $("conn-pill").textContent = addr;
        $("conn-pill").classList.add("up");
        $("conn-lock").classList.remove("hidden");
        showWorkspace();
        refreshPermissions();
        applyWhisperSettings();
        chatUI.onConnect(); // (133) MOTD + myUniqueID for mentions/own-msgs
        window.__noxaFiles.loadServerIcon(); // (270) server icon in the sidebar
        window.__noxaSocial.refreshNews(); // (313) server news pane
        startQualitySampler(); // (333) connection quality pill
        noteActivity(); // (308) auto-away timer starts at connect
        playEvent("connection_connected");
        // (4a) surface the connection security as an info line.
        if (session.security) sysMsg("connected: " + session.security);
        warnCertificateClock(clockWarning, addr);
    } catch (e) {
        playCurrentConnectionFailure();
        $("login-error").textContent = String(e);
    } finally {
        submit.disabled = false;
        submit.textContent = submitLabel;
        document.querySelector(".login-card").removeAttribute("aria-busy");
    }
}

// showFingerprintWarning displays the TOFU mismatch dialog (4a): the server
// certificate changed since the pinned fingerprint — possible MITM. The user
// may explicitly trust the new fingerprint, which updates known_servers.json
// and retries the connect.
async function showFingerprintWarning(addr, detail) {
    const presented = extractPresentedFingerprint(detail);
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="dlg share-dlg fp-warn">
            <h3>⚠ Server certificate changed</h3>
            <p class="dlg-text">The TLS fingerprint of <span class="mono"></span> does not match the pinned one. This could be a man-in-the-middle attack — or a legitimate server reinstall.</p>
            <p class="dlg-text mono fp-detail"></p>
            <div class="dlg-buttons">
                <button class="dlg-cancel">Abort</button>
                <button class="dlg-ok danger-btn">Trust new fingerprint</button>
            </div>
        </div>`;
    overlay.querySelector("span.mono").textContent = addr;
    overlay.querySelector(".fp-detail").textContent = detail;
    overlay.querySelector(".dlg-cancel").onclick = () => overlay.remove();
    overlay.querySelector(".dlg-ok").onclick = async () => {
        if (!presented) {
            overlay.remove();
            $("login-error").textContent = "trust failed: the server did not provide a valid replacement fingerprint";
            return;
        }
        const err = await window.go.main.App.TrustServerFingerprint(addr, presented);
        overlay.remove();
        if (err) {
            $("login-error").textContent = "trust failed: " + err;
            return;
        }
        connectFromLogin();
    };
    mountServerDialog(overlay, { initialFocus: ".dlg-cancel" });
}

function normalizeConnectResult(result) {
    if (typeof result === "string") return { tabID: "", error: result };
    return {
        tabID: String(result?.tab_id || ""),
        error: String(result?.error || ""),
    };
}

async function connectBookmarkTabWithID(bookmark, addr, nick, pw, spw) {
    const method = window.go.main.App.ConnectBookmarkTabWithID;
    if (typeof method === "function") {
        return normalizeConnectResult(await method(bookmark, addr, nick, pw, spw));
    }
    return normalizeConnectResult(
        await window.go.main.App.ConnectBookmarkTab(bookmark, addr, nick, pw, spw));
}

async function activeTabInfo() {
    try {
        return (await window.go.main.App.ListTabs()).find((tab) => tab.active) || null;
    } catch {
        return null;
    }
}

async function tabIsActive(tabID) {
    if (!tabID) return true; // compatibility with pre-tab-ID bindings
    if (state.activeTabID && state.activeTabID !== tabID) return false;
    const active = await activeTabInfo();
    if (state.activeTabID && state.activeTabID !== tabID) return false;
    return active?.id === tabID;
}

async function certificateClockWarning(expectedAddr = "", expectedTabID = "") {
    const generation = state.serverGeneration;
    try {
        if (expectedTabID && !await tabIsActive(expectedTabID)) return "";
        const warning = String(await window.go.main.App.CertificateClockWarning() || "").trim();
        if (!expectedAddr && !expectedTabID) return warning;
        // If the user switched tabs while the bridge call was pending, do not
        // attribute the active tab's certificate warning to this server.
        const active = await activeTabInfo();
        if (generation !== state.serverGeneration) return "";
        if (expectedTabID && active?.id !== expectedTabID) return "";
        return active?.addr && expectedAddr && active.addr !== expectedAddr ? "" : warning;
    } catch {
        return "";
    }
}

function warnCertificateClock(warning, addr) {
    if (!warning) return;
    const context = addr ? `Certificate timing warning for ${addr}: ` : "Certificate timing warning: ";
    sysMsg(context + warning);
    toast(context + warning, "warn", "alert");
}

const clockCheckedTabs = new Set();

async function checkCertificateClock(addr, tabID = "") {
    if (tabID && clockCheckedTabs.has(tabID)) return;
    const warning = await certificateClockWarning(addr, tabID);
    if (tabID) {
        if (!await tabIsActive(tabID)) return;
        clockCheckedTabs.add(tabID);
    }
    warnCertificateClock(warning, addr);
}

// rememberTabConnect files the credential record under the tab it belongs to
// (281): switching away and back must restore the record a reconnect needs,
// and only the connect call knows the password.
async function rememberTabConnect(c, expectedGeneration = null, tabID = "") {
    const active = await activeTabInfo();
    if (expectedGeneration !== null && expectedGeneration !== reconnectGeneration) return false;
    state.lastSuccessfulConnect = { ...c };
    if (tabID) state.tabConnects.set(tabID, c);
    else if (active) state.tabConnects.set(active.id, c);
    if (tabID && (active?.id !== tabID || (state.activeTabID && state.activeTabID !== tabID))) return false;
    state.lastConnect = c;
    return true;
}

let reconnectCountdownTimer = null;
let reconnectRequestPending = false;
let reconnectGeneration = 0;
let reconnectFailureSounded = false;
let reconnectCueTimer = null;

function autoReconnectEnabled() {
    // Missing settings and pre-setting-version profiles inherit the safer
    // default: recover an unexpectedly lost connection unless explicitly off.
    return state.settings?.reconnect_on_loss !== false;
}

function clearReconnectTimer() {
    if (state.reconnectTimer) {
        clearTimeout(state.reconnectTimer);
        state.reconnectTimer = null;
    }
    if (reconnectCountdownTimer) {
        clearInterval(reconnectCountdownTimer);
        reconnectCountdownTimer = null;
    }
    if (reconnectCueTimer) {
        clearTimeout(reconnectCueTimer);
        reconnectCueTimer = null;
    }
}

async function completeReconnect(c, generation, tabID) {
    // The backend query must precede unrelated awaits so it reads the tab the
    // reconnect just created. The message below also names that server.
    const clockWarning = tabID ? "" : await certificateClockWarning(c.addr);
    const ownsActiveTab = await rememberTabConnect(c, generation, tabID);
    if (generation !== reconnectGeneration) return false;
    const finalizationGeneration = state.serverGeneration;
    const sessionGeneration = state.sessionGeneration;
    // A user-selected tab now owns the global UI. The reconnect still
    // succeeded in the background, so stop retrying without painting over it.
    if (!ownsActiveTab) {
        state.reconnectAttempts = 0;
        return true;
    }
    let session;
    try {
        session = await window.go.main.App.SessionInfoForTab(tabID);
    } catch { return generation === reconnectGeneration; }
    if (generation !== reconnectGeneration) return false;
    if (state.serverGeneration !== finalizationGeneration) return true;
    if (!await tabIsActive(tabID)) return true;
    if (state.serverGeneration !== finalizationGeneration) return true;
    if (state.sessionGeneration !== sessionGeneration || !session.connected) return false;
    state.reconnectAttempts = 0;
    state.myNickname = c.nick;
    state.myClientID = session.client_id;
    state.isGuest = session.is_guest;
    state.authorizationModel = session.authorization_model || "";
    $("conn-pill").textContent = c.addr;
    $("conn-pill").classList.add("up");
    $("conn-lock").classList.remove("hidden");
    showWorkspace();
    refreshPermissions();
    applyWhisperSettings();
    chatUI.onConnect();
    window.__noxaFiles?.loadServerIcon?.();
    window.__noxaSocial?.refreshNews?.();
    startQualitySampler();
    noteActivity();
    playEvent("connection_reconnected");
    clearSpeech("connection");
    warnCertificateClock(clockWarning, c.addr);
    return generation === reconnectGeneration;
}

async function retireReplacedTab(sourceTabID, replacementTabID, c, generation) {
    if (!sourceTabID || !replacementTabID || sourceTabID === replacementTabID) return;
    try {
        const tabs = await window.go.main.App.ListTabs();
        if (generation !== reconnectGeneration) return;
        const source = tabs?.find((tab) => tab.id === sourceTabID);
        // Explicit connections may intentionally open the same server twice.
        // Only the disconnected tab that initiated this retry is replaced.
        if (!source || source.connected || source.addr !== c.addr || source.nickname !== c.nick) return;
        await window.go.main.App.CloseTab(sourceTabID);
        state.tabConnects.delete(sourceTabID);
    } catch (error) {
        sysMsg("reconnected, but could not close the previous server tab: " + String(error));
    }
}

async function attemptReconnect(c = state.lastConnect, { announceFailure = true, sourceTabID = state.activeTabID } = {}) {
    if (!c || state.reconnectInFlight) return false;
    const generation = reconnectGeneration;
    const sourceServerGeneration = state.serverGeneration;
    state.reconnectInFlight = true;
    let err = "";
    let tabID = "";
    try {
        const result = await connectBookmarkTabWithID(
            c.bookmark || "", c.addr, c.nick, c.pw, c.spw);
        err = result.error;
        tabID = result.tabID;
    } catch (cause) {
        err = String(cause || "reconnect failed");
    } finally {
        state.reconnectInFlight = false;
    }
    if (generation !== reconnectGeneration) {
        // Disconnect may have run while the native connect call was pending.
        // If that stale call nevertheless opened a tab, close it before it can
        // restore credentials or paint the UI as connected again.
        if (!err && tabID) {
            try { await window.go.main.App.CloseTab(tabID); } catch { /* best-effort stale-tab cleanup */ }
        }
        return false;
    }
    if (err) {
        if (sourceTabID !== state.activeTabID || sourceServerGeneration !== state.serverGeneration) return false;
        sysMsg("reconnect failed: " + err);
        if (announceFailure) toast("Reconnect failed: " + err, "warn", "conn");
        return false;
    }
    const completed = await completeReconnect(c, generation, tabID);
    if (!completed && generation !== reconnectGeneration && tabID) {
        try { await window.go.main.App.CloseTab(tabID); } catch { /* best-effort stale-tab cleanup */ }
    }
    if (completed) await retireReplacedTab(sourceTabID, tabID, c, generation);
    return completed;
}

function scheduleReconnect(
    target = state.lastConnect,
    generation = reconnectGeneration,
    sourceTabID = state.activeTabID,
    sourceServerGeneration = state.serverGeneration,
) {
    const ownsSource = () => generation === reconnectGeneration
        && sourceServerGeneration === state.serverGeneration
        && sourceTabID === state.activeTabID;
    if (generation !== reconnectGeneration || !autoReconnectEnabled() || !target || state.reconnectAttempts >= 5) {
        if (target && state.reconnectAttempts >= 5 && !reconnectFailureSounded && ownsSource()) {
            reconnectFailureSounded = true;
            playAlert("reconnect_failed");
        }
        chatUI.cancelReconnectAnnouncementBatch();
        showLogin();
        return;
    }
    // Pin the retry series to the connection that dropped. A tab switch may
    // replace state.lastConnect while the five-second countdown is running.
    const reconnectTarget = { ...target };

    if (state.reconnectAttempts === 0) {
        reconnectFailureSounded = false;
        // Let the loss contour complete before the recovery contour starts.
        // The timer is cancelled by a manual disconnect or a newer connection.
        reconnectCueTimer = setTimeout(() => {
            reconnectCueTimer = null;
            if (ownsSource() && state.lastConnect?.addr === reconnectTarget.addr) {
                playEvent("connection_reconnecting");
            }
        }, 550);
    }
    state.reconnectAttempts++;
    chatUI.beginReconnectAnnouncementBatch();
    sysMsg(`reconnecting in 5s (attempt ${state.reconnectAttempts}/5)…`);
    $("conn-pill").textContent = `retry ${state.reconnectAttempts}/5 in 5s…`;
    let countdown = 4;
    reconnectCountdownTimer = setInterval(() => {
        if (countdown <= 0 || !state.reconnectTimer) {
            clearInterval(reconnectCountdownTimer);
            reconnectCountdownTimer = null;
            return;
        }
        $("conn-pill").textContent = `retry ${state.reconnectAttempts}/5 in ${countdown--}s…`;
    }, 1000);
    state.reconnectTimer = setTimeout(async () => {
        clearReconnectTimer();
        if (generation !== reconnectGeneration) return;
        const sessionGeneration = state.sessionGeneration;
        const connected = await attemptReconnect(reconnectTarget, { announceFailure: false, sourceTabID });
        if (connected) return;
        if (generation !== reconnectGeneration) return;
        // A disconnect during finalization already scheduled its own recovery.
        if (sessionGeneration !== state.sessionGeneration) return;
        chatUI.cancelReconnectAnnouncementBatch();
        scheduleReconnect(reconnectTarget, generation, sourceTabID, sourceServerGeneration);
    }, 5000);
}

async function reconnectLastServerNow() {
    // The native menu is disabled while connected. Keep this guard for a
    // delayed menu click already queued by the operating system.
    if (state.reconnectInFlight || reconnectRequestPending) return;
    reconnectRequestPending = true;
    const sourceTabID = state.activeTabID;
    const serverGeneration = state.serverGeneration;
    const sessionGeneration = state.sessionGeneration;
    const generation = reconnectGeneration;
    const previous = state.lastSuccessfulConnect;
    const target = previous ? { ...previous } : null;
    const fallbackTarget = target ? null : window.__noxaTabs?.quickConnectTarget?.();
    const current = () => sourceTabID === state.activeTabID && serverGeneration === state.serverGeneration &&
        sessionGeneration === state.sessionGeneration && generation === reconnectGeneration && previous === state.lastSuccessfulConnect;
    try {
        if ($("conn-pill").classList.contains("up")) return;
        let tabs;
        try { tabs = await window.go.main.App.ListTabs(); }
        catch {
            if (current()) toast("Connection status unavailable. Try reconnecting again.", "warn", "conn");
            return;
        }
        if (!current() || state.reconnectInFlight || !Array.isArray(tabs)) return;
        const active = tabs.find(tab => tab.active);
        if ((active?.id || "") !== (sourceTabID || "") || active?.connected) return;

        clearReconnectTimer();
        const c = target;
        if (!c) {
            await window.__noxaTabs?.quickConnectLast?.(fallbackTarget || null,
                () => generation !== reconnectGeneration || sessionGeneration !== state.sessionGeneration);
            return;
        }
        // Intentional Disconnect clears lastConnect to suppress automatic
        // reconnect. Restore only the in-memory successful record after the
        // user explicitly chooses the tray action.
        state.lastConnect = { ...c };
        chatUI.beginReconnectAnnouncementBatch();
        $("conn-pill").textContent = "reconnecting now…";
        const connected = await attemptReconnect(state.lastConnect, { sourceTabID });
        if (connected) return;
        if (!current()) return;
        chatUI.cancelReconnectAnnouncementBatch();
        if (autoReconnectEnabled()) scheduleReconnect();
        else showLogin();
    } finally {
        reconnectRequestPending = false;
    }
}

window.runtime.EventsOn("tray_reconnect", () => { void reconnectLastServerNow(); });
window.runtime.EventsOn("tray_disconnect", () => { void disconnect(); });

async function disconnect() {
    // Clear reconnect intent synchronously. CloseTab owns the actual
    // intentional connection edge and publishes it before tab replacement.
    const sourceServerGeneration = state.serverGeneration;
    const sourceTabID = state.activeTabID;
    const wasVisiblyConnected = $("conn-pill").classList.contains("up");
    const ownsSource = () => sourceServerGeneration === state.serverGeneration
        && sourceTabID === state.activeTabID;
    reconnectGeneration++;
    state.lastConnect = null; // intentional disconnect: no reconnect
    clearReconnectTimer();
    chatUI.cancelReconnectAnnouncementBatch();
    try {
        await window.go.main.App.DisconnectTab(sourceTabID);
    } catch {
        // The local cancellation above is still intentional even when the
        // bridge is already gone. Surface one actionable connection warning
        // instead of leaking an unhandled tray-event rejection.
        if (wasVisiblyConnected && ownsSource()) {
            toast("disconnect failed", "warn", "conn");
            playEvent("connection_failed");
        }
    }
}

// Go emits this before disconnecting an active connected tab and before a
// replacement tab's reset/replay batch. It centralizes menu, tray and tab-X
// teardown into one exactly-once user-facing connection edge.
window.runtime.EventsOn("intentional_disconnect", (tabID) => {
    if (String(tabID || "") !== state.activeTabID) return;
    clearSpeech();
    if (state.settings?.notify_connection !== false) toast("Disconnected", "info", "conn");
    playEvent("connection_disconnected");
});

window.runtime.EventsOn("disconnected", () => {
    state.sessionGeneration++;
    const unexpected = !!state.lastConnect;
    if (unexpected && state.settings?.notify_connection !== false) toast("Connection lost", "warn", "conn");
    if (unexpected) playAlert("connection_lost", { delay: 1200 });
    sysMsg("disconnected from server");
    // (32/33) whisper state is per-connection: client IDs and the server-side
    // whisper list do not survive a reconnect.
    activeWhisperers.clear();
    state.whisperArmed = false;
    state.whisperTargetUID = "";
    state.whisperPrev = null;
    state.myChannelID = 0;
    state.myClientID = "";
    resetVoiceSession();
    state.selectedClientID = "";
    state.isGuest = true;
    state.myStatus = "";
    state.canSetInvisible = false;
    setDetailsOpen(false);
    $("conn-pill").textContent = "offline";
    $("conn-pill").classList.remove("up");
    stopQualitySampler();

    // Unexpected loss reconnects by default. An explicit false setting opts
    // out; intentional disconnects clear lastConnect before this event.
    scheduleReconnect();
});

window.runtime.EventsOn("servererror", (msg) => {
    const text = String(msg).replace(/^\d+:\s*/, "");
    toast(text || "The server rejected that action", "warn");
    if (/^insufficient permission[: ]|^permission denied\b/i.test(text)) playAlert("permission_denied");
    else playEvent("server_error");
});

// (282) the Go side maintains settings of its own (recents on every connect),
// so the merged blob it pushes is the authoritative cache — without this the
// recents list stays frozen at the value read once at startup.
window.runtime.EventsOn("settings_update", (s) => {
    state.settings = s;
    refreshUserAudio();
    renderVoiceHints();
    void updateSoundOutput();
    void applyLiveAudioSettings().catch((error) => toast("Audio settings: " + error.message, "warn"));
});

// (320) recordRecentChannel tracks the last 5 joined channels per server
// address in settings (debounced persist).
function recordRecentChannel(channelID) {
    const s = state.settings;
    if (!s || !channelID || !state.lastConnect) return;
    s.recent_channels = s.recent_channels || {};
    const key = state.lastConnect.addr;
    const list = [channelID, ...(s.recent_channels[key] || []).filter((x) => x !== channelID)];
    s.recent_channels[key] = list.slice(0, 5);
    debouncedSaveSettings();
}

// (320) recentChannels returns the current server's recent channel IDs.
function recentChannels() {
    if (!state.lastConnect || !state.settings?.recent_channels) return [];
    return state.settings.recent_channels[state.lastConnect.addr] || [];
}

// ---------------------------------------------------------------------------
// Presence: auto-away (308)
// ---------------------------------------------------------------------------

let idleTimer = null;
let activityRevision = 0;

// noteActivity resets the idle timer; after auto_away_minutes without input
// the client sets itself away, restoring on the next activity.
function noteActivity() {
    const scope = capturePresenceScope();
    const revision = ++activityRevision;
    restorePresenceOnActivity(scope);
    if (idleTimer) clearTimeout(idleTimer);
    const minutes = state.settings?.auto_away_minutes ?? 15;
    if (minutes <= 0 || !scope.clientID) return;
    idleTimer = setTimeout(async () => {
        if (revision !== activityRevision || !presenceIsCurrent(scope)) return;
        if (await setPresence("away", "auto-away", scope)) sysMsg("auto-away after " + minutes + " min idle");
    }, minutes * 60000);
}

["mousemove", "keydown", "mousedown"].forEach((ev) =>
    document.addEventListener(ev, noteActivity, { passive: true }));

// ---------------------------------------------------------------------------
// Connection quality pill (333) + reconnect timeline (332)
// ---------------------------------------------------------------------------

// qualityFromPing classifies the connection from the smoothed RTT: good
// <100ms, fair <250ms, poor above (no RTT yet = unknown).
function qualityFromPing(pingMs, known) {
    if (!known) return "";
    if (pingMs < 100) return "good";
    if (pingMs < 250) return "fair";
    return "poor";
}

let qualityTimer = null;
let qualityAgeTimer = null;
let qualitySamplerEpoch = 0;
let lastQualitySample = null;

function qualitySampleAge(at, now = Date.now()) {
    const seconds = Math.max(0, Math.floor((now - at) / 1000));
    if (seconds < 5) return "just now";
    if (seconds < 60) return `${seconds} second${seconds === 1 ? "" : "s"} ago`;
    const minutes = Math.floor(seconds / 60);
    return `${minutes} minute${minutes === 1 ? "" : "s"} ago`;
}

function renderQualitySample() {
    if (!lastQualitySample) return;
    const pill = $("conn-pill");
    const latency = $("voice-latency");
    const stale = Date.now() - lastQualitySample.at >= 15000;
    const quality = stale ? "stale" : lastQualitySample.quality;
    const text = `${lastQualitySample.pingMs} ms${stale ? " · stale" : ""}`;
    const label = `Server latency: ${lastQualitySample.pingMs} milliseconds, ${quality}`;
    // The existing age ticker runs each second; only change visible DOM when
    // the value or freshness actually changes. No layout reads or new timers.
    if (latency.textContent !== text) latency.textContent = text;
    if (latency.dataset.quality !== quality) latency.dataset.quality = quality;
    if (latency.getAttribute("aria-label") !== label) latency.setAttribute("aria-label", label);
    if (latency.hidden) latency.hidden = false;
    if (pill.dataset.quality !== quality) pill.dataset.quality = quality;
    pill.title = `connection quality: ${lastQualitySample.quality} ` +
        `(RTT ${lastQualitySample.pingMs} ms, sampled ${qualitySampleAge(lastQualitySample.at)})`;
    const title = `Server round-trip latency, ${quality}. Sampled ${qualitySampleAge(lastQualitySample.at)}. Open server information.`;
    if (latency.title !== title) latency.title = title;
}

function startQualitySampler() {
    stopQualitySampler();
    const epoch = qualitySamplerEpoch;
    const generation = state.serverGeneration;
    const tabID = state.activeTabID;
    let inFlight = false;
    if (state.myClientID) {
        $("voice-latency").hidden = false;
        $("voice-latency").textContent = "— ms";
        $("voice-latency").setAttribute("aria-label", "Server latency: waiting for a sample");
    }
    const sample = async () => {
        if (!state.myClientID || inFlight) return;
        inFlight = true;
        const clientID = state.myClientID;
        try {
            const info = await window.go.main.App.GetClientInfoForTab(tabID, clientID);
            if (epoch !== qualitySamplerEpoch || generation !== state.serverGeneration ||
                clientID !== state.myClientID) return;
            const q = qualityFromPing(info.ping_ms, Number.isFinite(info.ping_ms) && info.ping_ms >= 0);
            if (q) {
                lastQualitySample = { quality: q, pingMs: info.ping_ms, at: Date.now() };
                renderQualitySample();
            }
        } catch {
            // Keep the last successful sample, but age it so stale telemetry
            // is never presented as current.
            if (epoch === qualitySamplerEpoch) renderQualitySample();
        } finally {
            inFlight = false;
        }
    };
    sample();
    qualityTimer = setInterval(sample, 5000);
    qualityAgeTimer = setInterval(renderQualitySample, 1000);
}

function stopQualitySampler() {
    qualitySamplerEpoch++;
    if (qualityTimer) {
        clearInterval(qualityTimer);
        qualityTimer = null;
    }
    if (qualityAgeTimer) {
        clearInterval(qualityAgeTimer);
        qualityAgeTimer = null;
    }
    lastQualitySample = null;
    const pill = $("conn-pill");
    delete pill.dataset.quality;
    pill.title = pill.classList.contains("up") ? "" : "Offline — no current RTT sample";
    const latency = $("voice-latency");
    latency.hidden = true;
    latency.textContent = "";
    latency.removeAttribute("data-quality");
    latency.removeAttribute("aria-label");
    latency.removeAttribute("title");
}

// ---------------------------------------------------------------------------
// Snapshot & events
// ---------------------------------------------------------------------------

// (307) client_id -> channel_id at the time of user_left: going invisible and
// coming back is announced as leave+join, and user_joined has no channel.
// Only a returning client consumes its entry and the snapshot arrives once at
// login, so the map is capped: clients that leave for good would otherwise
// accumulate for the whole session.
const lastKnownChannel = new Map();
const LAST_CHANNEL_MAX = 200;

function actionSoundsSuppressed() {
    return !!state.replayingTabID;
}

// A tab switch replays the other tab's journal so the view can be rebuilt.
// That history is not live join/leave activity and must never play cues.
window.runtime.EventsOn("tab_replay_done", (tabID) => {
    if (String(tabID || "") !== state.replayingTabID) return;
    state.replayingTabID = "";
    // Identity resolution may have won or lost the race with this marker.
    // In either case, this resolves a new tab's first own-channel cue exactly
    // once, while restored tabs have no pending cue to play.
    syncOwnChannel({ audible: false });
});

window.runtime.EventsOn("snapshot", (json) => {
    const snap = parseRuntimeObject(json);
    if (!snap) return;
    state.canSetInvisible = snap.can_set_invisible === true;
    state.channels = [];
    state.clients = [];
    lastKnownChannel.clear(); // the snapshot is authoritative
    for (const root of snap.root_channels || []) flattenChannel(root);
    for (const client of snap.unassigned_clients || []) state.clients.push(client);
    // Snapshot replay can beat the async ClientID lookup during a tab switch
    // or reconnect. Reconcile here when the identity is already known; the
    // identity completion path calls the same helper for the opposite order.
    syncOwnChannel();
    // (317) blocked users are locally muted on sight; (318) contact nickname
    // history updates from presence; (383) buddy alerts; (389) channel watch.
    applyBlockAndContacts();
    expandMyBranch(); // (302)
    resolveTrackUsers();
    videoRefreshNames(); // (61/73) tile labels follow the refreshed client list
    window.__noxaNotify?.checkBuddyOnline();
    window.__noxaNotify?.checkChannelWatch();
    renderTree();
});

// syncOwnChannel makes channel ownership independent of whether the snapshot
// / user_moved event or the active-tab ClientID lookup finishes first.
function syncOwnChannel({ audible = true } = {}) {
    reconcileSpatialVoice();
    void refreshPermissions();
    if (!state.myClientID) return;
    const me = state.clients.find((c) => c.client_id === state.myClientID);
    if (!me) return;
    state.myStatus = me.status || "";
    state.myPriority = !!me.priority_speaker;
    $("voice-prio").classList.toggle("active", state.myPriority);
    $("voice-prio").setAttribute("aria-pressed", String(state.myPriority));
    const channelID = Number(me.channel_id) || 0;
    if (state.myChannelID === channelID) {
        // Identity can resolve while journal replay is still muted. In that
        // ordering it has already recorded our initial channel, so replay_done
        // must flush the pending new-tab join from this equality branch.
        const initialCuePending = state.pendingInitialChannelCueTabID === state.activeTabID;
        if (initialCuePending && channelID > 0 && !actionSoundsSuppressed()) {
            playEvent("own_channel_join");
            state.pendingInitialChannelCueTabID = "";
        }
        ensureVoiceForChannel();
        return;
    }
    const previousChannelID = state.myChannelID;
    state.myChannelID = channelID;
    if (channelID > 0) setDeafened(false);
    const initialCuePending = state.pendingInitialChannelCueTabID === state.activeTabID;
    let playedCue = false;
    if ((audible || initialCuePending) && !actionSoundsSuppressed()) {
        if (channelID > 0) {
            if (previousChannelID > 0) playEvent("own_channel_switch");
            else playEvent("own_channel_join");
            playedCue = true;
        } else if (previousChannelID > 0) {
            playEvent("own_channel_leave");
            playedCue = true;
        }
    }
    if (playedCue && initialCuePending) state.pendingInitialChannelCueTabID = "";
    expandMyBranch();
    applyChannelAudio();
    chatUI.onMyChannelChanged();
    window.__noxaFiles?.onChannelChanged?.();
    renderTree();
    ensureVoiceForChannel();
}

// applyBlockAndContacts mutes blocked users locally and records contact
// nickname history from the fresh snapshot (317/318). Settings persist is
// debounced.
function applyBlockAndContacts() {
    const s = state.settings;
    if (!s) return;
    refreshUserAudio();
    let dirty = false;
    for (const contact of s.contacts || []) {
        const online = state.clients.find((c) => c.unique_id === contact.unique_id);
        if (!online || !online.nickname) continue;
        contact.nick_history = contact.nick_history || [];
        if (contact.nick_history[contact.nick_history.length - 1] !== online.nickname) {
            contact.nick_history.push(online.nickname);
            if (contact.nick_history.length > 8) contact.nick_history.shift();
            dirty = true;
        }
    }
    if (dirty) debouncedSaveSettings();
}

let saveSettingsTimer = null;

// debouncedSaveSettings persists settings at most once per 2s (318/320).
function debouncedSaveSettings() {
    if (saveSettingsTimer) clearTimeout(saveSettingsTimer);
    saveSettingsTimer = setTimeout(() => {
        if (state.settings) window.go.main.App.SaveSettings(state.settings);
    }, 2000);
}

function flattenChannel(node) {
    state.channels.push({
        ChannelID: node.ChannelID,
        ParentID: node.ParentID,
        Name: node.Name,
        HasIcon: !!node.HasIcon,
        // (304) lock icon — the field is read under both spellings so a json
        // tag on the server's Channel struct cannot silently drop the lock.
        HasPassword: !!(node.has_password ?? node.HasPassword),
        ClientCount: node.ClientCount || 0, // (303) [n/max]
        Topic: node.Topic || "",
        Description: node.Description || "",
        MaxClients: node.MaxClients || 0,
        OpusBitrate: node.OpusBitrate || 0,
        OpusFEC: !!node.OpusFEC,
        OpusDTX: !!node.OpusDTX,
        OpusStereo: !!node.OpusStereo,
        // (114) the snapshot marshals Go field names, so slow mode arrives as
        // SlowModeSeconds. Dropping it here is why the client could never show
        // or edit the rate limit it is subject to.
        SlowModeSeconds: node.SlowModeSeconds || 0,
        OrderIndex: node.OrderIndex || 0,
    });
    for (const c of node.clients || []) state.clients.push(c);
    for (const child of node.children || []) flattenChannel(child);
}

window.runtime.EventsOn("event", (json) => {
    const env = parseRuntimeObject(json);
    if (!env) return;
    const d = env.data || {};
    switch (env.type) {
        case "user_joined": {
            // (307) the server omits channel_id on user_joined, and an
            // invisible→visible return is announced as leave+join: restore the
            // channel remembered from the leave so the user is not stranded in
            // channel 0 ("no channel") until the next user_moved.
            const existing = state.clients.find((client) => client.client_id === d.client_id);
            const joinedChannel = d.channel_id ?? lastKnownChannel.get(d.client_id) ?? existing?.channel_id ?? 0;
            lastKnownChannel.delete(d.client_id);
            if (existing) {
                // Auth's join can follow a snapshot already containing self.
                // Keep presence, bot and media metadata from that snapshot.
                existing.unique_id = d.unique_id ?? existing.unique_id;
                existing.nickname = d.nickname ?? existing.nickname;
                existing.channel_id = joinedChannel;
            } else {
                state.clients.push({ client_id: d.client_id, unique_id: d.unique_id, nickname: d.nickname, channel_id: joinedChannel, is_speaking: false });
            }
            if (d.client_id !== state.myClientID) {
                chatUI.sysJoinLeave(d.nickname || d.unique_id || "someone", "joined"); // (130/131)
                if (joinedChannel === state.myChannelID && state.myChannelID !== 0) {
                    // (385) joins in my channel dispatch through the matrix.
                    window.__noxaNotify?.notify("join_leave", (d.nickname || "someone") + " joined your channel",
                        { channelID: state.myChannelID, className: "joins", kind: "info",
                            soundEvent: "user_join", noSound: actionSoundsSuppressed() });
                }
            }
            break;
        }
        case "user_left": {
            const was = state.clients.find((c) => c.client_id === d.client_id);
            state.clients = state.clients.filter((c) => c.client_id !== d.client_id);
            activeWhisperers.delete(d.client_id); // (32) a re-join whispers afresh
            if (was) {
                if (was.channel_id) {
                    lastKnownChannel.set(d.client_id, was.channel_id);
                    // insertion order: drop the oldest unclaimed entry first.
                    while (lastKnownChannel.size > LAST_CHANNEL_MAX) {
                        lastKnownChannel.delete(lastKnownChannel.keys().next().value);
                    }
                }
                chatUI.sysJoinLeave(was.nickname || was.unique_id || "someone", "left"); // (130/131)
                if (was.client_id !== state.myClientID && was.channel_id === state.myChannelID && state.myChannelID !== 0) {
                    window.__noxaNotify?.notify("join_leave", (was.nickname || "someone") + " left your channel",
                        { channelID: state.myChannelID, className: "joins", kind: "info",
                            soundEvent: "user_leave", noSound: actionSoundsSuppressed() });
                }
            }
            videoTrackRemoved(d.client_id);
            recomputeDucking();
            break;
        }
        case "user_moved": {
            const c = state.clients.find((c) => c.client_id === d.client_id);
            const previousRemoteChannelID = Number(c?.channel_id) || 0;
            const nextChannelID = Number(d.channel_id) || 0;
            if (c) c.channel_id = nextChannelID;
            if (d.client_id === state.myClientID) {
                const previousChannelID = state.myChannelID;
                const forcedMove = nextChannelID > 0 && nextChannelID !== previousChannelID
                    && d.by_client_id && d.by_client_id !== state.myClientID;
                state.myChannelID = nextChannelID;
                if (nextChannelID > 0 && nextChannelID !== previousChannelID) setDeafened(false);
                let playedOwnCue = false;
                if (!actionSoundsSuppressed()) {
                    if (nextChannelID > 0 && nextChannelID !== previousChannelID) {
                        if (forcedMove) playAlert("moved_by_admin", { effect: previousChannelID > 0 ? "own_channel_switch" : "own_channel_join" });
                        else if (previousChannelID > 0) playEvent("own_channel_switch");
                        else playEvent("own_channel_join");
                        playedOwnCue = true;
                    } else if (nextChannelID === 0 && previousChannelID > 0) {
                        playEvent("own_channel_leave");
                        playedOwnCue = true;
                    }
                }
                // A new tab can finish its replay in channel 0, then receive
                // its first live self-move. That live cue fulfills the pending
                // initial transition; leave no marker for a later equality
                // sync to replay the same join.
                if (playedOwnCue && state.pendingInitialChannelCueTabID === state.activeTabID) {
                    state.pendingInitialChannelCueTabID = "";
                }
                expandMyBranch(); // (302)
                applyChannelAudio();
                chatUI.onMyChannelChanged(); // (103/111) load history + header for the new channel
                window.__noxaFiles?.onChannelChanged?.(); // (256) file browser follows the channel
                recordRecentChannel(nextChannelID); // (320) recent channels
                ensureVoiceForChannel();
            } else if (c && nextChannelID === state.myChannelID && state.myChannelID !== 0
                && previousRemoteChannelID !== nextChannelID) {
                window.__noxaNotify?.notify("join_leave", (c.nickname || "someone") + " moved into your channel",
                    { channelID: state.myChannelID, className: "joins", kind: "info",
                        soundEvent: "user_move_in", noSound: actionSoundsSuppressed() });
            } else if (c && previousRemoteChannelID === state.myChannelID && state.myChannelID !== 0
                && previousRemoteChannelID !== nextChannelID) {
                window.__noxaNotify?.notify("join_leave", (c.nickname || "someone") + " moved out of your channel",
                    { channelID: previousRemoteChannelID, className: "joins", kind: "info",
                        soundEvent: "user_move_out", noSound: actionSoundsSuppressed() });
            }
            recomputeDucking();
            break;
        }
        case "channel_created":
            state.channels.push({ ChannelID: d.channel_id, ParentID: d.parent_id || 0, Name: d.name, HasIcon: false });
            break;
        case "channel_deleted": {
            // New servers include the complete cascaded subtree; older ones
            // send only channel_id. Seed from every payload ID, then expand
            // through the cached tree so legacy and partial payloads remove
            // the same complete subtree. Normalize string IDs at the event
            // boundary before any state cleanup.
            const deleted = new Set([d.channel_id, ...(Array.isArray(d.channel_ids) ? d.channel_ids : [])]
                .map(Number).filter((id) => id > 0));
            if (deleted.size === 0) break;
            const childrenByParent = new Map();
            for (const channel of state.channels) {
                const channelID = Number(channel.ChannelID);
                const parentID = Number(channel.ParentID || 0);
                if (channelID <= 0) continue;
                if (!childrenByParent.has(parentID)) childrenByParent.set(parentID, []);
                childrenByParent.get(parentID).push(channelID);
            }
            const queue = [...deleted];
            for (let index = 0; index < queue.length; index++) {
                for (const childID of childrenByParent.get(queue[index]) || []) {
                    if (deleted.has(childID)) continue;
                    deleted.add(childID);
                    queue.push(childID);
                }
            }
            state.channels = state.channels.filter((channel) => !deleted.has(Number(channel.ChannelID)));
            let selfDisplaced = deleted.has(Number(state.myChannelID));
            for (const client of state.clients) {
                const channelID = Number(client.channel_id ?? client.ChannelID ?? 0);
                if (!deleted.has(channelID)) continue;
                client.channel_id = 0;
                if ("ChannelID" in client) client.ChannelID = 0;
                if (client.client_id === state.myClientID) selfDisplaced = true;
            }
            for (const [clientID, channelID] of lastKnownChannel) {
                if (deleted.has(Number(channelID))) lastKnownChannel.delete(clientID);
            }
            for (const channelID of deleted) {
                state.collapsedChannels.delete(channelID);
                state.expandedVirtual.delete(channelID);
            }
            if (selfDisplaced) {
                if (state.myChannelID > 0 && !actionSoundsSuppressed()) playEvent("own_channel_leave");
                state.myChannelID = 0;
            }
            chatUI.onChannelsDeleted([...deleted]);
            if (selfDisplaced) {
                chatUI.onMyChannelChanged();
                window.__noxaFiles?.onChannelChanged?.();
                ensureVoiceForChannel();
            }
            recomputeDucking();
            break;
        }
        case "channel_updated": {
            const ch = state.channels.find((c) => c.ChannelID === d.channel_id);
            if (ch) {
                ch.Topic = d.topic || "";
                ch.Description = d.description || "";
                ch.MaxClients = d.max_clients || 0;
                ch.OpusBitrate = d.opus_bitrate || 0;
                ch.OpusFEC = !!d.opus_fec;
                ch.OpusDTX = !!d.opus_dtx;
                ch.OpusStereo = !!d.opus_stereo;
                ch.SlowModeSeconds = d.slow_mode_seconds || 0; // (114)
                ch.OrderIndex = d.order_index || 0;            // (163)
                // a re-parent moves the row, so the cached ancestry has to
                // follow it or the next edit dialog offers a stale parent.
                ch.ParentID = d.parent_id || 0;
                if (d.channel_id === state.myChannelID) applyChannelAudio();
                chatUI.refreshHeader();
            }
            break;
        }
        case "priority_speaker_changed": {
            const c = state.clients.find((c) => c.client_id === d.client_id);
            if (c) c.priority_speaker = d.active;
            if (d.client_id === state.myClientID) {
                state.myPriority = d.active;
                $("voice-prio").classList.toggle("active", d.active);
                $("voice-prio").setAttribute("aria-pressed", String(!!d.active));
            }
            sysMsg(clientName(d.client_id) + (d.active ? " is now a priority speaker" : " is no longer a priority speaker"));
            recomputeDucking();
            break;
        }
        // (307-309) presence status updates.
        case "status_changed": {
            const c = state.clients.find((c) => c.client_id === d.client_id);
            if (c) {
                c.status = d.status || "";
                c.status_message = d.message || "";
            }
            if (d.client_id === state.myClientID) state.myStatus = d.status || "";
            break;
        }
        // (321/322) poke: toast + sound + taskbar flash.
        case "poke":
            // (385) pokes dispatch through the notification matrix.
            window.__noxaNotify?.notify("poke",
                `poke from ${d.from_nickname || "someone"}${d.message ? ": " + d.message : ""}`,
                { className: "messages", kind: "warn" });
            break;
        // (32) directed whisper signal — the server sends it only to the
        // targets of an active whisper, so its mere arrival means "whispered
        // at me". speaking_changed's broadcast whisper flag cannot say that.
        case "whisper":
            setWhisperActive(d.from_client_id || "", d.from_unique_id || "",
                d.from_nickname || "", !!d.speaking);
            break;
        case "speaking_changed": {
            const c = state.clients.find((c) => c.client_id === d.client_id);
            if (c) c.is_speaking = d.speaking;
            // (343) announce speaking events to the screen-reader region.
            if (d.speaking) window.__noxaPolish?.announce((c ? c.nickname || c.unique_id : "someone") + " started speaking");
            updateTalkBanner();
            videoSpeaking(d.client_id, d.speaking);
            recomputeDucking();
            break;
        }
        case "chat":
            addChat(d);
            return;
        // wave-5b chat events — handled by chat-ui.js; no tree refresh needed
        // (chat-ui triggers renderTree itself when unread badges change).
        case "chat_edited":
            chatUI.onChatEdited(d);
            return;
        case "chat_deleted":
            chatUI.onChatDeleted(d);
            return;
        case "chat_pinned":
            chatUI.onChatPinned(d, true);
            return;
        case "chat_unpinned":
            chatUI.onChatPinned(d, false);
            return;
        case "chat_reaction":
            chatUI.onChatReaction(d);
            return;
        case "poll_changed":
            refreshPolls(d);
            return;
        case "conversation_changed":
            void conversationChanged(d);
            return;
        case "private_call":
            void privateCallChanged(d);
            void conversationChanged();
            return;
        case "private_call_signal":
            void privateCallSignal(d);
            return;
        case "position": {
            reconcileSpatialVoice();
            const member = state.clients.find(client => client.client_id === d.client_id);
            if (state.settings?.positional_audio && d.channel_id === state.myChannelID && member?.channel_id === state.myChannelID && d.client_id !== state.myClientID) {
                spatialVoice?.remote(d.client_id, d);
                spatialVoice?.update(true);
            }
            return;
        }
        // (120) typing relay. Returns early like the other chat events: an
        // indicator changes nothing in the tree and must not trigger a redraw
        // of it on every keystroke of every user.
        case "typing":
            chatUI.onTyping(d);
            return;
        case "dm_delivered":
            chatUI.onDelivered(d);
            return;
        case "dm_read":
            chatUI.onRead(d);
            return;
        case "emoji_added":
            chatUI.onEmojiAdded();
            return;
        case "announcement":
            chatUI.onAnnouncement(d);
            return;
        case "server_shutdown":
            if (!actionSoundsSuppressed()) {
                state.lastConnect = null;
                clearReconnectTimer();
                playAlert("server_shutdown");
                toast("The server is shutting down.", "warn", "conn");
            }
            break;
        case "kicked": {
            const self = d.client_id === state.myClientID;
            const removal = d.ban ? "banned" : d.from_server ? "kicked" : "kicked_channel";
            const description = self ? (d.ban ? "You were banned from the server." : d.from_server ? "You were kicked from the server." : "You were removed from the channel.") : "Client " + (d.ban ? "banned" : "kicked");
            // (385) kicks dispatch through the notification matrix.
            window.__noxaNotify?.notify("kick", description + (d.reason ? " Reason: " + d.reason : ""),
                { className: "messages", kind: "warn", soundEvent: d.ban ? "ban" : "kick", noSound: self });
            if (self && !actionSoundsSuppressed()) {
                if (d.from_server || d.ban) {
                    state.lastConnect = null;
                    reconnectGeneration++;
                    clearReconnectTimer();
                }
                playAlert(removal);
            }
            if (state.settings?.sys_kick !== false) { // (130) category filter
                sysMsg("client " + d.client_id + " was kicked" + (d.reason ? " (" + d.reason + ")" : ""));
            }
            break;
        }
        case "stream_watch_started":
            if (!actionSoundsSuppressed() && isCurrentPublication(d)) playEvent("stream_watch_started");
            return;
        case "screenshare_changed": {
            // (73) remember who is sharing: the grid labels those tiles, and
            // the camera-off detector (61) must not mistake a still desktop
            // for a switched-off camera.
            const sharer = state.clients.find((c) => c.client_id === d.client_id);
            if (sharer) sharer.sharing = !!d.active;
            videoRefreshNames();
            sysMsg(clientName(d.client_id) + (d.active ? " started" : " stopped") + " screen sharing");
            break;
        }
        case "avatar_changed":
            state.avatars.delete(d.unique_id);
            fetchAvatar(d.unique_id);
            videoRefreshNames();
            break;
    }
    resolveTrackUsers();
    reconcileSpatialVoice();
    // (389) the snapshot arrives once at login, so only the live join/leave/
    // move events can ever show a channel crossing its watch threshold.
    window.__noxaNotify?.checkChannelWatch();
    renderTree();
    videoRefreshNames();
});

// ---------------------------------------------------------------------------
// Channel tree
// ---------------------------------------------------------------------------

// (303) recomputeClientCounts derives the [n/max] badge counts from the live
// client list: the snapshot's ClientCount is a point-in-time value that
// user_joined/left/moved never refresh. The hover card (8b) and windowed mode
// (349) read the same field.
function recomputeClientCounts() {
    const counts = new Map();
    for (const c of state.clients) counts.set(c.channel_id, (counts.get(c.channel_id) || 0) + 1);
    for (const ch of state.channels) ch.ClientCount = counts.get(ch.ChannelID) || 0;
}

// (302) expandMyBranch un-collapses my channel and its ancestors: a move into
// a branch the user collapsed earlier would otherwise hide the channel they
// are actually in.
function expandMyBranch() {
    let id = state.myChannelID;
    let guard = 0;
    while (id && guard++ < 1000) {
        state.collapsedChannels.delete(id);
        id = state.channels.find((c) => c.ChannelID === id)?.ParentID || 0;
    }
}

function renderTree() {
    recomputeClientCounts();
    const root = $("channel-tree");
    const focusState = captureTreeFocus(root);
    root.innerHTML = "";
    // (179) Hoisted server groups render as their own sections above the
    // channels; (177) section headers carry the group icon; (178) nickname
    // colors come from the first applicable group (clientRow).
    for (const h of P().hoistedGroups()) {
        const sec = document.createElement("div");
        sec.className = "hoist-section";
        const head = document.createElement("div");
        head.className = "hoist-head";
        head.innerHTML = `<span class="hoist-icon"></span><span class="hoist-name"></span>`;
        head.querySelector(".hoist-name").textContent = h.group.name;
        if (h.group.color) head.style.color = h.group.color;
        if (h.group.icon) head.querySelector(".hoist-icon").textContent = h.group.icon;
        sec.appendChild(head);
        for (const c of h.members) sec.appendChild(clientRow(c));
        root.appendChild(sec);
    }
    if (state.channels.length === 0) {
        const empty = document.createElement("div");
        empty.className = "empty-state";
        empty.textContent = "No channels yet";
        root.appendChild(empty);
    }
    const byParent = new Map();
    for (const ch of state.channels) {
        const key = ch.ParentID || 0;
        if (!byParent.has(key)) byParent.set(key, []);
        byParent.get(key).push(ch);
    }
    // (163) the manual sort index decides sibling order; sort is stable, so
    // equal indices keep the order the snapshot delivered them in.
    for (const list of byParent.values()) {
        list.sort((a, b) => (a.OrderIndex || 0) - (b.OrderIndex || 0));
    }
    // (319) live tree filter: matching channels/users stay; a channel stays
    // when it or a descendant or one of its users matches.
    if (state.treeFilter) {
        const q = state.treeFilter.toLowerCase();
        const keep = new Set();
        const matchCh = (ch) => {
            let ok = ch.Name.toLowerCase().includes(q) ||
                state.clients.some((c) => c.channel_id === ch.ChannelID &&
                    (c.nickname || c.unique_id || "").toLowerCase().includes(q));
            for (const child of byParent.get(ch.ChannelID) || []) ok = matchCh(child) || ok;
            if (ok) keep.add(ch.ChannelID);
            return ok;
        };
        for (const ch of byParent.get(0) || []) matchCh(ch);
        for (const [p, list] of byParent) {
            byParent.set(p, list.filter((ch) => keep.has(ch.ChannelID)));
        }
    }
    for (const ch of byParent.get(0) || []) renderChannel(root, ch, byParent, 0);
    if (state.treeFilter && !root.querySelector(".channel, .client")) {
        const empty = document.createElement("div");
        empty.className = "empty-state";
        empty.setAttribute("role", "status");
        empty.textContent = "No matching channels or users";
        root.appendChild(empty);
    }
    renderDirectTargets();
    filterConversations();
    renderClientCard();
    chatUI.refreshHeader(); // (111) topic/title follows tree + channel updates
    renderWorkspace();
    syncTrayVoice();
    restoreTreeFocus(root, focusState);
}

function captureTreeFocus(root) {
    const active = document.activeElement;
    if (!(active instanceof HTMLElement) || !root.contains(active)) return null;
    const kind = active.classList.contains("channel") ? "channel" : active.classList.contains("client") ? "client" : "";
    if (!kind) return null;
    const dataKey = kind === "channel" ? "chid" : "clid";
    const key = active.dataset[dataKey];
    const matches = [...root.querySelectorAll(`.${kind}`)].filter((row) => row.dataset[dataKey] === key);
    return { kind, key, occurrence: Math.max(0, matches.indexOf(active)) };
}

function restoreTreeFocus(root, focusState) {
    if (!focusState) return;
    const dataKey = focusState.kind === "channel" ? "chid" : "clid";
    const matches = [...root.querySelectorAll(`.${focusState.kind}`)]
        .filter((row) => row.dataset[dataKey] === focusState.key);
    const target = matches[focusState.occurrence] || matches.at(-1);
    target?.focus({ preventScroll: true });
}

function setChannelExpanded(channelID, expanded) {
    if (expanded) {
        state.collapsedChannels.delete(channelID);
        state.expandedVirtual.add(channelID);
    } else {
        state.collapsedChannels.add(channelID);
        state.expandedVirtual.delete(channelID);
    }
    renderTree();
}

function renderChannel(parentEl, ch, byParent, depth) {
    const tabID = state.activeTabID, generation = state.serverGeneration;
    const node = document.createElement("div");
    node.className = "channel-node";
    node.style.setProperty("--depth", depth);
    const el = document.createElement("div");
    el.className = "channel" + (ch.ChannelID === state.myChannelID ? " mine" : "");
    el.dataset.chid = ch.ChannelID;
    el.tabIndex = 0; // (298) keyboard navigation
    el.setAttribute("role", "treeitem"); // (343)
    el.setAttribute("aria-label", "channel " + ch.Name + (ch.HasPassword ? ", password protected" : ""));
    el.innerHTML = `<span class="ch-disclosure" aria-hidden="true">${icon("chevron")}</span><span class="ch-icon">${icon("speaker")}</span><span class="ch-name"></span>`;
    el.querySelector(".ch-name").textContent = ch.Name;
    // (387) muted channel icon.
    if (window.__noxaNotify?.channelOverride?.(ch.ChannelID)?.muted) {
        const mute = document.createElement("span");
        mute.className = "ch-lock";
        mute.textContent = " 🔕";
        mute.title = "channel muted (notifications off)";
        el.querySelector(".ch-name").appendChild(mute);
    }
    // (304) password lock; (303) client count [n/max].
    if (ch.HasPassword) {
        const lock = document.createElement("span");
        lock.className = "ch-lock";
        lock.textContent = " 🔒";
        lock.title = "password-protected channel";
        el.querySelector(".ch-name").appendChild(lock);
    }
    if (ch.ClientCount > 0 || ch.MaxClients > 0) {
        const count = document.createElement("span");
        count.className = "ch-count mono";
        count.textContent = `${ch.ClientCount}${ch.MaxClients > 0 ? "/" + ch.MaxClients : ""}`;
        el.appendChild(count);
    }
    // (104) unread badge; accent variant when it includes a mention of me.
    const ub = chatUI.unreadFor(ch.ChannelID);
    if (ub && ub.n > 0) {
        const badge = document.createElement("span");
        badge.className = "ch-unread" + (ub.mention ? " mention" : "");
        badge.textContent = ub.n > 99 ? "99+" : ub.n;
        el.appendChild(badge);
    }
    el.onclick = async () => {
        if (generation !== state.serverGeneration) return;
        const groupView = privateGroupViewToken();
        try {
            const err = await window.go.main.App.JoinChannelForTab(tabID, ch.ChannelID);
            if (err && generation === state.serverGeneration) toast("join failed: " + err, "warn");
            if (!err && generation === state.serverGeneration && tabID === state.activeTabID && groupView && groupView === privateGroupViewToken()) await chatUI.openChannelTab(ch.ChannelID);
        } catch (err) {
            if (generation === state.serverGeneration) toast("join failed: " + err, "warn");
        }
    };
    // (163) drag a channel onto another to take that channel's slot among its
    // siblings. The header is separate from its member/child branch, so a
    // member drag can never be relabelled as a channel drag.
    el.draggable = true;
    el.addEventListener("dragstart", (e) => {
        if (e.target !== el) return;
        e.dataTransfer.setData("text/noxa-chid", String(ch.ChannelID));
        e.dataTransfer.setData("text/noxa-tab", tabID);
        e.dataTransfer.setData("text/noxa-generation", String(generation));
        e.dataTransfer.effectAllowed = "move";
    });
    // (305) drag users onto a channel to move them there.
    el.addEventListener("dragover", (e) => {
        if (e.dataTransfer.types.includes("text/noxa-uid") ||
            e.dataTransfer.types.includes("text/noxa-chid")) {
            e.preventDefault();
            el.classList.add("drop-active");
        }
    });
    el.addEventListener("dragleave", () => el.classList.remove("drop-active"));
    el.addEventListener("drop", async (e) => {
        e.preventDefault();
        // sub-channels are nested inside their parent's element, so without
        // this a drop on a child also fires every ancestor's handler.
        e.stopPropagation();
        el.classList.remove("drop-active");
        if (generation !== state.serverGeneration || e.dataTransfer.getData("text/noxa-tab") !== tabID ||
            e.dataTransfer.getData("text/noxa-generation") !== String(generation)) return;
        const chid = Number(e.dataTransfer.getData("text/noxa-chid"));
        if (chid) {
            await reorderChannel(chid, ch, tabID, generation);
            return;
        }
        const uid = e.dataTransfer.getData("text/noxa-uid");
        const clientID = e.dataTransfer.getData("text/noxa-client");
        const target = state.clients.find((c) => c.client_id === clientID && c.unique_id === uid);
        if (!target) return;
        try {
            const err = target.client_id === state.myClientID
                ? await window.go.main.App.JoinChannelForTab(tabID, ch.ChannelID)
                : await window.go.main.App.MoveClientForTab(tabID, target.client_id, ch.ChannelID);
            if (err && generation === state.serverGeneration) toast("move failed: " + err, "warn");
        } catch (err) {
            if (generation === state.serverGeneration) toast("move failed: " + err, "warn");
        }
    });
    node.appendChild(el);
    parentEl.appendChild(node);

    // (302) collapsed channels hide their members and children; (349) in
    // windowed mode every channel off my branch counts as collapsed unless
    // the user explicitly expanded it (double-click).
    let collapsed = state.collapsedChannels.has(ch.ChannelID);
    if (window.__noxaPolish?.virtualizeEnabled?.()) {
        collapsed = state.collapsedChannels.has(ch.ChannelID) ||
            (!window.__noxaPolish.myBranchIDs().has(ch.ChannelID) &&
                !state.expandedVirtual.has(ch.ChannelID));
    }
    const expandable = state.clients.some((c) => c.channel_id === ch.ChannelID) ||
        (byParent.get(ch.ChannelID) || []).length > 0;
    if (expandable) el.setAttribute("aria-expanded", String(!collapsed));
    el.querySelector(".ch-disclosure").onclick = (event) => {
        event.stopPropagation();
        if (expandable) setChannelExpanded(ch.ChannelID, collapsed);
    };
    if (!collapsed) {
        const members = state.clients.filter((c) => c.channel_id === ch.ChannelID);
        if (members.length > 0) {
            const list = document.createElement("div");
            list.className = "channel-members";
            list.setAttribute("role", "group");
            list.setAttribute("aria-label", ch.Name + " members");
            for (const c of members) list.appendChild(clientRow(c));
            node.appendChild(list);
        }
        const children = byParent.get(ch.ChannelID) || [];
        if (children.length > 0) {
            const branch = document.createElement("div");
            branch.className = "channel-children";
            branch.setAttribute("role", "group");
            branch.setAttribute("aria-label", ch.Name + " subchannels");
            for (const child of children) renderChannel(branch, child, byParent, depth + 1);
            node.appendChild(branch);
        }
    }
}

// (163) reorderChannel puts the dragged channel strictly before or after the
// drop target according to its current direction of travel. The named field
// list is deliberate: parent_id
// 0 is the legitimate "move to root" value and so has no sentinel, and a
// re-parent drops the server's whole permission cache — so "parent" is only
// named when the drop actually changes the parent.
async function reorderChannel(draggedID, target, tabID, generation) {
    if (draggedID === target.ChannelID) return;
    const dragged = state.channels.find((c) => c.ChannelID === draggedID);
    if (!dragged) return;
    const reparent = (dragged.ParentID || 0) !== (target.ParentID || 0);
    const draggedPos = state.channels.findIndex((c) => c.ChannelID === draggedID);
    const targetPos = state.channels.findIndex((c) => c.ChannelID === target.ChannelID);
    const movingDown = draggedPos >= 0 && targetPos >= 0 && draggedPos < targetPos;
    const targetOrder = target.OrderIndex || 0;
    const order = movingDown ? targetOrder + 1 : targetOrder - 1;
    const current = () => generation === state.serverGeneration && tabID === state.activeTabID;
    if (!Number.isInteger(order) || order < -2147483648 || order > 2147483647) {
        toast(t("roles.channel.orderLimit"), "warn");
        return;
    }
    try {
        const { openRoleChannel } = await import("./channel-lifecycle-ui.js");
        if (!current()) return;
        openRoleChannel(reparent ? "channel_move" : "channel_edit", draggedID, {
            destinationID: target.ParentID || 0, orderIndex: order,
        });
    } catch (err) {
        if (current()) toast("channel reorder failed: " + String(err), "warn");
    }
}

function clientRow(c) {
    const tabID = state.activeTabID, generation = state.serverGeneration;
    const row = document.createElement("div");
    const speakingHere = state.myChannelID !== 0 && c.channel_id === state.myChannelID && c.is_speaking;
    row.className = "client" + (speakingHere ? " speaking" : "") +
        (state.multiSelect.has(c.client_id) ? " selected" : "") +
        (c.status === "away" || c.status === "busy" || c.status === "invisible" ? " " + c.status : "");
    row.dataset.clid = c.client_id;
    row.tabIndex = 0; // (298) keyboard navigation
    row.setAttribute("role", "treeitem"); // (343)
    row.setAttribute("aria-selected", String(state.multiSelect.has(c.client_id)));
    row.setAttribute("aria-label", (c.nickname || c.unique_id || "user") +
        (c.status ? ", " + c.status : "") + (speakingHere ? ", speaking" : "") +
        (c.priority_speaker ? ", priority speaker" : "") +
        (c.client_id === state.myClientID && state.muted ? ", muted" : "") +
        (c.client_id === state.myClientID && state.deafened ? ", deafened" : ""));
    // (140/305) users are draggable (group assign in the manager; move by
    // dropping onto a channel).
    if (c.unique_id) {
        row.draggable = true;
        row.addEventListener("dragstart", (e) => {
            e.dataTransfer.setData("text/noxa-uid", c.unique_id);
            e.dataTransfer.setData("text/noxa-tab", tabID);
            e.dataTransfer.setData("text/noxa-generation", String(generation));
            e.dataTransfer.setData("text/noxa-client", c.client_id);
            e.dataTransfer.effectAllowed = "copy";
        });
    }
    const av = document.createElement("span");
    av.className = "avatar";
    av.dataset.uid = c.unique_id;
    const dataUrl = state.avatars.get(c.unique_id);
    if (dataUrl) {
        setSafeImage(av, dataUrl);
    } else {
        av.textContent = initials(c.nickname || c.unique_id || "?");
        fetchAvatar(c.unique_id);
    }
    const name = document.createElement("span");
    name.className = "client-name";
    name.textContent = (c.nickname || c.unique_id) + (c.client_id === state.myClientID ? t("workspace.you") : "");
    // (178) nickname color from the first applicable server group.
    const gColor = P().groupColorFor(c.unique_id);
    if (gColor) name.style.color = gColor;
    const g = P().primaryGroup(c.unique_id);
    if (g) name.title = `${t("roles.title")}: ${(c.roles || []).map((r) => r.name).join(", ")}`;
    row.appendChild(av);
    row.appendChild(name);
    // (310) group badge next to the name. Groups without an icon get a text
    // chip in the group colour instead of nothing — colour and hoisting are
    // settable on their own, so an icon is not what makes a group visible.
    if (g) {
        row.appendChild(roleChip(g));
    }
    // (307) priority speaker: the flag is broadcast for every client, so it
    // belongs on their row and not only on my own voice-bar button.
    if (c.priority_speaker) {
        const pr = document.createElement("span");
        pr.className = "status-icons";
        pr.textContent = " ★";
        pr.title = "priority speaker";
        row.appendChild(pr);
    }
    // (307-309) presence status icons; (381) invisible marker (admin view).
    if (c.status === "away" || c.status === "busy" || c.status === "invisible") {
        const st = document.createElement("span");
        st.className = "status-icons";
        st.textContent = c.status === "away" ? " 🕐" : c.status === "busy" ? " ⛔" : " 👻";
        st.title = c.status + (c.status_message ? ": " + c.status_message : "");
        row.appendChild(st);
    }
    // (10) Own status icons: muted / deafened / screen sharing.
    if (c.client_id === state.myClientID) {
        const icons = document.createElement("span");
        icons.className = "status-icons";
        icons.innerHTML = icon(state.muted ? "micOff" : "mic") +
            (state.deafened ? icon("headphonesOff") : "") + (state.screenSharing ? icon("screen") : "");
        row.appendChild(icons);
        // (347) DND shows on own status icons.
        if (window.__noxaPolish?.dndActive?.()) {
            const dnd = document.createElement("span");
            dnd.className = "status-icons";
            dnd.textContent = " 🌙";
            dnd.title = "do not disturb active";
            row.appendChild(dnd);
        }
    } else if (isUserMuted(c.unique_id)) {
        // (2) Local-mute icon for users muted locally by me.
        const icons = document.createElement("span");
        icons.className = "status-icons";
        icons.textContent = " 🔕";
        icons.title = "muted locally";
        row.appendChild(icons);
    }
    if (speakingHere) {
        const voice = document.createElement("span");
        voice.className = "client-voice-state";
        voice.title = "Talking in your channel";
        voice.setAttribute("aria-label", "talking");
        voice.innerHTML = "<i></i><i></i><i></i>";
        row.appendChild(voice);
    }
    row.onclick = (e) => {
        e.stopPropagation();
        // (306) ctrl/shift multi-select; plain click selects just this user.
        if (e.ctrlKey || e.metaKey) {
            if (state.multiSelect.has(c.client_id)) state.multiSelect.delete(c.client_id);
            else state.multiSelect.add(c.client_id);
        } else if (e.shiftKey && state.selectedClientID) {
            const rows = state.clients.filter((x) => x.channel_id === c.channel_id);
            const ids = rows.map((x) => x.client_id);
            const a = ids.indexOf(state.selectedClientID);
            const b = ids.indexOf(c.client_id);
            if (a >= 0 && b >= 0) {
                for (const id of ids.slice(Math.min(a, b), Math.max(a, b) + 1)) state.multiSelect.add(id);
            }
        } else {
            state.multiSelect = new Set([c.client_id]);
        }
        state.selectedClientID = c.client_id;
        setDetailsOpen(true);
        renderTree();
    };
    return row;
}

function initials(name) {
    const parts = name.trim().split(/\s+/);
    return (parts[0][0] + (parts.length > 1 ? parts[1][0] : "")).toUpperCase();
}

function clientName(clientID) {
    const c = state.clients.find((c) => c.client_id === clientID);
    return c ? (c.nickname || c.unique_id) : clientID;
}

// uidName resolves a unique ID to a nickname (whisper targets are addressed by
// unique ID, not client ID).
function uidName(uniqueID) {
    const c = state.clients.find((c) => c.unique_id === uniqueID);
    return c ? (c.nickname || c.unique_id) : uniqueID;
}

// ---------------------------------------------------------------------------
// Incoming voice whispers (32/33)
// ---------------------------------------------------------------------------

// activeWhisperers holds the senders currently whispering to me: the signal
// repeats (it rides every speaking transition), so notifying on the rising
// edge only keeps one burst to one sound + flash.
const activeWhisperers = new Set();

// setWhisperActive records a whisper start/stop from the server's receive-side
// whisper signal and fires the notification on the rising edge (32).
function setWhisperActive(clientID, uniqueID, nickname, active) {
    const key = clientID || uniqueID;
    if (!key) return;
    if (clientID === state.myClientID || (uniqueID && uniqueID === state.myUniqueID)) return;
    if (!active) {
        activeWhisperers.delete(key);
        return;
    }
    if (activeWhisperers.has(key)) return;
    activeWhisperers.add(key);
    onWhisperReceived(clientID, uniqueID, nickname);
}

// onWhisperReceived plays the whisper sound and flashes the taskbar through
// the notification matrix (32), and remembers the sender so the whisper-reply
// hotkey has a target (33).
function onWhisperReceived(clientID, uniqueID, nickname) {
    const c = state.clients.find((x) => x.client_id === clientID) ||
        state.clients.find((x) => x.unique_id === uniqueID);
    const uid = uniqueID || c?.unique_id || "";
    // (317) blocked users stay silent here too.
    if (uid && (state.settings?.blocked_users || []).includes(uid)) return;
    if (uid) state.lastWhispererUID = uid;
    const who = nickname || c?.nickname || uid || clientID || "someone";
    trayMention();
    window.__noxaNotify?.notify("whisper", who + " is whispering to you",
        { uid, className: "messages", kind: "warn", noSound: !state.settings?.whisper_sound });
}

// (290) trayMention badges the tray for something directed at me in the ACTIVE
// tab — the backend only counts background tabs, whose frames it relays. A
// focused window means the user is already looking at it.
function trayMention() {
    if (document.hasFocus()) return;
    window.go.main.App.TrayMention();
}

window.addEventListener("focus", () => window.go.main.App.TrayClearMentions());

// ---------------------------------------------------------------------------
// Avatars
// ---------------------------------------------------------------------------

async function fetchAvatar(uniqueID) {
    if (!uniqueID || state.avatars.has(uniqueID) || state.avatarPending.has(uniqueID)) return;
    const generation = state.serverGeneration;
    state.avatarPending.add(uniqueID);
    try {
        const data = await window.go.main.App.GetAvatarForTab(state.activeTabID, uniqueID);
        if (generation !== state.serverGeneration) return;
        state.avatars.set(uniqueID, imageDataURL(data));
    } catch {
        if (generation !== state.serverGeneration) return;
        state.avatars.set(uniqueID, null);
    } finally {
        if (generation === state.serverGeneration) state.avatarPending.delete(uniqueID);
    }
    document.querySelectorAll(`.avatar[data-uid="${CSS.escape(uniqueID)}"]`).forEach((el) => {
        const url = state.avatars.get(uniqueID);
        if (url) setSafeImage(el, url);
    });
    renderClientCard();
}

// ---------------------------------------------------------------------------
// Client card (details pane)
// ---------------------------------------------------------------------------

function renderClientCard() {
    renderMember();
}

// The inspector is contextual: keep the workspace wide until the user selects
// somebody or explicitly asks for server/permission details. At narrower
// widths CSS presents the same panel as a drawer instead of shrinking chat.
function setDetailsOpen(open) {
    const details = $("details");
    const toggle = $("details-toggle");
    const restoreFocus = !open && details.contains(document.activeElement);
    document.body.classList.toggle("details-collapsed", !open);
    details.setAttribute("aria-hidden", String(!open));
    toggle.setAttribute("aria-expanded", String(open));
    if (restoreFocus) toggle.focus();
}

$("details-close").onclick = () => setDetailsOpen(false);
$("details-toggle").onclick = () => setDetailsOpen(true);
document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && !document.body.classList.contains("details-collapsed") &&
        !document.querySelector(".dlg-overlay")) {
        setDetailsOpen(false);
    }
});

// ---------------------------------------------------------------------------
// Chat
// ---------------------------------------------------------------------------

function addChat(d) {
    // (317) blocked users' messages are hidden.
    if (d.from_unique_id && (state.settings?.blocked_users || []).includes(d.from_unique_id)) {
        return;
    }
    // File logging per scope (Chat setting) — kept from the pre-5b renderer.
    const scope = d.direct || d.e2e ? (d.offline ? "dm · offline" : "dm") : (d.channel_id ? "channel" : "chat");
    const s = state.settings;
    if (s) {
        const line = `[${scope}] ${d.from}: ${d.text}`;
        if ((scope === "channel" && s.log_channel_chat) ||
            (scope.startsWith("dm") && s.log_private_chat) ||
            (scope === "chat" && s.log_server_chat)) {
            window.go.main.App.LogChat(line);
        }
    }
    // Rendering, tabs, notification classification, badges, and receipts live
    // in chat-ui.js. Only a successfully routed E2EE message is a DM; global
    // chat also has no channel_id and must never enter the direct path.
    const result = chatUI.addChat(d);
    if (result?.incomingDM) {
        // Track the last DM sender for the Ctrl+R whisper-reply hotkey (33).
        if (d.from_client_id) {
            const sender = state.clients.find((c) => c.client_id === d.from_client_id);
            if (sender) state.lastWhispererUID = sender.unique_id;
        }
        trayMention(); // (290)
    }
    if (result?.announcement) announceLive(result.announcement);
}

function sysMsg(text) {
    const log = $("chat-log");
    const el = document.createElement("div");
    el.className = "msg sys";
    el.textContent = "— " + text + " —";
    log.appendChild(el);
    log.scrollTop = log.scrollHeight;
}

function setDirectTargetVisible(visible) {
    $("chat-target-picker").classList.toggle("hidden", !visible);
    if (visible) renderDirectTargets();
    else hideDirectTargets();
}

function directTargetClients(filter = "") {
    const needle = filter.trim().toLowerCase();
    return state.clients
        .filter((client) => client.client_id !== state.myClientID && client.unique_id)
        .filter((client) => !needle || `${client.nickname || ""} ${client.unique_id}`.toLowerCase().includes(needle))
        .sort((a, b) => {
            const aHere = a.channel_id === state.myChannelID ? 0 : 1;
            const bHere = b.channel_id === state.myChannelID ? 0 : 1;
            return aHere - bHere || (a.nickname || a.unique_id).localeCompare(b.nickname || b.unique_id);
        });
}

function hideDirectTargets() {
    $("chat-target-options").classList.add("hidden");
    $("chat-target").setAttribute("aria-expanded", "false");
}

function renderDirectTargets(show = false) {
    const input = $("chat-target");
    const options = $("chat-target-options");
    const clients = directTargetClients(input.value);
    options.innerHTML = "";
    if (clients.length === 0) {
        const empty = document.createElement("div");
        empty.className = "target-empty";
        empty.textContent = state.clients.length > 1 ? "No matching connected members" : "No other members connected";
        options.appendChild(empty);
    }
    for (const client of clients) {
        const option = document.createElement("button");
        option.type = "button";
        option.className = "target-option" + (client.channel_id === state.myChannelID ? " same-channel" : "");
        option.setAttribute("role", "option");
        const channel = state.channels.find((item) => item.ChannelID === client.channel_id);
        option.innerHTML = `<span class="target-option-dot"></span><span class="target-option-copy"><strong></strong><small class="target-option-meta mono"></small></span><span class="target-option-channel"></span>`;
        option.querySelector("strong").textContent = client.nickname || client.unique_id;
        option.querySelector("small").textContent = client.unique_id;
        option.querySelector(".target-option-channel").textContent = channel?.Name || "no channel";
        option.onclick = () => {
            input.value = client.unique_id;
            hideDirectTargets();
            input.dispatchEvent(new Event("change", { bubbles: true }));
            $("chat-text").focus();
        };
        options.appendChild(option);
    }
    if (show && !$("chat-target-picker").classList.contains("hidden")) {
        options.classList.remove("hidden");
        input.setAttribute("aria-expanded", "true");
    }
}

$("chat-target").addEventListener("focus", () => renderDirectTargets(true));
$("chat-target").addEventListener("input", () => renderDirectTargets(true));
$("chat-target-toggle").onclick = () => {
    const opening = $("chat-target-options").classList.contains("hidden");
    renderDirectTargets(opening);
    if (opening) $("chat-target").focus();
};
document.addEventListener("pointerdown", (event) => {
    if (!event.target.closest("#chat-target-picker")) hideDirectTargets();
});

$("chat-scope").onchange = () => setDirectTargetVisible($("chat-scope").value === "direct");

async function sendChat() {
    // Rich send flow (reply prefix, staged file uploads, DM tabs) is in
    // chat-ui.js (wave 5b); this wrapper keeps the __noxa export stable.
    await chatUI.sendMessage();
}

$("chat-send").onclick = sendChat;
$("chat-text").addEventListener("keydown", (e) => { if (e.key === "Enter") sendChat(); });

// ---------------------------------------------------------------------------
// Voice
// ---------------------------------------------------------------------------

let voiceSessionEpoch = 0;
let voiceStartPromise = null;
let voiceStartOwner = null;
let mediaLimitsSequence = 0;
let mediaLimitsRefresh = null;

// Invalidations carry no snapshot. Coalesce them around a fresh native read;
// an old tab/peer completion cannot update or tear down its replacement.
function refreshLiveMediaLimits() {
    const pc = state.pc;
    if (!pc) return; // Startup fetches again if its in-flight read was invalidated.
    const epoch = voiceSessionEpoch;
    const generation = state.serverGeneration;
    const session = state.sessionGeneration;
    const tabID = state.activeTabID;
    const clientID = state.myClientID;
    const ownsPeer = () => state.pc === pc && voiceSessionEpoch === epoch && state.serverGeneration === generation &&
        state.sessionGeneration === session && state.activeTabID === tabID && state.myClientID === clientID;
    if (mediaLimitsRefresh?.pc === pc && mediaLimitsRefresh.current()) return;
    const request = { pc, active: true };
    const current = () => request.active && mediaLimitsRefresh === request && ownsPeer();
    request.current = current;
    mediaLimitsRefresh = request;
    const starting = voiceStartPromise;
    let timer;
    const deadline = new Promise((_resolve, reject) => {
        timer = setTimeout(() => {
            request.active = false;
            reject(new Error(t("voice.mediaLimitsFailed")));
        }, 10000);
    });
    const update = (async () => {
        // Startup owns its initial offer and capture setup; never rebuild it
        // concurrently or reset its camera state after a live update completes.
        if (starting) await starting;
        while (current()) {
            const sequence = mediaLimitsSequence;
            const limits = await window.go.main.App.GetMediaLimitsForTab(tabID);
            if (!current()) return;
            if (sequence !== mediaLimitsSequence) continue;
            const result = await applyVideoLimits(limits, current);
            if (!current()) return;
            if (result?.dimensionsChanged) {
                // Refresh negotiated codecs while retaining the live transport;
                // the server's packet guards already enforce the new limits.
                await renegotiate(pc, generation, { iceRestart: true }, current);
            }
            request.appliedSequence = sequence;
            if (sequence === mediaLimitsSequence) return;
        }
    })();
    // Wails ignores callback promises. Contain rejection and invalidate pending
    // browser/bridge work before closing this exact peer on failure or timeout.
    void Promise.race([update, deadline]).catch(() => {
        if (mediaLimitsRefresh !== request || !ownsPeer()) return;
        request.active = false;
        resetVoiceSession();
        setVoiceStatus("voice unavailable");
        sysMsg(t("voice.mediaLimitsFailed"));
    }).finally(() => {
        clearTimeout(timer);
        request.active = false;
        if (mediaLimitsRefresh === request) {
            mediaLimitsRefresh = null;
            // An event can arrive after update resolves but before this promise
            // cleanup runs. It must not disappear into the completed request.
            if (ownsPeer() && request.appliedSequence !== mediaLimitsSequence) refreshLiveMediaLimits();
        }
    });
}

// Channel presence owns voice presence. There is deliberately no separate
// join/leave voice control: a confirmed local channel move establishes the
// session, while disconnecting or changing server tabs tears it down.
function ensureVoiceForChannel() {
    reconcileSpatialVoice();
    if (state.myChannelID > 0) stopPrivateCall();
    if (state.myChannelID <= 0) {
        if (state.pc || state.localStream || voiceStartPromise ||
            state.micState === "none" || state.micState === "denied") {
            resetVoiceSession();
        }
        return Promise.resolve(false);
    }
    if (voiceStartPromise) return voiceStartPromise;
    if (state.pc) {
        if (!streamSessionIsCurrent(state.pc)) {
            // A channel move retains microphone voice, but publication and
            // viewing consent belong to the channel that granted them.
            for (const track of state.localStream?.getVideoTracks() || []) { track.stop(); state.localStream.removeTrack(track); }
            state.shareStream?.getTracks().forEach(track => track.stop());
            state.shareStream = null;
            state.screenSharing = false;
            state.shareStarting = null;
            state.shareStopping = false;
            resetCameraState();
            startStreamSession(state.pc, videoTrackAdded, videoTrackRemoved);
            for (const receiver of state.pc.getReceivers()) {
                const track = receiver.track;
                const parsed = parseTrackID(track.id);
                const publisher = state.clients.find(c => String(c.client_id) === parsed.clientID);
                if (track.kind === "video") receiveStreamTrack(track, publisher);
                else if (parsed.slot === SLOT_SCREEN_AUDIO) receiveShareAudio(track, parsed.clientID);
            }
        }
        setVoiceStatus("voice on");
        return Promise.resolve(true);
    }
    const epoch = voiceSessionEpoch;
    const owner = {};
    const cancelled = new Promise(resolve => { owner.cancel = () => resolve(false); });
    voiceStartOwner = owner;
    setVoiceStatus("voice connecting…");
    const start = (async () => {
        try {
            return await startVoice(epoch);
        } catch (e) {
            // Reset releases startup ownership immediately, even when an old
            // browser/native operation has not returned yet.
            if (voiceStartOwner !== owner || epoch !== voiceSessionEpoch) return false;
            sysMsg("voice failed: " + e);
            toast("Voice could not start automatically", "warn", "conn");
            teardownVoice();
            resetVoiceUI();
            setVoiceStatus("voice unavailable");
            return false;
        }
    })();
    voiceStartPromise = Promise.race([start, cancelled]).finally(() => {
        if (voiceStartOwner === owner) {
            voiceStartOwner = null;
            voiceStartPromise = null;
        }
    });
    return voiceStartPromise;
}

async function readInitialMediaLimits(tabID, stillRelevant) {
    let active = true;
    let timer;
    const deadline = new Promise((_resolve, reject) => {
        timer = setTimeout(() => {
            active = false;
            reject(new Error(t("voice.mediaLimitsFailed")));
        }, 10000);
    });
    const read = (async () => {
        while (active && stillRelevant()) {
            const sequence = mediaLimitsSequence;
            const limits = await window.go.main.App.GetMediaLimitsForTab(tabID);
            if (!active || !stillRelevant()) return null;
            if (sequence === mediaLimitsSequence) return { limits, sequence };
        }
        return null;
    })();
    try { return await Promise.race([read, deadline]); }
    finally { active = false; clearTimeout(timer); }
}

function resetVoiceSession() {
    voiceSessionEpoch++;
    voiceStartOwner?.cancel();
    voiceStartOwner = null;
    voiceStartPromise = null;
    teardownVoice();
    resetVoiceUI();
    state.micState = "unknown";
    renderMicStatus($("mic-status"), "unknown");
}

// (25) Capture follows the joined channel's audio profile: a music channel is
// captured stereo with the browser's speech DSP off, everything else uses the
// user's own settings.
function audioConstraints() {
    return captureConstraints(state.channels.find((c) => c.ChannelID === state.myChannelID));
}

function microphoneFailureState(error) {
    return error?.name === "NotAllowedError" || error?.name === "SecurityError" ? "denied" : "none";
}

async function startVoice(expectedEpoch = voiceSessionEpoch) {
    const generation = state.serverGeneration;
    const tabID = state.activeTabID;
    const current = () => expectedEpoch === voiceSessionEpoch && state.serverGeneration === generation && state.activeTabID === tabID && state.myChannelID > 0;
    // Joining never requests a camera. A missing/denied microphone still
    // permits receiving media, sharing a screen, and explicitly enabling video.
    let micErr = null;
    let capturedStream;
    try {
        capturedStream = await navigator.mediaDevices.getUserMedia({
            audio: audioConstraints(),
            video: false,
        });
    } catch (e) {
        micErr = e;
        capturedStream = new MediaStream();
    }
    if (!current()) {
        capturedStream.getTracks().forEach((track) => track.stop());
        return false;
    }
    state.localStream = capturedStream;
    setMicState(micErr === null ? "ok" : microphoneFailureState(micErr));
    // (25) record which profile this capture was taken with, so a later move
    // only re-captures when the profile actually changes.
    markCaptureProfile(state.localStream.getAudioTracks()[0],
        state.channels.find((c) => c.ChannelID === state.myChannelID));
    $("local-video").srcObject = null;
    $("local-video").classList.add("hidden");

    // ICE servers delivered by the server at connect (STUN/TURN); an empty
    // list means "client defaults" (plain RTCPeerConnection).
    let iceServers = null;
    try {
        iceServers = await window.go.main.App.GetICEServersForTab(tabID);
    } catch { /* fall back to client defaults */ }
    let mediaLimits = null;
    let limitsSequence = mediaLimitsSequence;
    try {
        if (current()) {
            const snapshot = await readInitialMediaLimits(tabID, current);
            mediaLimits = snapshot?.limits;
            limitsSequence = snapshot?.sequence;
        }
    } catch {
        capturedStream.getTracks().forEach((track) => track.stop());
        if (state.localStream === capturedStream) state.localStream = null;
        if (current()) throw new Error(t("voice.mediaLimitsFailed"));
        return false;
    }
    if (!current()) {
        capturedStream.getTracks().forEach((track) => track.stop());
        if (state.localStream === capturedStream) state.localStream = null;
        return false;
    }
    state.mediaLimits = mediaLimits;
    const pc = iceServers && iceServers.length
        ? new RTCPeerConnection({ iceServers })
        : new RTCPeerConnection();
    // A replacement peer owns a new ICE-restart ladder. Dispose the previous
    // current owner's timer before publishing the replacement; callbacks from
    // that old peer can never affect the new one.
    if (state.pc && state.pc !== pc) resetICERestart(state.pc);
    state.pc = pc;
    state.voiceTabID = tabID;
    // A queued event can run between the read helper returning and this
    // continuation installing the peer. Reconcile it after startup's offer.
    if (limitsSequence !== mediaLimitsSequence) refreshLiveMediaLimits();

    const audioTrack = state.localStream.getAudioTracks()[0];
    if (audioTrack) {
        pc.addTransceiver(audioTrack, { direction: "sendrecv", streams: [state.localStream] });
    } else {
        pc.addTransceiver("audio", { direction: "recvonly" });
    }

    pc.onicecandidate = (e) => {
        if (current() && state.pc === pc && e.candidate) {
            // A candidate can arrive while teardown has already closed the
            // control bridge. There is nothing useful to show for that race,
            // but its rejected promise must not become an unhandled one.
            void window.go.main.App.SendICECandidateForTab(
                tabID, e.candidate.candidate, e.candidate.sdpMid || "", e.candidate.sdpMLineIndex || 0,
            ).catch(() => {});
        }
    };
    // (59) ICE restart: re-offer with iceRestart on failed/disconnected,
    // backing off 1s, 2s, 5s, 15s before giving up with a warning toast.
    pc.oniceconnectionstatechange = () => onICEStateChange(pc);
    const receivedTracks = new Map();
    startStreamSession(pc, videoTrackAdded, videoTrackRemoved);
    pc.ontrack = (e) => {
        if (!current() || state.pc !== pc) {
            e.track.stop();
            return;
        }
        // The server labels each track with its publisher and slot (see
        // parseTrackID in video.js), so tracks are attributed without SSRC
        // mapping and a publisher can own more than one of them.
        const { clientID, slot } = parseTrackID(e.track.id);
        const publisher = state.clients.find((c) => String(c.client_id) === clientID) || null;
        state.trackUsers.set(e.track.id, publisher
            ? { client_id: publisher.client_id, unique_id: publisher.unique_id, nickname: publisher.nickname }
            : { client_id: clientID, unique_id: "", nickname: "" });
        receivedTracks.set(e.track.id, e.track);
        e.track.addEventListener("ended", () => {
            if (!current() || state.pc !== pc || receivedTracks.get(e.track.id) !== e.track) return;
            receivedTracks.delete(e.track.id);
            state.trackUsers.delete(e.track.id);
            if (e.track.kind === "video") videoTrackRemoved(e.track.id);
        });
        if (e.track.kind === "video") {
            // (61/73) one grid tile per publisher video slot; removed on ended.
            receiveStreamTrack(e.track, publisher);
            return;
        }
        if (slot === SLOT_SCREEN_AUDIO) {
            receiveShareAudio(e.track, clientID);
            // (70) a sharer's system audio is not a second microphone.
            attachShareAudio(e.track, clientID, publisher);
            return;
        }
        // Audio: route through the WebAudio chain (per-user gain hook,
        // limiter, normalizer) instead of the video element.
        attachRemoteAudio(e.track, publisher);
    };

    await renegotiate(pc, state.serverGeneration, undefined, () => expectedEpoch === voiceSessionEpoch);

    if (expectedEpoch !== voiceSessionEpoch || state.pc !== pc || state.myChannelID <= 0) {
        pc.close();
        return false;
    }

    applyChannelAudio();
    resetCameraState();
    // (88) re-apply the persisted low-bandwidth mode to the fresh session.
    if (state.settings?.low_bandwidth) setLowBandwidth(true, false);
    startVoiceMonitor();
    startMicMeter(state.localStream);
    applyVoiceState();
    setVoiceStatus("voice on");
    return true;
}

// (24) applyChannelAudio applies the current channel's Opus settings to the
// outgoing audio: a maxBitrate cap on the sender encoding (0 = server
// default 32000 bits/s, i.e. no explicit cap) and a contentHint on the local
// track ("music" for stereo channels, "speech" otherwise).
async function applyChannelAudio() {
    if (!state.pc) return;
    const ch = state.channels.find((c) => c.ChannelID === state.myChannelID);
    if (!ch) return;
    const bitrate = ch.OpusBitrate || 0;
    const sender = state.pc.getSenders().find((s) => s.track && s.track.kind === "audio");
    if (sender) {
        const p = sender.getParameters();
        if (p.encodings?.length) {
            p.encodings[0].maxBitrate = bitrate > 0 ? bitrate : undefined;
            await sender.setParameters(p).catch(() => {});
        }
    }
    // (25) A move into or out of a music channel needs a fresh capture: the
    // stereo/DSP constraints cannot be changed on a live track.
    const { track, changed, error } = await applyCaptureProfile(state.pc, state.localStream, ch);
    if (changed) {
        startVoiceMonitor();
        startMicMeter(state.localStream);
        applyVoiceState();
    }
    if (track && !changed) track.contentHint = ch.OpusStereo ? "music" : "speech";
    return error;
}

// Serialize device swaps; settings_update and the Settings save can both ask
// for the same change. The capture constraints prevent a redundant recapture.
let liveAudioSettings = Promise.resolve();
function applyLiveAudioSettings() {
    liveAudioSettings = liveAudioSettings.catch(() => {}).then(async () => {
        const error = await applyChannelAudio();
        if (error) throw error;
        applyVoiceState();
        if (remoteChain.ctx) await selectAudioOutput(remoteChain.ctx);
        if (remoteChain.master) {
            remoteChain.master.gain.value = state.deafened ? 0 : Math.min(2, (state.settings?.volume ?? 100) / 100);
        }
        // Local preview elements must stay muted to avoid microphone feedback.
        applyOutputSettings($("remote-video"));
    });
    return liveAudioSettings;
}

// (13/14) Priority-speaker ducking: while another priority speaker in my
// channel is talking, everyone else is ducked (setDucking in audio.js);
// priority speakers themselves are exempt. Un-ducking is delayed 500 ms so
// sentence gaps don't pump the gain; the timer is cancelled when a priority
// speaker starts again.
let unduckTimer = null;

function recomputeDucking() {
    updateConversationDucking();
    const inMyChannel = state.clients.filter((c) => c.channel_id === state.myChannelID && c.channel_id !== 0);
    const prioAll = inMyChannel.filter((c) => c.priority_speaker);
    const anyOtherTalking = prioAll.some((c) => c.client_id !== state.myClientID && c.is_speaking);
    if (anyOtherTalking) {
        if (unduckTimer) {
            clearTimeout(unduckTimer);
            unduckTimer = null;
        }
        applyDucking(true, prioAll.map((c) => c.unique_id));
        return;
    }
    if (unduckTimer) clearTimeout(unduckTimer);
    unduckTimer = setTimeout(() => {
        unduckTimer = null;
        const stillTalking = state.clients.some((c) =>
            c.channel_id === state.myChannelID && c.channel_id !== 0 &&
            c.priority_speaker && c.client_id !== state.myClientID && c.is_speaking);
        if (!stillTalking) applyDucking(false, []);
    }, 500);
}

// (13) Priority-speaker self toggle in the voice bar.
let priorityRequest = null;
$("voice-prio").onclick = async () => {
    if (priorityRequest && mediaScopeIsCurrent(priorityRequest)) return;
    const scope = captureMediaScope();
    const active = !state.myPriority;
    priorityRequest = scope;
    let err;
    try { err = await window.go.main.App.SetPrioritySpeakerForTab(scope.tabID, active); }
    catch (error) { err = String(error); }
    if (priorityRequest !== scope) return;
    priorityRequest = null;
    if (!mediaScopeIsCurrent(scope)) return;
    if (err) {
        sysMsg("priority speaker failed: " + err);
        return;
    }
    // The authoritative role event/snapshot updates local state.
};

// (59) ICE restart ladder: on failed/disconnected, re-offer with iceRestart
// after 1s, 2s, 5s, then 15s; a connected/completed state resets the ladder.
// The server treats a re-offer as a full session replacement (idempotent).
const ICE_BACKOFF_MS = [1000, 2000, 5000, 15000];
let iceFailures = 0;
let iceTimer = null;
let iceFailureNotified = false;
let iceRestartPC = null;

function ownsICERestart(pc) {
    return state.pc === pc && iceRestartPC === pc;
}

function claimICERestart(pc) {
    if (state.pc !== pc) return false;
    if (iceRestartPC === pc) return true;
    // This can only be reached by the current peer. A prior owner's timer is
    // stale at this point and must not keep the current ladder blocked.
    if (iceTimer !== null) clearTimeout(iceTimer);
    iceRestartPC = pc;
    iceFailures = 0;
    iceTimer = null;
    iceFailureNotified = false;
    return true;
}

function onICEStateChange(pc) {
    if (pc !== state.pc) return;
    const s = pc.iceConnectionState;
    if (s === "connected" || s === "completed") {
        resetICERestart(pc);
        return;
    }
    if (s === "failed" || s === "disconnected") {
        scheduleICERestart(pc);
    }
}

function resetICERestart(pc) {
    if (!ownsICERestart(pc)) return;
    if (iceTimer !== null) clearTimeout(iceTimer);
    iceFailures = 0;
    iceFailureNotified = false;
    iceTimer = null;
    iceRestartPC = null;
}

function scheduleICERestart(pc) {
    if (state.pc !== pc || !claimICERestart(pc) || !ownsICERestart(pc)) return;
    if (iceTimer !== null) return;
    if (iceFailures >= ICE_BACKOFF_MS.length) {
        if (!iceFailureNotified) {
            iceFailureNotified = true;
            toast("Voice connection unstable — reconnect if it does not recover", "warn", "conn");
        }
        return;
    }
    const delay = ICE_BACKOFF_MS[iceFailures++];
    let timer = null;
    timer = setTimeout(async () => {
        if (!ownsICERestart(pc) || iceTimer !== timer) return;
        iceTimer = null;
        const s = pc.iceConnectionState;
        if (s === "connected" || s === "completed" || s === "closed") {
            resetICERestart(pc);
            return;
        }
        try {
            await renegotiate(pc, state.serverGeneration, { iceRestart: true }, () => ownsICERestart(pc));
        } catch {
            // Retry scheduling below owns failure feedback. Per-attempt chat
            // lines would flood the conversation during a bad network spell.
        }
        // Still not connected: oniceconnectionstatechange may not fire again
        // for a persistent failure, so chain the next backoff step explicitly.
        if (ownsICERestart(pc) && !["connected", "completed", "closed"].includes(pc.iceConnectionState)) {
            scheduleICERestart(pc);
        }
    }, delay);
    if (!ownsICERestart(pc)) {
        clearTimeout(timer);
        return;
    }
    iceTimer = timer;
}

function teardownVoice() {
    stopStreamSession();
    state.mediaLimits = null;
    stopVoiceMonitor();
    stopMicMeter();
    resetICERestart(state.pc);
    if (unduckTimer) {
        clearTimeout(unduckTimer);
        unduckTimer = null;
    }
    applyDucking(false, []);
    clearVideoGrid();
    if (state.shareStream) {
        state.shareStream.getTracks().forEach((t) => t.stop());
        state.shareStream = null;
        state.shareAudioSender = null;
        state.screenSharing = false;
        $("voice-screen").classList.remove("active");
        $("voice-screen").setAttribute("aria-pressed", "false");
    }
    // the transceiver belongs to the peer connection being closed: keeping the
    // reference would make the next session's share replaceTrack a dead sender.
    state.shareAudioTransceiver = null;
    state.shareVideoTransceiver = null;
    state.shareStarting = null;
    state.shareStopping = false;
    clearRegionBox(); // (71)
    detachRemoteAudio();
    if (state.pc) { state.pc.close(); state.pc = null; }
    state.voiceTabID = "";
    syncTrayVoice();
    if (state.localStream) {
        for (const t of state.localStream.getTracks()) t.stop();
        state.localStream = null;
    }
    resetCameraState(); // (85)
    $("local-video").classList.add("hidden");
    $("remote-video").classList.add("hidden");
}

// voiceStatusBase is the plain voice on/off text. renderVoiceStatus appends the
// whisper-reply marker (33): an armed reply reroutes every transmission, so it
// must not be invisible.
let voiceStatusBase = "voice off";

function setVoiceStatus(text) {
    voiceStatusBase = text;
    renderVoiceStatus();
}

function renderVoiceStatus() {
    renderVoiceHints();
    const key = { "voice on": "voice.on", "voice off": "voice.off", "voice connecting…": "voice.connecting", "voice unavailable": "voice.unavailable" }[voiceStatusBase];
    $("voice-status").textContent = (key ? t(key) : voiceStatusBase) +
        (state.whisperArmed ? " · " + t("polish.whisper", { name: uidName(state.whisperTargetUID) }) : "");
}

function resetVoiceUI() {
    setVoiceStatus("voice off");
    state.pttActive = false;
    $("ptt-btn").classList.remove("live");
    $("ptt-btn").setAttribute("aria-pressed", "false");
    state.myPriority = false;
    $("voice-prio").classList.remove("active");
    $("voice-prio").setAttribute("aria-pressed", "false");
    updateTalkBanner();
}

function setMicState(s) {
    state.micState = s;
    const el = $("mic-status");
    const videoOnly = !!state.localStream?.getVideoTracks?.().length;
    renderMicStatus(el, s, retryMicrophoneAccess, videoOnly, $("ptt-btn"));
    if (s === "ok") {
        $("ptt-btn").disabled = false;
    } else if (s === "none") {
        $("ptt-btn").disabled = true;
        sysMsg(videoOnly ? "no microphone found; joined voice video-only" :
            "no microphone found; voice capture unavailable");
    } else if (s === "denied") {
        $("ptt-btn").disabled = true;
        sysMsg(videoOnly ? "microphone access denied; joined voice video-only" :
            "microphone access denied; voice capture unavailable");
    }
}

let micRetryPromise = null;

// Retry only the missing microphone. Keeping the existing peer connection and
// local video stream avoids interrupting a working camera or screen share.
function retryMicrophoneAccess() {
    if (micRetryPromise) return micRetryPromise;
    micRetryPromise = retryMicrophoneCapture().finally(() => {
        micRetryPromise = null;
    });
    return micRetryPromise;
}

async function retryMicrophoneCapture() {
    if (state.myChannelID <= 0) return false;
    // A session where every capture device failed has no peer connection to
    // extend. In that case retry the normal audio-only capture flow.
    if (!state.pc || !state.localStream) {
        return ensureVoiceForChannel();
    }
    if (state.localStream.getAudioTracks().length > 0) {
        setMicState("ok");
        return true;
    }

    const peerConnection = state.pc;
    const localStream = state.localStream;
    const expectedEpoch = voiceSessionEpoch;
    const generation = state.serverGeneration;
    const current = () => state.pc === peerConnection && state.localStream === localStream &&
        voiceSessionEpoch === expectedEpoch && state.serverGeneration === generation && state.myChannelID > 0;
    let capturedStream;
    try {
        capturedStream = await navigator.mediaDevices.getUserMedia({ audio: audioConstraints() });
    } catch (error) {
        if (current()) setMicState(microphoneFailureState(error));
        return false;
    }

    const audioTrack = capturedStream.getAudioTracks()[0];
    if (!audioTrack) {
        capturedStream.getTracks().forEach((track) => track.stop());
        if (current()) setMicState("none");
        return false;
    }
    if (!current()) {
        capturedStream.getTracks().forEach((track) => track.stop());
        return false;
    }

    localStream.addTrack(audioTrack);
    markCaptureProfile(audioTrack, state.channels.find((channel) => channel.ChannelID === state.myChannelID));
    let transceiver = null;
    try {
        transceiver = peerConnection.addTransceiver(audioTrack, {
            direction: "sendrecv",
            streams: [localStream],
        });
        await renegotiate(peerConnection, generation);
        if (!current()) throw new DOMException("voice session changed", "AbortError");
        await applyChannelAudio();
        startVoiceMonitor();
        startMicMeter(localStream);
        applyVoiceState();
        setMicState("ok");
        setVoiceStatus("voice on");
        sysMsg("microphone connected");
        return true;
    } catch (error) {
        localStream.removeTrack(audioTrack);
        audioTrack.stop();
        await transceiver?.sender?.replaceTrack(null).catch(() => {});
        try {
            if (transceiver) transceiver.direction = "inactive";
        } catch { /* the peer connection may already be closed */ }
        if (!current()) return false;
        setMicState(state.micState === "denied" ? "denied" : "none");
        sysMsg("microphone retry failed: " + (error?.message || error));
        return false;
    } finally {
        for (const track of capturedStream.getTracks()) {
            if (track !== audioTrack) track.stop();
        }
    }
}

// Server->client ICE and renegotiation.
window.runtime.EventsOn("media_limits_changed", () => {
    mediaLimitsSequence++;
    refreshLiveMediaLimits();
});
$("login-addr").addEventListener("input", () => { $("login-accountpw").value = ""; });

window.runtime.EventsOn("ice", (json) => {
    if (!state.pc) return;
    const c = parseRuntimeObject(json);
    if (!c) return;
    state.pc.addIceCandidate({
        candidate: c.candidate,
        sdpMid: c.sdp_mid || null,
        sdpMLineIndex: c.sdp_mline_index ?? null,
    }).catch(() => {});
});

window.runtime.EventsOn("offer", (json) => {
    const pc = state.pc;
    const o = parseRuntimeObject(json);
    if (!pc || !o || typeof o.sdp !== "string") return;
    const generation = state.serverGeneration;
    // Wails does not observe a returned event-handler promise. Contain every
    // async rejection here so a failed renegotiation cannot surface globally.
    // A tab reset may replace both the active backend and its peer while the
    // offer is pending, so never answer through the new backend for old SDP.
    void answerRemoteOffer(pc, generation, o.sdp).catch(() => {});
});

// Mute / deafen / PTT ---------------------------------------------------------

function applyVoiceState() {
    if (!state.localStream) return;
    const mode = state.settings?.activation_mode || "ptt";
    let audible = !state.muted;
    // ptt and vad both gate transmission on the (hotkey- or VAD-driven)
    // pttActive flag; continuous always transmits.
    if (mode !== "continuous") audible = audible && state.pttActive;
    for (const t of state.localStream.getAudioTracks()) t.enabled = audible;
}

// Voice monitor: one interval driving VAD transmission, the mic meter's
// sibling level feed, the talking-while-muted warning (26), and the
// talking-to-empty-channel hint (27).
let mutedTalkStreak = 0, emptyStreak = 0, lastEmptyWarn = 0;
let voiceMonitorTrack = null;
let voiceMonitorSource = null;

function startVoiceMonitor() {
    stopVoiceMonitor();
    const audioTrack = state.localStream?.getAudioTracks()[0];
    if (!audioTrack) return;
    try {
        // The sender track is disabled by mute/PTT/VAD. Monitoring that same
        // track hears silence after VAD closes and can never reopen the gate.
        // Keep a local-only clone enabled so capture also stays available
        // while the window is hidden. This track is never sent to a peer.
        voiceMonitorTrack = audioTrack.clone();
        voiceMonitorTrack.enabled = true;
        state.voiceMonitorCtx = new (window.AudioContext || window.webkitAudioContext)();
        const ctx = state.voiceMonitorCtx;
        const src = ctx.createMediaStreamSource(new MediaStream([voiceMonitorTrack]));
        voiceMonitorSource = src;
        const analyser = ctx.createAnalyser();
        analyser.fftSize = 512;
        src.connect(analyser);
        void ctx.resume().catch(() => {});
        const buf = new Uint8Array(analyser.frequencyBinCount);
        let lastVoice = 0;
        state.vadMonitor = setInterval(() => {
            analyser.getByteTimeDomainData(buf);
            let sum = 0;
            for (const v of buf) sum += Math.abs(v - 128);
            const level = sum / buf.length / 128; // 0..1
            const mode = state.settings?.activation_mode || "ptt";
            const threshold = (state.settings?.vad_threshold ?? 50) / 100 * 0.2;

            // VAD transmission.
            if (mode === "vad") {
                if (level > threshold) {
                    lastVoice = Date.now();
                    if (!state.pttActive) setPTT(true);
                } else if (state.pttActive && Date.now() - lastVoice > 300) {
                    setPTT(false);
                }
            }

            // (26) talking while muted / PTT off.
            const blocked = state.muted || (mode !== "continuous" && !state.pttActive);
            if (blocked && level > threshold && state.settings?.warn_muted_talking !== false) {
                mutedTalkStreak++;
            } else {
                mutedTalkStreak = 0;
                $("mic-warning").classList.add("hidden");
            }
            if (mutedTalkStreak > 10 && (state.settings?.warn_muted_talking !== false)) {
                warnMutedTalking();
            }

            // (27) talking to an empty channel.
            const others = state.clients.filter((c) => c.channel_id === state.myChannelID && c.client_id !== state.myClientID);
            if (others.length === 0 && state.myChannelID !== 0 && level > threshold) {
                emptyStreak++;
            } else {
                emptyStreak = 0;
            }
            if (emptyStreak > 10 && (state.settings?.warn_empty_channel !== false)) {
                warnEmptyChannel();
            }
        }, 100);
    } catch {
        stopVoiceMonitor();
    }
}

function stopVoiceMonitor() {
    if (state.vadMonitor) {
        clearInterval(state.vadMonitor);
        state.vadMonitor = null;
    }
    if (voiceMonitorSource) {
        voiceMonitorSource.disconnect();
        voiceMonitorSource = null;
    }
    if (voiceMonitorTrack) {
        voiceMonitorTrack.stop();
        voiceMonitorTrack = null;
    }
    if (state.voiceMonitorCtx) {
        void state.voiceMonitorCtx.close().catch(() => {});
        state.voiceMonitorCtx = null;
    }
    mutedTalkStreak = 0;
    $("mic-warning").classList.add("hidden");
    emptyStreak = 0;
}

// (26) Keep the blocked-speech warning beside the microphone control.
function warnMutedTalking() {
    const b = $("mic-warning");
    b.textContent = t(state.muted ? "polish.mutedSpeech" : "polish.pttSpeech");
    b.classList.remove("hidden");
}

// (27) Talking-to-empty-channel hint.
function warnEmptyChannel() {
    if (Date.now() - lastEmptyWarn < 30000) return;
    lastEmptyWarn = Date.now();
    toast("Nobody is in your channel", "info", "alert");
}

// Output settings: volume + sink for remote media elements.
let lastOutputDevice = "";
let outputWarningShown = false;
let outputSelection = 0;

async function selectAudioOutput(target) {
    const deviceID = state.settings?.playback_device_id || "";
    if (deviceID !== lastOutputDevice) {
        lastOutputDevice = deviceID;
        outputWarningShown = false;
        outputSelection++;
    }
    if (typeof target.setSinkId !== "function") return;
    const selection = outputSelection;
    try {
        await target.setSinkId(deviceID);
    } catch {
        if (selection !== outputSelection || deviceID !== (state.settings?.playback_device_id || "") || target.state === "closed" || outputWarningShown) return;
        outputWarningShown = true;
        toast(t("audio.outputSwitchFailed"), "warn");
    }
}

function applyOutputSettings(el) {
    const s = state.settings || {};
    el.volume = Math.min(1, (s.volume ?? 100) / 100);
    void selectAudioOutput(el);
    el.muted = state.deafened;
    if (remoteChain.master) {
        remoteChain.master.gain.value = state.deafened ? 0 : Math.min(2, (s.volume ?? 100) / 100);
    }
}

// Remote audio WebAudio chain. One shared context carries every publisher:
// per-track source -> [per-track normalizer] -> per-user gain -> per-user mute
// -> master gain (volume) -> [limiter] -> destination. Per-publisher tracks
// (track ID = publisher client ID) make per-user volume/mute/auto-level
// audible; the registries themselves live in audio.js.
const remoteChain = { ctx: null, master: null };

function resumeRemoteAudio() {
    for (const { playback } of [...remoteTracks.values(), ...shareAudio.values()]) {
        if (playback.paused) void playback.play().catch(() => {});
    }
    void resumeAudioPlayback(remoteChain.ctx).catch(() => {
        toast("Voice playback could not start. Check your output device and try again.", "warn");
    });
}
window.addEventListener("pointerdown", resumeRemoteAudio, { passive: true });
window.addEventListener("keydown", resumeRemoteAudio);
window.addEventListener("focus", resumeRemoteAudio);
document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") resumeRemoteAudio();
});
// remoteTracks maps media track ID -> {src, gain, mute, uid} for per-track
// teardown when a publisher leaves or voice is stopped.
const remoteTracks = new Map();
let spatialVoice = null;
let positionReadPending = false;
let spatialScope = "";

function reconcileSpatialVoice() {
    const scope = JSON.stringify([state.activeTabID, state.serverGeneration, state.myChannelID, voiceSessionEpoch]);
    if (scope !== spatialScope) {
        spatialScope = scope;
        spatialVoice?.reset();
    }
    spatialVoice?.retainPeers(state.clients.filter(client => client.channel_id === state.myChannelID && client.client_id !== state.myClientID).map(client => client.client_id));
    spatialVoice?.update(!!state.settings?.positional_audio);
}

// The native file source is read only while the user has opted in and joined
// voice. Capture both server and voice scope before any asynchronous work.
setInterval(async () => {
    const enabled = !!state.settings?.positional_audio;
    reconcileSpatialVoice();
    if (!enabled || positionReadPending || !state.myChannelID || !state.pc || state.pc.connectionState === "closed") return;
    const { activeTabID: tabID, serverGeneration: generation, myChannelID: channelID } = state;
    const epoch = voiceSessionEpoch;
    const current = () => state.settings?.positional_audio && state.activeTabID === tabID && state.serverGeneration === generation && state.myChannelID === channelID && voiceSessionEpoch === epoch;
    positionReadPending = true;
    try {
        const position = await window.go.main.App.ReadPositionalInput();
        if (!current()) return;
        spatialVoice?.local(position);
        spatialVoice?.update(true);
        await window.go.main.App.PublishPositionForTab(tabID, { channel_id: channelID, context: position.context, x: position.x, y: position.y, z: position.z });
    } catch {
        // A game may not be running. Expiry restores ordinary voice playback.
    } finally { positionReadPending = false; }
}, 250);

// ensureRemoteChain builds the shared processing tail once per voice session.
function ensureRemoteChain() {
    if (remoteChain.ctx) { resumeRemoteAudio(); return true; }
    try {
        const ctx = new (window.AudioContext || window.webkitAudioContext)();
        remoteChain.ctx = ctx;
        spatialVoice = new SpatialVoice(ctx);
        const master = ctx.createGain();
        remoteChain.master = master;
        master.gain.value = state.deafened ? 0 : Math.min(2, (state.settings?.volume ?? 100) / 100);

        // (52) voice limiter/compressor (default on). Gain normalization (53)
        // is per publisher and lives in attachRemoteAudio.
        let out = master;
        if (state.settings?.voice_limiter !== false) {
            const comp = makeLimiter(ctx);
            master.connect(comp);
            out = comp;
        }
        out.connect(ctx.destination);

        // Output device selection where supported (Chrome 110+).
        void selectAudioOutput(ctx);
        resumeRemoteAudio();
        return true;
    } catch (e) {
        sysMsg("remote audio chain failed: " + e);
        return false;
    }
}

// attachRemoteAudio adds one publisher's audio track to the shared chain.
// publisher is the resolved state.clients entry (or null when unknown); its
// unique ID keys the per-user volume/mute registry from audio.js.
function attachRemoteAudio(track, publisher) {
    detachRemoteTrack(track.id); // re-attach after an ICE restart replaces the track
    if (!ensureRemoteChain()) return;
    try {
        const ctx = remoteChain.ctx;
        // a MediaStreamAudioSourceNode taps only the first audio track of the
        // stream it is built from, so the per-user volume (1) and local mute
        // (2) chains would all follow one publisher if several tracks share a
        // stream: wrap this track alone.
        const { src, playback } = createRemoteAudioSource(ctx, track);
        const gain = ctx.createGain();
        const mute = ctx.createGain();
        // (53) auto-level per publisher: keyed by track ID, inserted behind
        // this publisher's source and returning src unchanged when the setting
        // is off. A master-bus normalizer could not lift a quiet speaker out
        // of a loud one.
        const head = attachUserNormalizer(ctx, track.id, src);
        head.connect(gain);
        gain.connect(mute);
        spatialVoice.attach(track.id, mute, remoteChain.master, publisher?.client_id || "");

        const uid = publisher?.unique_id || "";
        const entry = { src, playback, gain, mute, uid };
        remoteTracks.set(track.id, entry);
        if (uid) registerUserChain(uid, gain, mute);

        track.addEventListener("ended", () => {
            if (remoteTracks.get(track.id) === entry) detachRemoteTrack(track.id);
        });
    } catch (e) {
        sysMsg("remote audio chain failed: " + e);
    }
}

// resolveTrackUsers re-attributes tracks whose renegotiation arrived before
// the publisher's user_joined event: at ontrack time the client list has no
// entry yet, and without a second pass the per-user volume (1) and local mute
// (2) chains are never registered for that publisher for the whole session.
function resolveTrackUsers() {
    for (const [trackID, u] of state.trackUsers) {
        if (u.unique_id) continue;
        const { clientID } = parseTrackID(trackID);
        const publisher = state.clients.find((c) => String(c.client_id) === clientID);
        if (!publisher) continue;
        state.trackUsers.set(trackID, {
            client_id: publisher.client_id, unique_id: publisher.unique_id, nickname: publisher.nickname,
        });
        const t = remoteTracks.get(trackID);
        if (t && !t.uid && publisher.unique_id) {
            spatialVoice?.peer(trackID, publisher.client_id);
            t.uid = publisher.unique_id;
            registerUserChain(publisher.unique_id, t.gain, t.mute);
        }
        // (14) the share chain needs the unique ID too, or its publisher can
        // never be exempted from ducking.
        const sa = shareAudio.get(clientID);
        if (sa && !sa.uid && publisher.unique_id) {
            sa.uid = publisher.unique_id;
            applyShareAudio(clientID);
        }
    }
}

// ---------------------------------------------------------------------------
// Shared system audio (70) — a screen share's audio arrives on its own slot
// and gets its own chain, NOT the publisher's voice chain.
//
// Chosen behaviour: the per-user volume slider and local mute (1/2) are
// controls for a PERSON's voice and only touch the microphone. A sharer's
// system audio has its own mute + volume on the screen tile's context menu,
// because the reason to mute someone is usually that they are noisy while you
// are watching what they share — killing the show with the talker is wrong.
// Priority-speaker ducking (14) does apply: it exists to make one voice
// audible over everything else, program audio included. The two controls are
// session-local; settings.user_volumes/muted_users stay the voice registry.
// ---------------------------------------------------------------------------

// shareAudio maps publisher client ID -> {trackID, src, gain, uid, volume, muted}.
const shareAudio = new Map();

// SHARE_DUCK_FACTOR must match audio.js's DUCK_FACTOR: these chains are not in
// its per-user registry, so setDucking cannot reach them.
const SHARE_DUCK_FACTOR = 0.25;
let shareDuckActive = false;
let shareDuckExempt = new Set();

// applyDucking is setDucking plus the share chains it cannot see.
function applyDucking(active, exceptUIDs) {
    shareDuckActive = active;
    shareDuckExempt = new Set(exceptUIDs || []);
    setDucking(active, exceptUIDs);
    for (const clid of shareAudio.keys()) applyShareAudio(clid);
}

function applyShareAudio(clientID) {
    const n = shareAudio.get(String(clientID));
    if (!n) return;
    const duck = shareDuckActive && !shareDuckExempt.has(n.uid) ? SHARE_DUCK_FACTOR : 1;
    n.gain.gain.value = (n.muted ? 0 : n.volume / 100) * duck;
}

function attachShareAudio(track, clientID, publisher) {
    const clid = String(clientID);
    detachShareAudio(clid); // re-attach after an ICE restart replaces the track
    if (!ensureRemoteChain()) return;
    try {
        const ctx = remoteChain.ctx;
        const { src, playback } = createRemoteAudioSource(ctx, track);
        const gain = ctx.createGain();
        src.connect(gain);
        // no auto-level (53) on program audio: it would pump on music and
        // game sound, which is not a quiet speaker that needs lifting.
        gain.connect(remoteChain.master);
        const entry = {
            trackID: track.id, src, playback, gain, uid: publisher?.unique_id || "",
            volume: 100, muted: false,
        };
        shareAudio.set(clid, entry);
        applyShareAudio(clid);
        track.addEventListener("ended", () => {
            if (shareAudio.get(clid) === entry) detachShareAudio(clid);
        });
    } catch (e) {
        sysMsg("shared audio chain failed: " + e);
    }
}

function detachShareAudio(clientID) {
    const n = shareAudio.get(String(clientID));
    if (!n) return;
    shareAudio.delete(String(clientID));
    n.playback.pause();
    n.playback.srcObject = null;
    try {
        n.gain.disconnect();
        n.src.disconnect();
    } catch { /* already disconnected */ }
}

// detachRemoteTrack removes one publisher's nodes from the shared chain.
function detachRemoteTrack(trackID) {
    const t = remoteTracks.get(trackID);
    if (!t) return;
    remoteTracks.delete(trackID);
    spatialVoice?.detach(trackID);
    t.playback.pause();
    t.playback.srcObject = null;
    if (t.uid) unregisterUserChain(t.uid);
    detachUserNormalizer(trackID); // (53) no-op when normalization is off
    try {
        t.mute.disconnect();
        t.gain.disconnect();
        t.src.disconnect();
    } catch { /* already disconnected */ }
}

function detachRemoteAudio() {
    for (const trackID of [...remoteTracks.keys()]) detachRemoteTrack(trackID);
    spatialVoice = null;
    for (const clid of [...shareAudio.keys()]) detachShareAudio(clid); // (70)
    detachAllUserNormalizers(); // (53) stops the shared auto-level ticker
    if (remoteChain.ctx) {
        remoteChain.ctx.close().catch(() => {});
        remoteChain.ctx = null;
        remoteChain.master = null;
    }
    state.trackUsers.clear();
}

function setPTT(active) {
    const scope = captureMediaScope();
    const epoch = voiceSessionEpoch;
    // (6) PTT release delay: PTT-up is deferred by the configured delay so
    // sentence ends aren't clipped. pttRelease handles cancel-on-repress.
    pttRelease(active, (effective) => {
        if (!mediaScopeIsCurrent(scope) || voiceSessionEpoch !== epoch) return;
        if (state.pttActive === effective) return;
        state.pttActive = effective;
        $("ptt-btn").classList.toggle("live", effective);
        $("ptt-btn").setAttribute("aria-pressed", String(effective));
        window.go.main.App.SetPTT(effective);
        applyVoiceState();
        // VAD continuously changes pttActive as speech starts and stops; only
        // physical push-to-talk actions earn an audible confirmation.
        if ((state.settings?.activation_mode || "ptt") === "ptt") {
            playEvent(effective ? "ptt_on" : "ptt_off");
        }
        updateTalkBanner();
    });
}

$("ptt-btn").addEventListener("mousedown", (e) => { e.preventDefault(); setPTT(true); });
["mouseup", "mouseleave"].forEach((ev) => $("ptt-btn").addEventListener(ev, () => setPTT(false)));
$("ptt-btn").addEventListener("keydown", (event) => {
    if (!isActivationKey(event.key) || event.repeat) return;
    event.preventDefault();
    setPTT(true);
});
$("ptt-btn").addEventListener("keyup", (event) => {
    if (!isActivationKey(event.key)) return;
    event.preventDefault();
    setPTT(false);
});
$("ptt-btn").addEventListener("blur", () => setPTT(false));

$("voice-mute").onclick = () => {
    state.muted = !state.muted;
    syncMuteButton($("voice-mute"), state.muted);
    window.go.main.App.SetMuted(state.muted);
    if (state.muted) playEvent("mic_off");
    else playEvent("mic_on");
    applyVoiceState();
    renderTree();
};
syncMuteButton($("voice-mute"), state.muted);

function setDeafened(on) {
    if (state.deafened === on) return;
    state.deafened = on;
    $("remote-video").muted = on;
    if (remoteChain.master) remoteChain.master.gain.value = on ? 0 : Math.min(2, (state.settings?.volume ?? 100) / 100);
    playEvent(on ? "deafen_on" : "deafen_off");
    renderTree();
}

let whisperReplyRequest = null;
window.runtime.EventsOn("hotkey", async (action) => {
    if (action === "mute_toggle") {
        $("voice-mute").click();
        return;
    }
    if (action === "deafen_toggle") {
        const { state, setDeafened, sysMsg } = window.__noxa;
        setDeafened(!state.deafened);
        sysMsg(state.deafened ? "deafened (incoming audio off)" : "undeafened");
        return;
    }
    // (285) quick connect: last-used bookmark/recent in a new tab.
    if (action === "quick_connect") {
        window.__noxaTabs?.quickConnectLast();
        return;
    }
    // (293) compact mode toggle.
    if (action === "compact_toggle") {
        toggleCompact();
        return;
    }
    // (341) zen mode toggle.
    if (action === "zen_toggle") {
        window.__noxaPolish?.toggleZen();
        return;
    }
    // (33) Whisper reply: arm whisper at the last whisperer, re-press restores
    // the whisper list it replaced (arming permanently would silently reroute
    // every later transmission).
    if (action === "whisper_reply") {
        if (whisperReplyRequest && mediaScopeIsCurrent(whisperReplyRequest)) return;
        const scope = captureMediaScope();
        if (state.whisperArmed) {
            const prev = state.whisperPrev || { clients: [], channels: [], active: false };
            whisperReplyRequest = scope;
            const error = await setWhisperRouting(prev, scope);
            if (whisperReplyRequest === scope) whisperReplyRequest = null;
            if (error === null) return;
            if (error) { sysMsg("whisper reply failed: " + error); return; }
            state.whisperArmed = false;
            state.whisperTargetUID = "";
            state.whisperPrev = null;
            renderVoiceStatus();
            sysMsg("whisper reply disarmed");
            return;
        }
        if (!state.lastWhispererUID) {
            toast("no whisper to reply to", "info", "alert");
            return;
        }
        const s = state.settings || {};
        const previous = {
            clients: [...(s.whisper_clients || [])], channels: [...(s.whisper_channels || [])], active: !!s.whisper_active,
        };
        const target = state.lastWhispererUID;
        whisperReplyRequest = scope;
        const error = await setWhisperRouting({ clients: [target], channels: [], active: true }, scope);
        if (whisperReplyRequest === scope) whisperReplyRequest = null;
        if (error === null) return;
        if (error) { sysMsg("whisper reply failed: " + error); return; }
        state.whisperPrev = previous;
        state.whisperTargetUID = target;
        state.whisperArmed = true;
        renderVoiceStatus();
        sysMsg("whisper reply armed → " + uidName(target));
        return;
    }
    if (document.hasFocus() && document.activeElement === $("chat-text")) return;
    if (state.micState === "none" || state.micState === "denied") return;
    if ((state.settings?.activation_mode || "ptt") !== "ptt") return;
    setPTT(action === "ptt_down");
});

// (293) compact mode: mini window with the voice bar and current speaker
// only. Toggled from the View menu or the compact hotkey.
function toggleCompact() {
    const on = !document.body.classList.contains("compact");
    if (on) window.__noxaFiles?.activateWorkspaceTab?.("chat", { focus: false });
    document.body.classList.toggle("compact", on);
    window.__noxaFiles?.restoreVisibleWorkspaceFocus?.();
    if (state.settings) {
        state.settings.compact_mode = on;
        window.go.main.App.SaveSettings(state.settings);
    }
}

// (298) keyboard navigation pass: Esc closes the topmost dialog/menu;
// arrow keys move through the channel tree; Enter joins the focused channel.
document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
        const ctx = document.querySelector(".ctx-menu");
        if (ctx) {
            ctx.remove();
            e.stopPropagation();
            return;
        }
        return;
    }
    // Arrow navigation in the channel tree (when it or a row has focus).
    if (!["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Enter", " "].includes(e.key)) return;
    const tree = $("channel-tree");
    const rows = [...tree.querySelectorAll(".channel, .client")];
    if (rows.length === 0) return;
    const activeEl = document.activeElement;
    const idx = rows.indexOf(activeEl);
    if (isActivationKey(e.key)) {
        if (idx >= 0) {
            e.preventDefault();
            activeEl.click();
        }
        return;
    }
    if (!tree.contains(activeEl) && idx < 0) return;
    if ((e.key === "ArrowLeft" || e.key === "ArrowRight") && idx >= 0) {
        e.preventDefault();
        if (activeEl.classList.contains("channel")) {
            const channelID = Number(activeEl.dataset.chid);
            const expanded = activeEl.getAttribute("aria-expanded");
            if (e.key === "ArrowRight" && expanded === "false") {
                setChannelExpanded(channelID, true);
                tree.querySelector(`.channel[data-chid="${channelID}"]`)?.focus();
            } else if (e.key === "ArrowRight" && expanded === "true") {
                activeEl.closest(".channel-node")?.querySelector(
                    ":scope > .channel-members > .client, :scope > .channel-children > .channel-node > .channel")?.focus();
            } else if (e.key === "ArrowLeft" && expanded === "true") {
                setChannelExpanded(channelID, false);
                tree.querySelector(`.channel[data-chid="${channelID}"]`)?.focus();
            } else if (e.key === "ArrowLeft") {
                activeEl.closest(".channel-children")?.closest(".channel-node")
                    ?.querySelector(":scope > .channel")?.focus();
            }
        } else if (e.key === "ArrowLeft") {
            activeEl.closest(".channel-members")?.closest(".channel-node")
                ?.querySelector(":scope > .channel")?.focus();
        }
        return;
    }
    e.preventDefault();
    const next = e.key === "ArrowDown" ? Math.min(rows.length - 1, idx + 1) : Math.max(0, idx - 1);
    rows[next < 0 ? 0 : next].focus();
});

window.runtime.EventsOn("hotkey_binding", (binding) => {
    if (binding.action !== "ptt") return;
    state.pttShortcutStatus = { spec: binding.spec };
    renderVoiceHints();
});
window.runtime.EventsOn("hotkey_status", (st) => {
    if (st.action === "ptt") {
        state.pttShortcutStatus = { ...state.pttShortcutStatus, ...st };
        renderVoiceHints();
    }
    const el = $("hotkey-status");
    const action = { mute_toggle: "Mute", deafen_toggle: "Deafen", ptt: "Push to talk" }[st.action] || String(st.action || "Voice").replaceAll("_", " ");
    if (st.registered) {
        el.textContent = action + " shortcut active";
        el.classList.remove("err");
        el.title = st.action + " hotkey active";
    } else {
        el.textContent = action + " shortcut unavailable";
        el.classList.add("err");
        el.title = st.action + " hotkey failed: " + (st.error || "");
        toast("hotkey failed: " + st.action + " — " + (st.error || "registration failed"), "warn", "conn");
    }
});

// TALK banner: visible whenever PTT is live or we are speaking.
function updateTalkBanner() {
    const me = state.clients.find((c) => c.client_id === state.myClientID);
    const talking = !state.muted && (state.pttActive || !!(me && me.is_speaking));
    $("talk-banner").textContent = "● " + t("polish.talking");
    $("talk-banner").classList.toggle("hidden", !talking);
}

// Screen share (69-72, 85) + low-bandwidth mode (88) — implemented in video.js.

$("voice-video").onclick = () => cameraToggle();

$("voice-screen").onclick = () => shareToggle();

$("voice-lowbw").onclick = () => setLowBandwidth(!isLowBandwidth(), true);

// Whisper (re-applied from settings after connect) -----------------------------

async function applyWhisperSettings() {
    const s = state.settings;
    if (!s || !s.whisper_active) return;
    if ((s.whisper_clients?.length || 0) === 0 && (s.whisper_channels?.length || 0) === 0) return;
    const error = await setWhisperRouting({ clients: s.whisper_clients || [], channels: s.whisper_channels || [], active: true });
    if (error) sysMsg("whisper configuration failed: " + error);
}

// ---------------------------------------------------------------------------
// Permissions
// ---------------------------------------------------------------------------

async function refreshPermissions() {
    const area = $("perm-area");
    if (state.authorizationModel === "pending") {
        area.replaceChildren();
        return;
    }
    const heading = document.createElement("h3");
    heading.textContent = t("roles.myRoles");
    const help = document.createElement("p");
    help.textContent = t("roles.ownAccessHelp");
    area.replaceChildren(heading, help);
    const ownRoles = state.clients.find(client => client.client_id === state.myClientID)?.roles || [];
    for (const role of [...ownRoles].sort((a, b) => b.position - a.position)) area.appendChild(roleChip(role));
}

// ---------------------------------------------------------------------------
// Shared namespace for menu.js and settings-ui.js
// ---------------------------------------------------------------------------

window.__noxa = {
    soundEngine,
    speechQueue,
    state, $, toast, announceLive, sysMsg, showLogin, showWorkspace, disconnect, sendChat, setPTT, noteActivity,
    setDeafened, refreshPermissions, applyVoiceState, applyOutputSettings, applyLiveAudioSettings,
    startVADMonitor: startVoiceMonitor, stopVADMonitor: stopVoiceMonitor,
    connectFromLogin, renderTree, setChannelExpanded, setDetailsOpen, setDirectTargetVisible,
    clientName, initials, fetchAvatar,
    applyAppearance, toggleCompact, recentChannels, syncOwnChannel,
    playConnectionCue: (event = "connection_connected") => playEvent(event),
    startQualitySampler, stopQualitySampler,
    voiceOutputDevice: () => ({ active: !!remoteChain.ctx && remoteChain.ctx.state !== "closed", id: typeof remoteChain.ctx?.sinkId === "string" ? remoteChain.ctx.sinkId : "" }),
    checkCertificateClock,
    ensureVoiceForChannel, resetVoiceSession, retryMicrophoneAccess, renderVoiceStatus,
    stopPrivateCall,
    // (70) shared system audio controls for the screen tile's context menu.
    shareAudioCtl: {
        get: (clientID) => {
            const n = shareAudio.get(String(clientID));
            return n ? { muted: n.muted, volume: n.volume } : null;
        },
        setMuted: (clientID, muted) => {
            const n = shareAudio.get(String(clientID));
            if (!n) return;
            n.muted = !!muted;
            applyShareAudio(clientID);
        },
        setVolume: (clientID, pct) => {
            const n = shareAudio.get(String(clientID));
            if (!n) return;
            n.volume = Math.max(0, Math.min(200, pct | 0));
            applyShareAudio(clientID);
        },
    },
};

initModalSystem();
initSounds();
initMenu();
initSettingsUI();
initClientInfo();
initVideo();
initUpdater();
initPermsUI(); // roles-v1 access and moderation UI
initFilesUI(); // wave-7 file browser/transfers (registers window.__noxaFiles)
initTabs();    // wave-8a server tabs (registers window.__noxaTabs)
initSocialUI(); // wave-8b presence/contacts/hover (registers window.__noxaSocial)
initMetaUI();   // wave-8b debug/stats/onboarding (registers window.__noxaMeta)
initPolishUI(); // wave-8c polish/a11y (registers window.__noxaPolish)
initNotifications(); // wave-9 notification matrix (registers window.__noxaNotify)
chatUI.initChat(); // wave-5b chat UI (must run after __noxa exists)
initWorkspace();
