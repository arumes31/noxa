// fullscreen-hud.js — design 45 HUD for the app's own fullscreen stream view.
//
// Shown while a watched stream tile (.vtile) or the multiview stage
// (.stream-stage) is the document's fullscreen element:
//   - telemetry pill: resolution · FPS · bitrate · ping (real stats);
//   - speaker list clear of the game UI preset, using the native speaking
//     avatar unchanged, with a ~6 s chat peek underneath;
//   - control dock that slides in on mouse movement and auto-hides;
//   - multiview layout switch (grid / focus / stack) in the dock;
//   - whisper pill and a quick-reply bubble for incoming private messages.
// Presentation only: it never touches voice processing, the gaming overlay
// or the speaking-avatar component; screen-share volume goes through the
// existing shareAudioCtl like the stage's own volume menu.
import "./arctic-fullscreen.css";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { setSafeImage } from "./safe-media.js";
import { speakingAvatar, createSpeakingAvatars } from "./speaking-avatar.js";
import { isClientMicrophoneMuted } from "./audio-state.js";
import { currentWhisperRouting } from "./media-controls.js";
import { previewLine } from "./dm-model.js";
import { SLOT_SCREEN } from "./media-track-id.js";
import { STAGE_MODES } from "./multiview-state.js";
import { avatarColor } from "./workspace-ui.js";
import {
    createAutoHide, createChatPeek, createQuickReply, hudLayout, hudSpeakers, telemetryParts,
} from "./fullscreen-hud-state.js";

const V = () => window.__noxa;
const app = () => window.go?.main?.App;
const PING_POLL_MS = 5000;
const EDGE = 24;
const CHANNEL_RING = 4;

const STAGE_ICONS = {
    grid: '<rect x="3" y="3" width="8" height="8" rx="1.5"/><rect x="13" y="3" width="8" height="8" rx="1.5"/><rect x="3" y="13" width="8" height="8" rx="1.5"/><rect x="13" y="13" width="8" height="8" rx="1.5"/>',
    focus: '<rect x="3" y="3" width="18" height="12" rx="1.5"/><rect x="3" y="17.5" width="5" height="3.5" rx="1"/><rect x="9.5" y="17.5" width="5" height="3.5" rx="1"/><rect x="16" y="17.5" width="5" height="3.5" rx="1"/>',
    stack: '<path d="M12 3l9 5-9 5-9-5z"/><path d="M3 13l9 5 9-5"/>',
    pip: '<rect x="3" y="5" width="18" height="14" rx="2"/><rect x="12" y="12" width="7" height="5" rx="1"/>',
    exit: '<path d="M9 3v6H3M15 3v6h6M9 21v-6H3M15 21v-6h6"/>',
};

function svg(name) {
    if (STAGE_ICONS[name]) return `<svg class="ui-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">${STAGE_ICONS[name]}</svg>`;
    return icon(name);
}

function el(tag, className, html) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (html) node.innerHTML = html;
    return node;
}

function setLabel(node, label) {
    if (node.getAttribute("aria-label") !== label) node.setAttribute("aria-label", label);
    if (node.title !== label) node.title = label;
}

function setIcon(button, name) {
    if (button.dataset.icon === name) return;
    button.dataset.icon = name;
    button.querySelector(".fs-db-icon").innerHTML = svg(name);
}

function setText(node, text) {
    if (node.textContent !== text) node.textContent = text;
}

// chatPreview builds the same safe one-line preview the DM list uses: an
// attachment shows its name and a refused or unopened body its refusal text.
function chatPreview(d) {
    return previewLine({
        text: d.text ?? d.body ?? "", deleted: !!d.deleted, e2e: !!d.e2e, direct: !!d.direct || !!d.e2e,
        enc: !!d.enc || !!d.enc_verified, encVerified: !!d.enc_verified,
        encMode: d.enc_verified && (d.enc_mode === "e2e" || d.enc_mode === "server") ? d.enc_mode : "", self: false,
    });
}

export function createFullscreenHud(stage) {
    let host = null;              // the fullscreen element the HUD lives in
    let hud = null;
    let parts = {};
    let avatars = null;
    let pingTimer = null, tickTimer = null;
    let ping = null;              // { ms, at }
    let layoutKey = "";
    let speakerKey = "";
    let quickKey = "";
    let peekKey = "";
    let chatUnread = 0;
    let sending = false;
    const channelRing = [];

    const autoHide = createAutoHide({ onChange: visible => applyVisibility(visible) });
    const peek = createChatPeek({ onChange: () => renderPeek() });
    const quick = createQuickReply({ onChange: () => renderQuick() });

    // ------------------------------------------------------------ building
    function build() {
        hud = el("div", "fs-hud");
        hud.setAttribute("role", "region");
        hud.dataset.hud = "hidden";
        hud.innerHTML = `
            <div class="fs-top">
                <div class="fs-telemetry fs-glass fs-transient" role="group">
                    <span class="avatar fs-tele-avatar" aria-hidden="true"></span>
                    <b class="fs-tele-name"></b><span class="stage-live fs-live"></span>
                    <span class="fs-sep" aria-hidden="true"></span>
                    <span class="fs-tele-item fs-tele-video">${svg("screen")}<span class="mono"></span></span>
                    <span class="fs-tele-item fs-tele-rate">${svg("signal")}<span class="mono"></span></span>
                    <span class="fs-tele-item fs-tele-ping"><span class="fs-ping-dot" aria-hidden="true"></span><span class="mono"></span></span>
                </div>
                <div class="fs-whisper-out fs-glass" role="status" hidden>
                    <span class="fs-whisper-icon">${svg("whisper")}</span><b class="fs-whisper-label"></b><span class="fs-whisper-targets"></span>
                </div>
            </div>
            <div class="fs-side">
                <ul class="fs-speakers"></ul>
                <p class="fs-more" hidden></p>
                <ul class="fs-peek"></ul>
            </div>
            <div class="fs-reply">
            <section class="fs-quick fs-glass" hidden>
                <header class="fs-quick-head">
                    <span class="fs-quick-avatar"></span>
                    <span class="fs-quick-who"><b class="fs-quick-name"></b><span class="fs-quick-sub"></span></span>
                    <span class="fs-quick-lock" role="img">${svg("lock")}</span>
                    <button type="button" class="fs-quick-close">${svg("close")}</button>
                </header>
                <ol class="fs-quick-log"></ol>
                <form class="fs-quick-form">
                    <input class="fs-quick-input" type="text" autocomplete="off" spellcheck="true" maxlength="2000">
                    <kbd class="fs-kbd" aria-hidden="true">Enter</kbd><kbd class="fs-kbd" aria-hidden="true">Esc</kbd>
                    <button type="submit" class="fs-quick-send">${svg("send")}</button>
                </form>
                <p class="fs-quick-error" role="alert" hidden></p>
            </section>
            <button type="button" class="fs-unread fs-glass" hidden aria-keyshortcuts="Alt+R">
                ${svg("chat")}<b class="fs-unread-count"></b><span class="fs-unread-names"></span><kbd class="fs-kbd" aria-hidden="true">Alt + R</kbd><span class="fs-unread-reply"></span>
            </button>
            </div>
            <div class="fs-dock fs-glass fs-transient" role="toolbar" aria-orientation="horizontal">
                <button type="button" class="fs-db fs-mic"><span class="fs-db-icon"></span><span class="fs-db-label"></span></button>
                <button type="button" class="fs-db fs-deafen"><span class="fs-db-icon"></span><span class="fs-db-label"></span></button>
                <label class="fs-volume">
                    <span class="fs-volume-head">${svg("speaker")}<span class="fs-volume-label"></span><output class="mono fs-volume-value"></output></span>
                    <input type="range" min="0" max="200" step="5" class="fs-volume-input">
                </label>
                <div class="fs-layouts" role="radiogroup"></div>
                <button type="button" class="fs-db fs-pip"><span class="fs-db-icon">${svg("pip")}</span><span class="fs-db-label"></span></button>
                <button type="button" class="fs-db fs-chat" aria-keyshortcuts="C"><span class="fs-db-icon">${svg("chat")}</span><span class="fs-db-label"></span><span class="fs-badge" hidden></span></button>
                <button type="button" class="fs-db fs-exit" aria-keyshortcuts="Escape"><span class="fs-db-icon">${svg("exit")}</span><span class="fs-db-label"></span></button>
            </div>`;
        parts = {
            top: hud.querySelector(".fs-top"),
            telemetry: hud.querySelector(".fs-telemetry"),
            whisper: hud.querySelector(".fs-whisper-out"),
            side: hud.querySelector(".fs-side"),
            speakers: hud.querySelector(".fs-speakers"),
            more: hud.querySelector(".fs-more"),
            peek: hud.querySelector(".fs-peek"),
            reply: hud.querySelector(".fs-reply"),
            quick: hud.querySelector(".fs-quick"),
            input: hud.querySelector(".fs-quick-input"),
            unread: hud.querySelector(".fs-unread"),
            dock: hud.querySelector(".fs-dock"),
            layouts: hud.querySelector(".fs-layouts"),
            volume: hud.querySelector(".fs-volume-input"),
        };
        for (const mode of STAGE_MODES) {
            const button = el("button", "fs-db fs-layout", `<span class="fs-db-icon">${svg(mode)}</span><span class="fs-db-label"></span>`);
            button.type = "button";
            button.dataset.mode = mode;
            button.setAttribute("role", "radio");
            button.onclick = () => { stage.setMode(mode); update(); };
            parts.layouts.append(button);
        }
        parts.layouts.addEventListener("keydown", event => {
            const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[event.key];
            if (!step) return;
            event.preventDefault();
            event.stopPropagation();
            const index = (STAGE_MODES.indexOf(stage.state.mode()) + step + STAGE_MODES.length) % STAGE_MODES.length;
            stage.setMode(STAGE_MODES[index]);
            update();
            parts.layouts.querySelector(`[data-mode="${STAGE_MODES[index]}"]`)?.focus();
        });
        // Toolbar keys: Left / Right move between the dock's controls (the
        // layout radios and the volume slider keep their own arrow keys).
        parts.dock.addEventListener("keydown", event => {
            const step = { ArrowRight: 1, ArrowLeft: -1, Home: -Infinity, End: Infinity }[event.key];
            if (!step || !event.target.matches(".fs-db:not(.fs-layout)")) return;
            const controls = [...parts.dock.querySelectorAll(".fs-db:not(.fs-layout), .fs-layout[tabindex='0'], .fs-volume-input")]
                .filter(node => !node.hidden && !node.disabled && !node.closest("[hidden]"));
            const index = controls.indexOf(event.target);
            const next = step === -Infinity ? 0 : step === Infinity ? controls.length - 1 : Math.min(controls.length - 1, Math.max(0, index + step));
            event.preventDefault();
            controls[next]?.focus();
        });
        hud.querySelector(".fs-mic").onclick = () => { document.getElementById("voice-mute")?.click(); renderDock(); };
        hud.querySelector(".fs-deafen").onclick = () => { document.getElementById("voice-deafen")?.click(); renderDock(); };
        hud.querySelector(".fs-pip").onclick = () => currentTile()?.el.querySelector(".vtile-pip")?.click();
        hud.querySelector(".fs-chat").onclick = () => toggleChatDrawer();
        hud.querySelector(".fs-exit").onclick = () => exit();
        parts.volume.oninput = () => {
            const tile = audioTile();
            const ctl = V()?.shareAudioCtl;
            if (!tile || !ctl?.get(tile.clientID)) return;
            ctl.setVolume(tile.clientID, Number(parts.volume.value));
            renderVolume();
        };
        hud.querySelector(".fs-quick-close").onclick = () => closeQuick(true);
        hud.querySelector(".fs-quick-form").onsubmit = event => { event.preventDefault(); void sendQuick(); };
        parts.input.addEventListener("focus", () => quick.engage(true));
        parts.input.addEventListener("blur", () => quick.engage(!!parts.input.value.trim()));
        parts.unread.onclick = () => replyLatest(true);
        // Hovering or focusing the interactive HUD keeps it on screen.
        for (const holder of [parts.dock, parts.quick, parts.unread]) {
            holder.addEventListener("pointerenter", () => autoHide.hold(holder));
            holder.addEventListener("pointerleave", () => autoHide.release(holder));
        }
        hud.addEventListener("focusin", event => {
            if (event.target.closest(".fs-dock, .fs-quick, .fs-unread")) autoHide.hold("focus");
        });
        hud.addEventListener("focusout", event => {
            if (!event.relatedTarget || !hud.contains(event.relatedTarget) || !event.relatedTarget.closest(".fs-dock, .fs-quick, .fs-unread")) autoHide.release("focus");
        });
        // A click on the HUD must not reach the tile (focus / theatre clicks).
        hud.addEventListener("click", event => event.stopPropagation());
        hud.addEventListener("dblclick", event => event.stopPropagation());
        hud.addEventListener("contextmenu", event => event.stopPropagation());
        return hud;
    }

    // --------------------------------------------------------------- hosts
    function tileFor(element) {
        for (const tile of stage.tiles().values()) if (tile.el === element) return tile;
        return null;
    }

    function hostKind() {
        if (!host) return "none";
        return host === stage.root() ? "multi" : "single";
    }

    function currentTile() {
        if (!host) return null;
        if (hostKind() === "single") return tileFor(host);
        return stage.tiles().get(stage.state.focus) || null;
    }

    function audioTile() {
        if (hostKind() === "single") return currentTile();
        return stage.tiles().get(stage.state.audioSource()) || currentTile();
    }

    function eligible(element) {
        if (!element) return false;
        if (element === stage.root()) return stage.kind() === "multi" || stage.kind() === "single";
        return !!tileFor(element);
    }

    function sync() {
        const element = document.fullscreenElement;
        if (eligible(element)) {
            if (host !== element) attach(element);
            else update();
        } else if (host) detach();
    }

    function attach(element) {
        if (host) detach();
        host = element;
        if (!hud) build();
        host.classList.add("fs-hud-host");
        host.append(hud);
        avatars ||= createSpeakingAvatars(hud);
        host.addEventListener("pointermove", onPointer);
        host.addEventListener("pointerdown", onPointer);
        host.addEventListener("wheel", onPointer, { passive: true });
        layoutKey = speakerKey = quickKey = peekKey = "";
        chatUnread = 0;
        autoHide.reset();
        update();
        autoHide.activity();
        startPolling();
        V()?.announceLive?.(t("fs.entered"));
    }

    function detach() {
        stopPolling();
        if (host) {
            host.removeEventListener("pointermove", onPointer);
            host.removeEventListener("pointerdown", onPointer);
            host.removeEventListener("wheel", onPointer);
            host.classList.remove("fs-hud-host", "fs-hud-idle");
        }
        autoHide.reset();
        peek.clear();
        quick.reset();
        hud?.remove();
        host = null;
        avatars?.refresh();
    }

    function exit() {
        if (document.fullscreenElement) void document.exitFullscreen().catch(() => {});
    }

    // ---------------------------------------------------------- activity
    function onPointer() {
        autoHide.activity();
    }

    function applyVisibility(visible) {
        if (!hud) return;
        hud.dataset.hud = visible ? "shown" : "hidden";
        host?.classList.toggle("fs-hud-idle", !visible);
        renderSpeakers();
    }

    function typing(target) {
        return !!target && (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName || ""));
    }

    // Capture phase on window: runs before video-grid's document-level Esc
    // handler, so Esc in the reply field closes the bubble, not fullscreen.
    function onKey(event) {
        if (!host || !host.isConnected) return;
        if (event.key === "Escape") {
            if (!parts.quick.hidden && parts.quick.contains(event.target)) {
                event.preventDefault();
                event.stopImmediatePropagation();
                closeQuick(true);
                return;
            }
            if (document.querySelector(".ctx-menu")) return;
            event.preventDefault();
            event.stopImmediatePropagation();
            exit();
            return;
        }
        if (event.key === "Tab") { autoHide.activity(); return; }
        if (event.altKey && !event.ctrlKey && !event.metaKey && (event.code === "KeyR" || event.key?.toLowerCase() === "r")) {
            event.preventDefault();
            replyLatest(true);
            return;
        }
        if (typing(event.target) || event.ctrlKey || event.altKey || event.metaKey || event.repeat) return;
        if (event.code === "KeyC" || event.key === "c" || event.key === "C") {
            event.preventDefault();
            toggleChatDrawer();
            return;
        }
        autoHide.activity();
    }

    // ------------------------------------------------------------ polling
    function startPolling() {
        stopPolling();
        const samplePing = async () => {
            const state = V()?.state;
            const fn = app()?.GetClientInfoForTab;
            if (!host || !state?.myClientID || typeof fn !== "function") return;
            const tab = state.activeTabID, clientID = state.myClientID;
            try {
                const info = await fn(tab, clientID);
                if (!host || V()?.state?.activeTabID !== tab) return;
                if (Number.isFinite(info?.ping_ms) && info.ping_ms >= 0) ping = { ms: info.ping_ms, at: Date.now() };
                renderTelemetry();
            } catch { /* keep the last sample; it ages out below */ }
        };
        void samplePing();
        pingTimer = setInterval(samplePing, PING_POLL_MS);
        // Whisper routing and the self speaking state have no event here.
        tickTimer = setInterval(() => { renderWhisper(); renderTelemetry(); renderDock(); }, 1000);
    }

    function stopPolling() {
        clearInterval(pingTimer);
        clearInterval(tickTimer);
        pingTimer = tickTimer = null;
    }

    // ------------------------------------------------------------- render
    function update() {
        if (!host || !hud) return;
        hud.dataset.kind = hostKind();
        hud.setAttribute("aria-label", t("fs.region"));
        renderTelemetry();
        renderWhisper();
        renderSpeakers();
        renderPeek();
        renderDock();
        renderQuick();
        renderLayout();
    }

    function client(clientID) {
        return V()?.state?.clients?.find(c => String(c.client_id) === String(clientID)) || null;
    }

    function paintAvatar(element, c, fallbackName = "") {
        const v = V();
        const name = c ? (c.nickname || c.unique_id || "?") : fallbackName || "?";
        const uid = c?.unique_id || "";
        if (element.dataset.uid === uid && element.dataset.name === name && element.childElementCount + element.textContent.length) return;
        element.dataset.uid = uid;
        element.dataset.name = name;
        element.style.setProperty("--avatar-color", avatarColor(uid || name));
        const url = uid && v?.state?.avatars?.get(uid);
        if (url) setSafeImage(element, url, { avatar: true });
        else {
            element.textContent = v?.initials ? v.initials(name) : name.slice(0, 1).toUpperCase();
            if (uid) v?.fetchAvatar?.(uid);
        }
    }

    function renderTelemetry() {
        if (!hud) return;
        const tile = currentTile();
        const box = parts.telemetry;
        box.hidden = !tile;
        if (!tile) return;
        box.setAttribute("aria-label", t("fs.telemetry"));
        paintAvatar(box.querySelector(".fs-tele-avatar"), client(tile.clientID), stage.displayName(tile.clientID));
        setText(box.querySelector(".fs-tele-name"), stage.displayName(tile.clientID));
        setText(box.querySelector(".fs-live"), t("stage.live"));
        const fresh = ping && Date.now() - ping.at < 15000 ? ping.ms : NaN;
        const values = telemetryParts(tile.diagnostics, fresh, {
            video: height => t("fs.video", { height }),
            videoFps: (height, fps) => t("fs.videoFps", { height, fps }),
            rate: bits => bits >= 1000000 ? t("stage.rateMbps", { value: (bits / 1000000).toFixed(1) })
                : t("stage.rateKbps", { value: Math.max(1, Math.round(bits / 1000)) }),
            ping: ms => t("fs.ping", { ping: ms }),
        });
        const video = box.querySelector(".fs-tele-video");
        setText(video.querySelector("span"), values.video || t("fs.measuring"));
        const rate = box.querySelector(".fs-tele-rate");
        rate.hidden = !values.bitrate;
        setText(rate.querySelector("span"), values.bitrate || "");
        const pingItem = box.querySelector(".fs-tele-ping");
        pingItem.hidden = !values.ping;
        setText(pingItem.querySelector(".mono"), values.ping || "");
        pingItem.dataset.quality = values.quality || "";
        if (values.ping) setLabel(pingItem, t("fs.pingLabel", { ping: Math.round(fresh) }));
    }

    function whisperTargets(config) {
        const state = V()?.state || {};
        const names = [
            ...(config.clients || []).map(uid => state.clients?.find(c => c.unique_id === uid)?.nickname || uid),
            ...(config.channels || []).map(id => t("fs.whisperChannel", { name: state.channels?.find(c => c.ChannelID === id)?.Name || String(id) })),
        ];
        return names.length ? names.join(", ") : t("fs.whisperNone");
    }

    function renderWhisper() {
        if (!hud) return;
        const routing = currentWhisperRouting();
        const config = routing?.status === "confirmed" ? routing.config : null;
        const box = parts.whisper;
        box.hidden = !config?.active;
        if (box.hidden) return;
        const state = V()?.state || {};
        const me = state.clients?.find(c => c.client_id === state.myClientID);
        const talking = !state.muted && (state.pttActive || !!me?.is_speaking);
        box.dataset.talking = talking ? "1" : "";
        const targets = whisperTargets(config);
        setText(box.querySelector(".fs-whisper-label"), t(talking ? "fs.whisperingLabel" : "fs.whisperArmedLabel"));
        setText(box.querySelector(".fs-whisper-targets"), targets);
        setLabel(box, t(talking ? "fs.whisperingTo" : "fs.whisperArmed", { targets }));
    }

    function voiceMembers() {
        const state = V()?.state;
        if (!state?.clients) return [];
        return state.clients.filter(c => (state.myChannelID != null && c.channel_id === state.myChannelID) || (c.is_whispering && c.is_speaking))
            .map(c => ({
                id: c.client_id, client: c, name: c.nickname || c.unique_id || String(c.client_id),
                speaking: !!c.is_speaking && !c.is_whispering, whispering: !!(c.is_whispering && c.is_speaking),
                muted: isClientMicrophoneMuted(c, state),
            }));
    }

    function renderSpeakers() {
        if (!hud) return;
        const state = V()?.state;
        const active = autoHide.visible;
        const { shown, more } = hudSpeakers(voiceMembers(), { self: state?.myClientID, active, limit: active ? 6 : 4 });
        const channel = state?.channels?.find(c => c.ChannelID === state.myChannelID);
        parts.speakers.setAttribute("aria-label", channel ? t("fs.speakers", { channel: channel.Name }) : t("fs.speakersNoChannel"));
        const key = JSON.stringify([active, more, shown.map(m => [m.id, m.name, m.speaking, m.whispering, m.muted, m.client.unique_id, state?.avatars?.get(m.client.unique_id) ? 1 : 0]), t("fs.whisperTag")]);
        if (key === speakerKey) return;
        speakerKey = key;
        parts.speakers.replaceChildren(...shown.map(member => {
            const talking = member.speaking || member.whispering;
            // The channel tree's row: .client(.speaking) with the native
            // speaking avatar around the same .avatar element.
            const row = el("li", `client fs-speaker${talking ? " speaking" : ""}${member.whispering ? " whispering" : ""}`);
            const avatar = el("span", "avatar");
            paintAvatar(avatar, member.client);
            const holder = el("span", "fs-speaker-av");
            holder.append(talking ? speakingAvatar(avatar) : avatar);
            const name = el("span", "client-name fs-speaker-name");
            name.textContent = member.name;
            row.append(holder, name);
            if (member.muted) {
                const mic = el("span", "fs-speaker-muted", svg("micOff"));
                mic.setAttribute("role", "img");
                mic.setAttribute("aria-label", t("fs.micOff"));
                row.append(mic);
            }
            if (member.whispering) {
                const tag = el("span", "fs-whisper-tag");
                tag.textContent = t("fs.whisperTag");
                row.append(tag);
            }
            if (talking) {
                const status = el("span", "fs-sr");
                status.textContent = t("fs.speaking");
                row.append(status);
            }
            return row;
        }));
        parts.more.hidden = !more;
        parts.more.textContent = more ? t("fs.moreSpeakers", { count: more }) : "";
        avatars?.refresh();
        renderLayout();
    }

    function renderPeek() {
        if (!hud) return;
        const items = peek.items;
        const key = JSON.stringify(items.map(item => item.id));
        if (key === peekKey) return;
        peekKey = key;
        parts.peek.setAttribute("aria-label", t("fs.chatPeek"));
        parts.peek.replaceChildren(...items.map(item => {
            const li = el("li", "fs-peek-item fs-glass");
            const avatar = el("span", "avatar fs-peek-avatar");
            avatar.setAttribute("aria-hidden", "true");
            paintAvatar(avatar, item.client, item.name);
            const body = el("span", "fs-peek-body");
            const name = el("b", "fs-peek-name");
            name.textContent = item.name;
            const text = el("span", "fs-peek-text");
            text.textContent = item.text;
            body.append(name, text);
            li.append(avatar, body);
            return li;
        }));
    }

    function renderDock() {
        if (!hud) return;
        const state = V()?.state || {};
        const kind = hostKind();
        parts.dock.setAttribute("aria-label", t("fs.dock"));
        const mic = hud.querySelector(".fs-mic");
        setIcon(mic, state.muted ? "micOff" : "mic");
        setText(mic.querySelector(".fs-db-label"), t("fs.mic"));
        setLabel(mic, t(state.muted ? "fs.micUnmute" : "fs.micMute"));
        mic.setAttribute("aria-pressed", String(!!state.muted));
        mic.classList.toggle("off", !!state.muted);
        mic.hidden = !document.getElementById("voice-mute");
        const deafen = hud.querySelector(".fs-deafen");
        setIcon(deafen, state.deafened ? "headphonesOff" : "headphones");
        setText(deafen.querySelector(".fs-db-label"), t("fs.deafen"));
        setLabel(deafen, t(state.deafened ? "fs.undeafen" : "fs.deafen"));
        deafen.setAttribute("aria-pressed", String(!!state.deafened));
        deafen.classList.toggle("off", !!state.deafened);
        deafen.hidden = !document.getElementById("voice-deafen");
        renderVolume();
        parts.layouts.hidden = kind !== "multi";
        parts.layouts.setAttribute("aria-label", t("fs.layout"));
        const mode = stage.state.mode();
        for (const button of parts.layouts.querySelectorAll(".fs-layout")) {
            const on = button.dataset.mode === mode;
            setText(button.querySelector(".fs-db-label"), t(`stage.mode.${button.dataset.mode}`));
            button.setAttribute("aria-checked", String(on));
            button.classList.toggle("on", on);
            button.tabIndex = on ? 0 : -1;
        }
        const pip = hud.querySelector(".fs-pip");
        const tilePip = currentTile()?.el.querySelector(".vtile-pip");
        pip.hidden = kind !== "single" || !tilePip;
        pip.disabled = !!tilePip?.disabled;
        setText(pip.querySelector(".fs-db-label"), t("fs.pip"));
        setLabel(pip, t("fs.pipHelp"));
        const chat = hud.querySelector(".fs-chat");
        setText(chat.querySelector(".fs-db-label"), t("fs.chat"));
        setLabel(chat, chatUnread ? t("fs.chatUnread", { count: chatUnread }) : t("fs.chatHelp"));
        const badge = chat.querySelector(".fs-badge");
        badge.hidden = !chatUnread;
        setText(badge, chatUnread > 99 ? "99+" : String(chatUnread));
        chat.setAttribute("aria-expanded", String(quick.state.target?.kind === "channel"));
        const exitButton = hud.querySelector(".fs-exit");
        setText(exitButton.querySelector(".fs-db-label"), t("fs.exit"));
        setLabel(exitButton, t("fs.exitHelp"));
    }

    function renderVolume() {
        const tile = audioTile();
        const audio = tile?.slot === SLOT_SCREEN ? V()?.shareAudioCtl?.get(tile.clientID) : null;
        const wrap = hud.querySelector(".fs-volume");
        wrap.classList.toggle("disabled", !audio);
        parts.volume.disabled = !audio;
        const value = audio ? (audio.muted ? 0 : audio.volume) : 0;
        if (document.activeElement !== parts.volume) parts.volume.value = String(value);
        const label = audio ? t("fs.streamVolume") : t("fs.streamVolumeNone");
        setText(wrap.querySelector(".fs-volume-label"), t("fs.streamVolume"));
        parts.volume.setAttribute("aria-label", label);
        wrap.title = label;
        const shown = `${parts.volume.value}%`;
        setText(wrap.querySelector(".fs-volume-value"), audio ? shown : "—");
        parts.volume.setAttribute("aria-valuetext", audio ? shown : label);
        wrap.style.setProperty("--fs-volume", `${Number(parts.volume.value) / 2}%`);
    }

    function renderQuick() {
        if (!hud) return;
        const snap = quick.state;
        const box = parts.quick;
        const key = JSON.stringify([snap, t("fs.privateMessage")]);
        renderUnread(snap);
        renderDock();
        if (key === quickKey) return;
        quickKey = key;
        box.hidden = !snap.open;
        if (!snap.open) { renderLayout(); return; }
        const target = snap.target;
        const dm = target.kind === "dm";
        box.dataset.kind = target.kind;
        box.setAttribute("role", "region");
        box.setAttribute("aria-label", dm ? t("fs.quickRegion", { name: target.name }) : t("fs.quickChannelRegion", { name: target.name }));
        const c = dm ? V()?.state?.clients?.find(x => x.unique_id === target.uid) : null;
        const avatarHolder = box.querySelector(".fs-quick-avatar");
        const avatar = el("span", "avatar");
        avatar.setAttribute("aria-hidden", "true");
        paintAvatar(avatar, c, dm ? target.name : `#${target.name}`);
        const speaking = !!(c?.is_speaking);
        avatarHolder.className = `fs-quick-avatar${speaking ? " client speaking" : ""}`;
        avatarHolder.replaceChildren(speaking ? speakingAvatar(avatar) : avatar);
        setText(box.querySelector(".fs-quick-name"), dm ? target.name : `#${target.name}`);
        setText(box.querySelector(".fs-quick-sub"), dm ? t("fs.privateMessage") : t("fs.channelChat"));
        const lock = box.querySelector(".fs-quick-lock");
        lock.hidden = !dm;
        lock.setAttribute("aria-label", t("fs.encrypted"));
        lock.title = t("fs.encrypted");
        setLabel(box.querySelector(".fs-quick-close"), t("fs.close"));
        box.querySelector(".fs-quick-log").replaceChildren(...snap.messages.map(message => {
            const li = el("li", `fs-bubble${message.self ? " self" : ""}`);
            if (!dm && !message.self && message.name) {
                const who = el("b", "fs-bubble-name");
                who.textContent = message.name;
                li.append(who);
            }
            const text = el("span", "fs-bubble-text");
            text.textContent = message.text;
            li.append(text);
            if (message.self) {
                const sr = el("span", "fs-sr");
                sr.textContent = t("fs.you");
                li.prepend(sr);
            }
            return li;
        }));
        const placeholder = dm ? t("fs.replyTo", { name: target.name }) : t("fs.messageChannel", { name: target.name });
        parts.input.placeholder = placeholder;
        parts.input.setAttribute("aria-label", dm ? t("fs.replyLabel", { name: target.name }) : placeholder);
        setLabel(box.querySelector(".fs-quick-send"), t("fs.send"));
        avatars?.refresh();
        renderLayout();
    }

    function renderUnread(snap) {
        const waiting = snap.unread;
        const button = parts.unread;
        button.hidden = !waiting.length;
        if (!waiting.length) return;
        const count = waiting.reduce((sum, item) => sum + item.count, 0);
        setText(button.querySelector(".fs-unread-count"), t("fs.unread", { count }));
        setText(button.querySelector(".fs-unread-names"), waiting.map(item => item.name).join(", "));
        setText(button.querySelector(".fs-unread-reply"), t("fs.reply"));
        setLabel(button, `${t("fs.unreadNames", { count, names: waiting.map(item => item.name).join(", ") })} · ${t("fs.replyLatest")}`);
    }

    // renderLayout positions the persistent parts from hudLayout(). Only
    // writes styles when the inputs change (count, size, settings, kind).
    function renderLayout() {
        if (!host || !hud) return;
        const settings = V()?.state?.settings || {};
        const w = host.clientWidth || window.innerWidth, h = host.clientHeight || window.innerHeight;
        const self = V()?.state?.myClientID;
        const rows = Math.max(1, Math.min(6, voiceMembers().filter(member => member.id !== self).length));
        const pillWidth = Math.ceil(Math.max(parts.telemetry.offsetWidth, parts.whisper.offsetWidth, 280) / 20) * 20;
        const key = JSON.stringify([w, h, rows, pillWidth, settings.gaming_overlay_game_ui, settings.gaming_overlay_avoid_zones,
            settings.gaming_overlay_position, settings.gaming_overlay_x, settings.gaming_overlay_y]);
        if (key === layoutKey) return;
        layoutKey = key;
        const pct = (px, total) => Math.min(100, px / Math.max(1, total) * 100);
        const layout = hudLayout({
            settings,
            speakers: { w: pct(260 + EDGE, w), h: pct(rows * 64 + EDGE, h) },
            bubble: { w: pct(380 + EDGE, w), h: pct(300 + EDGE, h) },
            pill: { w: pct(pillWidth + 16, w), h: pct(70, h) },
        });
        hud.dataset.anchor = layout.anchor;
        hud.dataset.side = layout.side;
        hud.dataset.bubble = layout.bubble.anchor;
        hud.dataset.moved = layout.moved ? "1" : "";
        hud.style.setProperty("--fs-top-inset", `${layout.topInset}%`);
        parts.side.classList.toggle("peek-above", layout.peekAbove);
        place(parts.side, layout.speakers);
        place(parts.reply, layout.bubble, true);
    }

    // place maps a percentage rectangle onto edge offsets: rectangles that
    // touch an edge hug it with the HUD margin, the others are centred on
    // their rectangle's centre.
    function place(node, rect, bubble = false) {
        const style = node.style;
        style.left = style.right = style.top = style.bottom = "";
        let tx = "0", ty = "0";
        if (rect.x <= 0.5) style.left = `${EDGE}px`;
        else if (rect.x + rect.w >= 99.5) style.right = `${EDGE}px`;
        else { style.left = `${rect.x + rect.w / 2}%`; tx = "-50%"; }
        if (rect.y <= 0.5) style.top = `calc(${EDGE}px + var(--fs-top-inset, 0%) * ${bubble ? 1 : 0})`;
        else if (rect.y + rect.h >= 99.5) style.bottom = `${bubble ? EDGE * 4 : EDGE}px`;
        else { style.top = `${rect.y + rect.h / 2}%`; ty = "-50%"; }
        style.setProperty("--fs-tx", tx);
        style.setProperty("--fs-ty", ty);
    }

    // -------------------------------------------------------- quick reply
    function closeQuick(returnFocus) {
        const had = parts.quick && parts.quick.contains(document.activeElement);
        parts.input.value = "";
        quick.close();
        if (returnFocus && had) host?.focus?.({ preventScroll: true });
        if (had) autoHide.release("focus");
    }

    function replyLatest(focusInput) {
        if (!host) return;
        const target = quick.openLatest();
        if (target && focusInput) {
            parts.input.focus({ preventScroll: true });
        }
    }

    function toggleChatDrawer() {
        if (!host) return;
        if (quick.state.target?.kind === "channel") { closeQuick(true); return; }
        const state = V()?.state || {};
        const channel = state.channels?.find(c => c.ChannelID === state.myChannelID);
        if (!channel) return;
        quick.openChannel({ id: channel.ChannelID, name: channel.Name, recent: channelRing.filter(m => m.channel === channel.ChannelID) });
        chatUnread = 0;
        renderDock();
        parts.input.focus({ preventScroll: true });
    }

    async function sendQuick() {
        const snap = quick.state;
        const text = parts.input.value.trim();
        const error = hud.querySelector(".fs-quick-error");
        if (!snap.open || !text || sending) return;
        const state = V()?.state || {};
        const send = app()?.SendChatForTab;
        if (typeof send !== "function") return;
        sending = true;
        error.hidden = true;
        try {
            const result = snap.target.kind === "dm"
                ? await send(state.activeTabID, "direct", snap.target.uid, text)
                : await send(state.activeTabID, "channel", String(snap.target.id), text);
            if (result) throw new Error(String(result));
            if (snap.target.kind === "dm") quick.sent(text);
            parts.input.value = "";
        } catch (failure) {
            error.textContent = t("fs.sendFailed", { error: failure?.message || String(failure) });
            error.hidden = false;
        } finally {
            sending = false;
        }
    }

    // ------------------------------------------------------- chat events
    // chatMessage receives every chat event after chat-ui handled it (the
    // result says whether it was new). Channel messages from your voice
    // channel peek under the speakers; private messages open the bubble.
    function chatMessage(d, result) {
        if (!d || !result) return;
        const state = V()?.state || {};
        if (state.replayingTabID) return;
        const direct = !!d.direct || !!d.e2e;
        const self = (d.from_client_id && d.from_client_id === state.myClientID) || (d.from_unique_id && d.from_unique_id === state.myUniqueID);
        const sender = state.clients?.find(c => (d.from_client_id && c.client_id === d.from_client_id) || (d.from_unique_id && c.unique_id === d.from_unique_id)) || null;
        const name = d.from || d.from_nickname || sender?.nickname || "?";
        if (!direct) {
            const channel = d.channel_id !== undefined && d.channel_id !== null && d.channel_id !== "" ? Number(d.channel_id) : null;
            if (channel === null || channel !== state.myChannelID) return;
            const key = d.id ? `ch:${d.id}` : "";
            if (key && channelRing.some(item => item.key === key)) return;
            const text = chatPreview(d);
            channelRing.push({ key, channel, name, text, self: !!self, at: Date.now() });
            while (channelRing.length > CHANNEL_RING) channelRing.shift();
            if (!host || self) return;
            if (window.__noxaNotify?.channelOverride?.(channel)?.muted) return;
            if (quick.state.target?.kind === "channel") {
                quick.channelMessage({ name, text, self: false });
                return;
            }
            chatUnread++;
            peek.push({ key, name, text, client: sender });
            renderDock();
            return;
        }
        if (!host || !result.incomingDM || self) return;
        const uid = d.from_unique_id || sender?.unique_id || "";
        if (!uid) return;
        quick.receive({ uid, name, text: chatPreview(d) });
    }

    function voiceChanged() {
        if (!host) return;
        renderSpeakers();
        renderWhisper();
        if (!parts.quick.hidden) { quickKey = ""; renderQuick(); }
    }

    window.addEventListener("keydown", onKey, true);
    document.addEventListener("fullscreenchange", sync);
    window.addEventListener("resize", () => { if (host) renderLayout(); });
    window.addEventListener("noxa-language-changed", () => { speakerKey = quickKey = peekKey = ""; update(); });

    return {
        update() { if (host) update(); },
        sync,
        chatMessage,
        voiceChanged,
        get active() { return !!host; },
        get visible() { return autoHide.visible; },
    };
}
