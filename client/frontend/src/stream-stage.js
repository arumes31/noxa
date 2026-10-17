// stream-stage.js — design 45 stream stage and multiview.
//
// Presentation only. video-grid.js still owns every tile, receiver binding,
// stats poll, hardware recovery and quality request; stream-controls.js owns
// watch/unwatch. This module arranges the visible watched tiles:
//   1 tile   → framed single stage with an info strip and a resizable chat
//              split (or theater: chat in the side panel);
//   2+ tiles → multiview (grid / focus / stack) with a Chat · Viewers panel,
//              1-5 focus hotkeys and focus-following screen-audio ducking.
// The gaming overlay and native :fullscreen tiles are not touched.
import "./arctic-stage.css";
import { t } from "./i18n.js";
import { setSafeImage } from "./safe-media.js";
import { mountContextMenu, closeContextMenu } from "./context-menu.js";
import { availableStreams } from "./stream-controls.js";
import { SLOT_SCREEN } from "./media-track-id.js";
import {
    AUDIO_FOLLOW, DUCK_LEVEL, HOTKEY_TILES, STAGE_MODES, SPLIT_MAX, SPLIT_MIN,
    createStageState, gridShape, hotkeyIndex, publisherOf, stageKind,
} from "./multiview-state.js";

const V = () => window.__noxa;

const ICONS = {
    multiview: '<rect x="3" y="3" width="8" height="8" rx="1.5"/><rect x="13" y="3" width="8" height="8" rx="1.5"/><rect x="3" y="13" width="8" height="8" rx="1.5"/><rect x="13" y="13" width="8" height="8" rx="1.5"/>',
    grid: '<rect x="3" y="3" width="8" height="8" rx="1.5"/><rect x="13" y="3" width="8" height="8" rx="1.5"/><rect x="3" y="13" width="8" height="8" rx="1.5"/><rect x="13" y="13" width="8" height="8" rx="1.5"/>',
    focus: '<rect x="3" y="3" width="18" height="12" rx="1.5"/><rect x="3" y="17.5" width="5" height="3.5" rx="1"/><rect x="9.5" y="17.5" width="5" height="3.5" rx="1"/><rect x="16" y="17.5" width="5" height="3.5" rx="1"/>',
    stack: '<path d="M12 3l9 5-9 5-9-5z"/><path d="M3 13l9 5 9-5"/>',
    speaker: '<path d="M4 9h4l5-4v14l-5-4H4z"/><path d="M16.5 9a4 4 0 0 1 0 6M19 6.5a8 8 0 0 1 0 11"/>',
    speakerOff: '<path d="M4 9h4l5-4v14l-5-4H4z"/><path d="M16 9l6 6M22 9l-6 6"/>',
    chevron: '<path d="M6 9l6 6 6-6"/>',
    plus: '<path d="M12 5v14M5 12h14"/>',
    fullscreen: '<path d="M3 9V3h6M21 9V3h-6M3 15v6h6M21 15v6h-6"/>',
    exitFullscreen: '<path d="M9 3v6H3M15 3v6h6M9 21v-6H3M15 21v-6h6"/>',
    pip: '<rect x="3" y="5" width="18" height="14" rx="2"/><rect x="12" y="12" width="7" height="5" rx="1"/>',
    theater: '<rect x="2" y="4" width="20" height="13" rx="2"/><path d="M7 21h10"/>',
    pin: '<path d="M9 3h6l-1 6 4 4H6l4-4zM12 13v8"/>',
    swap: '<path d="M7 7h13l-3-3M17 17H4l3 3"/>',
    chat: '<path d="M4 5h16v11H9l-5 4z"/>',
    users: '<circle cx="9" cy="8" r="4"/><path d="M2 21a7 7 0 0 1 14 0M16 4.2a4 4 0 0 1 0 7.6M22 21a7 7 0 0 0-4.5-6.5"/>',
    collapse: '<path d="M9 6l6 6-6 6"/>',
    expand: '<path d="M15 6l-6 6 6 6"/>',
};

function svg(name) {
    return `<svg class="stage-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">${ICONS[name] || ""}</svg>`;
}

function iconButton(className, name) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = className;
    button.innerHTML = svg(name);
    return button;
}

function setLabel(element, label) {
    element.title = label;
    element.setAttribute("aria-label", label);
}

// formatStageRate renders receive bitrate as the info strip's "6.2 Mbps".
export function formatStageRate(bits) {
    if (!Number.isFinite(bits) || bits <= 0) return "";
    return bits >= 1000000
        ? t("stage.rateMbps", { value: (bits / 1000000).toFixed(1) })
        : t("stage.rateKbps", { value: Math.max(1, Math.round(bits / 1000)) });
}

// stageStats is "1080p · 60 FPS · 6.2 Mbps" from the tile's latest receiver
// sample (video-grid's stats poll); parts that were not measured are omitted.
export function stageStats(sample) {
    const parts = [];
    if (sample?.height > 0) parts.push(t("stage.resolution", { height: sample.height }));
    if (Number.isFinite(sample?.fps) && sample.fps > 0) parts.push(t("stage.fps", { fps: Math.round(sample.fps) }));
    const rate = formatStageRate(sample?.bitrate);
    if (rate) parts.push(rate);
    return parts.join(" · ");
}

// stageResolution is the multiview tile tag's compact "1080p60".
export function stageResolution(sample) {
    if (!(sample?.height > 0)) return "";
    const fps = Number.isFinite(sample?.fps) && sample.fps > 0 ? Math.round(sample.fps) : "";
    return `${sample.height}p${fps}`;
}

export function createStreamStage({ gridEl, tiles, onFocusChange }) {
    const state = createStageState({
        session: globalThis.sessionStorage, local: globalThis.localStorage,
        scope: () => V()?.state?.activeTabID || "",
    });
    let root = null, head = null, body = null, foot = null, info = null;
    let panelHead = null, viewers = null, rail = null, splitter = null, chatHead = null;
    let kind = "none";
    let qualityFocus = null;
    let lastGains = "";

    // ------------------------------------------------------------- building
    function ensureRoot() {
        const grid = gridEl();
        if (!grid) return null;
        if (root && root.contains(grid)) return root;
        root = document.createElement("section");
        root.className = "stream-stage";
        root.id = "stream-stage";
        root.dataset.stage = "none";
        head = document.createElement("header");
        head.className = "stage-head";
        head.innerHTML = `<span class="stage-title">${svg("multiview")}<h2 class="stage-heading"></h2><span class="stage-count"></span></span>
            <div class="stage-modes" role="radiogroup"></div>
            <button type="button" class="stage-btn stage-audio" aria-haspopup="menu">${svg("speaker")}<span class="stage-audio-label"></span>${svg("chevron")}</button>
            <button type="button" class="stage-btn stage-add" aria-haspopup="menu">${svg("plus")}<span class="stage-add-label"></span></button>
            <button type="button" class="stage-btn stage-primary stage-fullscreen">${svg("fullscreen")}<span class="stage-fullscreen-label"></span></button>`;
        const modes = head.querySelector(".stage-modes");
        for (const mode of STAGE_MODES) {
            const button = document.createElement("button");
            button.type = "button";
            button.className = "stage-mode";
            button.dataset.mode = mode;
            button.setAttribute("role", "radio");
            button.innerHTML = `${svg(mode)}<span></span>`;
            button.onclick = () => setMode(mode);
            modes.append(button);
        }
        modes.addEventListener("keydown", event => {
            const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[event.key];
            if (!step) return;
            event.preventDefault();
            const index = (STAGE_MODES.indexOf(state.mode()) + step + STAGE_MODES.length) % STAGE_MODES.length;
            setMode(STAGE_MODES[index]);
            modes.querySelector(`[data-mode="${STAGE_MODES[index]}"]`).focus();
        });
        head.querySelector(".stage-audio").onclick = event => openAudioMenu(event.currentTarget);
        head.querySelector(".stage-add").onclick = event => openAddMenu(event.currentTarget);
        head.querySelector(".stage-fullscreen").onclick = () => toggleStageFullscreen();
        body = document.createElement("div");
        body.className = "stage-body";
        foot = document.createElement("footer");
        foot.className = "stage-foot";
        foot.innerHTML = `<span class="stage-foot-audio">${svg("speaker")}<span></span></span><span class="stage-foot-keys"><kbd></kbd><span></span></span>`;
        grid.before(root);
        body.append(grid);
        root.append(head, body, foot);
        info = buildInfo();
        document.addEventListener("keydown", onHotkey);
        document.addEventListener("fullscreenchange", () => render());
        window.addEventListener("noxa-language-changed", () => render(true));
        window.addEventListener("noxa-stream-catalog", () => renderAdd());
        return root;
    }

    function buildInfo() {
        const strip = document.createElement("div");
        strip.className = "stage-info";
        strip.innerHTML = `<span class="avatar stage-info-avatar" aria-hidden="true"></span>
            <span class="stage-info-name"></span><span class="stage-live"></span><span class="stage-info-stats"></span>
            <span class="stage-info-actions">
                <button type="button" class="stage-btn stage-add stage-info-add" aria-haspopup="menu">${svg("plus")}<span class="stage-add-label"></span></button>
                <button type="button" class="stage-tool stage-info-volume" aria-haspopup="menu">${svg("speaker")}</button>
                <button type="button" class="stage-tool stage-info-pip">${svg("pip")}</button>
                <button type="button" class="stage-tool stage-info-theater">${svg("theater")}</button>
                <button type="button" class="stage-tool stage-info-fullscreen">${svg("fullscreen")}</button>
            </span>`;
        strip.onclick = event => event.stopPropagation();
        strip.oncontextmenu = event => event.stopPropagation();
        strip.querySelector(".stage-info-add").onclick = event => openAddMenu(event.currentTarget);
        strip.querySelector(".stage-info-volume").onclick = event => {
            const tile = singleTile();
            if (tile) openVolumeMenu(event.currentTarget, tile);
        };
        // PiP and fullscreen reuse the tile's own controls and their paths.
        strip.querySelector(".stage-info-pip").onclick = () => singleTile()?.el.querySelector(".vtile-pip")?.click();
        strip.querySelector(".stage-info-fullscreen").onclick = () => singleTile()?.el.querySelector(".vtile-fullscreen")?.click();
        strip.querySelector(".stage-info-theater").onclick = () => { state.setTheater(!state.theater()); render(true); };
        return strip;
    }

    function ensureTileChrome(tile) {
        if (tile.stageTools) return;
        const tools = document.createElement("div");
        tools.className = "stage-tile-tools";
        const pin = iconButton("stage-tool stage-pin", "pin");
        const volume = iconButton("stage-tool stage-volume", "speaker");
        const swap = iconButton("stage-tool stage-swap", "swap");
        pin.onclick = event => { event.stopPropagation(); state.togglePin(tile.key); render(true); };
        volume.onclick = event => { event.stopPropagation(); toggleTileMute(tile); };
        swap.onclick = event => { event.stopPropagation(); state.swap(tile.key); render(true); announceFocus(); };
        tools.append(pin, volume, swap);
        tools.oncontextmenu = event => event.stopPropagation();
        const tag = document.createElement("span");
        tag.className = "stage-tag";
        tag.innerHTML = `<span class="avatar stage-tag-avatar" aria-hidden="true"></span><span class="stage-tag-name"></span><span class="stage-live"></span><span class="stage-tag-res"></span><span class="stage-tag-audio">${svg("speaker")}</span>`;
        tile.el.append(tools, tag);
        tile.stageTools = tools;
        tile.stageTag = tag;
    }

    // --------------------------------------------------------- side panel
    function chatPane() {
        const pane = document.getElementById("chat-pane");
        return pane && root && pane.contains(root) ? pane : null;
    }

    function ensurePanel(pane) {
        if (!panelHead || !pane.contains(panelHead)) {
            panelHead = document.createElement("div");
            panelHead.className = "stage-panel-head";
            panelHead.innerHTML = `<div class="stage-tabs" role="tablist">
                    <button type="button" role="tab" id="stage-tab-chat" class="stage-tab" data-tab="chat" aria-controls="chat-wrap"></button>
                    <button type="button" role="tab" id="stage-tab-viewers" class="stage-tab" data-tab="viewers" aria-controls="stage-viewers"></button>
                </div>
                <button type="button" class="stage-tool stage-panel-collapse" aria-controls="chat-pane">${svg("collapse")}</button>`;
            for (const tab of panelHead.querySelectorAll(".stage-tab")) tab.onclick = () => { state.setPanel({ tab: tab.dataset.tab }); render(true); };
            panelHead.querySelector(".stage-tabs").addEventListener("keydown", event => {
                if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
                event.preventDefault();
                const next = state.panel().tab === "chat" ? "viewers" : "chat";
                state.setPanel({ tab: next });
                render(true);
                panelHead.querySelector(`[data-tab="${next}"]`).focus();
            });
            panelHead.querySelector(".stage-panel-collapse").onclick = () => { state.setPanel({ collapsed: true }); render(true); rail.querySelector("button")?.focus(); };
            viewers = document.createElement("div");
            viewers.className = "stage-viewers";
            viewers.id = "stage-viewers";
            viewers.setAttribute("role", "tabpanel");
            viewers.setAttribute("aria-labelledby", "stage-tab-viewers");
            viewers.innerHTML = `<ul class="stage-viewer-list"></ul>`;
            rail = document.createElement("div");
            rail.className = "stage-rail";
            rail.innerHTML = `<button type="button" class="stage-tool" data-open="chat">${svg("chat")}</button>
                <button type="button" class="stage-tool" data-open="viewers">${svg("users")}</button>
                <button type="button" class="stage-tool" data-open="expand">${svg("expand")}</button>`;
            for (const button of rail.querySelectorAll("button")) button.onclick = () => {
                const tab = button.dataset.open === "expand" ? undefined : button.dataset.open;
                state.setPanel({ collapsed: false, ...(tab ? { tab } : {}) });
                render(true);
                panelHead.querySelector(`[data-tab="${state.panel().tab}"]`)?.focus();
            };
            pane.prepend(panelHead);
            panelHead.after(viewers);
            pane.append(rail);
        }
        const media = root.closest(".stream-media") || root;
        if (!splitter || !pane.contains(splitter)) {
            splitter = document.createElement("div");
            splitter.className = "stage-splitter";
            splitter.tabIndex = 0;
            splitter.setAttribute("role", "separator");
            splitter.setAttribute("aria-orientation", "horizontal");
            splitter.setAttribute("aria-valuemin", String(Math.round(SPLIT_MIN * 100)));
            splitter.setAttribute("aria-valuemax", String(Math.round(SPLIT_MAX * 100)));
            splitter.innerHTML = `<span class="stage-splitter-grip"></span><span class="stage-splitter-hint"></span>`;
            bindSplitter(splitter, pane);
            chatHead = document.createElement("div");
            chatHead.className = "stage-chat-head";
            chatHead.innerHTML = `<span class="stage-chat-hash" aria-hidden="true">#</span><span class="stage-chat-name"></span><span class="stage-chat-count"></span>`;
        }
        if (splitter.previousElementSibling !== media) media.after(splitter);
        if (splitter.nextElementSibling !== chatHead) splitter.after(chatHead);
    }

    function bindSplitter(handle, pane) {
        const apply = ratio => {
            pane.style.setProperty("--stage-split", String(ratio));
            handle.setAttribute("aria-valuenow", String(Math.round(ratio * 100)));
        };
        handle.addEventListener("pointerdown", event => {
            if (event.button !== 0) return;
            event.preventDefault();
            handle.setPointerCapture?.(event.pointerId);
            handle.classList.add("dragging");
            const move = ev => {
                const box = pane.getBoundingClientRect();
                if (box.height > 0) apply(state.setSplit((ev.clientY - box.top) / box.height));
            };
            const up = () => {
                handle.classList.remove("dragging");
                handle.removeEventListener("pointermove", move);
                handle.removeEventListener("pointerup", up);
                handle.removeEventListener("pointercancel", up);
            };
            handle.addEventListener("pointermove", move);
            handle.addEventListener("pointerup", up);
            handle.addEventListener("pointercancel", up);
        });
        handle.addEventListener("dblclick", () => apply(state.resetSplit()));
        handle.addEventListener("keydown", event => {
            const current = state.split();
            const next = { ArrowUp: current - 0.04, ArrowDown: current + 0.04, Home: SPLIT_MIN, End: SPLIT_MAX }[event.key];
            if (event.key === "Enter") { event.preventDefault(); apply(state.resetSplit()); return; }
            if (next === undefined) return;
            event.preventDefault();
            apply(state.setSplit(next));
        });
        apply(state.split());
    }

    // -------------------------------------------------------------- menus
    function openAudioMenu(trigger) {
        const menu = document.createElement("div");
        menu.className = "ctx-menu stage-menu";
        const current = state.audio;
        const add = (value, label) => {
            const item = document.createElement("a");
            item.textContent = label;
            item.setAttribute("aria-checked", String(current === value));
            item.dataset.checked = String(current === value);
            item.onclick = () => {
                state.setAudio(value);
                closeContextMenu(menu, true);
                render(true);
            };
            menu.append(item);
        };
        add(AUDIO_FOLLOW, t("stage.audioFollow"));
        for (const key of state.order) add(key, tileLabel(tiles().get(key)));
        for (const item of menu.querySelectorAll("a")) item.setAttribute("role", "menuitemradio");
        mountContextMenu(menu, { trigger });
        for (const item of menu.querySelectorAll("a")) item.setAttribute("role", "menuitemradio");
    }

    function openAddMenu(trigger) {
        const options = availableStreams();
        if (!options.length) return;
        const menu = document.createElement("div");
        menu.className = "ctx-menu stage-menu stage-add-menu";
        for (const option of options) {
            const item = document.createElement("a");
            const kindLabel = t(option.slot === SLOT_SCREEN ? "polish.screen" : "polish.camera");
            item.textContent = t("stage.addItem", { name: option.name, kind: kindLabel });
            item.onclick = () => { closeContextMenu(menu, true); void option.watch(); };
            menu.append(item);
        }
        mountContextMenu(menu, { trigger });
    }

    function openVolumeMenu(trigger, tile) {
        const ctl = V()?.shareAudioCtl;
        const audio = ctl?.get(tile.clientID);
        if (!audio) return;
        const menu = document.createElement("div");
        menu.className = "ctx-menu stage-menu";
        const mute = document.createElement("a");
        mute.textContent = t(audio.muted ? "polish.sharedAudioUnmute" : "polish.sharedAudioMute");
        mute.onclick = () => { ctl.setMuted(tile.clientID, !audio.muted); closeContextMenu(menu, true); render(true); };
        const row = document.createElement("div");
        row.className = "ctx-volume";
        row.innerHTML = `<span><span class="stage-volume-label"></span> <output class="mono ctx-vol-pct"></output></span><input type="range" min="0" max="200" step="5">`;
        row.querySelector(".stage-volume-label").textContent = t("polish.sharedAudio");
        const slider = row.querySelector("input"), output = row.querySelector("output");
        slider.value = String(audio.volume);
        slider.setAttribute("aria-label", t("polish.sharedAudio"));
        const sync = () => { output.textContent = `${slider.value}%`; slider.setAttribute("aria-valuetext", `${slider.value}%`); };
        sync();
        slider.oninput = () => { sync(); ctl.setVolume(tile.clientID, Number(slider.value)); };
        menu.append(mute, row);
        mountContextMenu(menu, { trigger });
    }

    function toggleTileMute(tile) {
        const ctl = V()?.shareAudioCtl, audio = ctl?.get(tile.clientID);
        if (!audio) return;
        ctl.setMuted(tile.clientID, !audio.muted);
        render(true);
    }

    async function toggleStageFullscreen() {
        if (!root) return;
        try {
            if (document.fullscreenElement === root) await document.exitFullscreen();
            else await root.requestFullscreen();
        } catch (error) {
            V()?.toast?.(t("polish.fullscreenFailed", { error: error?.message || String(error) }), "warn");
        }
    }

    // ------------------------------------------------------------ helpers
    function client(clientID) {
        return V()?.state?.clients?.find(c => String(c.client_id) === String(clientID)) || null;
    }

    function displayName(clientID) {
        const c = client(clientID);
        return c ? (c.nickname || c.unique_id) : String(clientID);
    }

    function tileLabel(tile) {
        if (!tile) return "";
        return t("stage.addItem", { name: displayName(tile.clientID), kind: t(tile.slot === SLOT_SCREEN ? "polish.screen" : "polish.camera") });
    }

    function paintAvatar(element, clientID) {
        const v = V(), c = client(clientID), name = displayName(clientID);
        const uid = c?.unique_id || "";
        if (element.dataset.uid === uid && element.dataset.name === name && element.childElementCount) return;
        element.dataset.uid = uid;
        element.dataset.name = name;
        const url = uid && v?.state?.avatars?.get(uid);
        if (url) setSafeImage(element, url, { avatar: true });
        else {
            element.textContent = v?.initials ? v.initials(name || "?") : (name || "?").slice(0, 1).toUpperCase();
            if (uid) v?.fetchAvatar?.(uid);
        }
    }

    function hasAudio(tile) {
        return tile.slot === SLOT_SCREEN && !!V()?.shareAudioCtl?.get(tile.clientID);
    }

    function visibleTiles() {
        return [...tiles().values()].filter(tile => !tile.el.classList.contains("hidden"));
    }

    function singleTile() {
        if (kind !== "single") return null;
        return visibleTiles()[0] || null;
    }

    function announceFocus() {
        const tile = tiles().get(state.focus);
        if (tile) V()?.announceLive?.(t("stage.focusAnnounce", { name: tileLabel(tile) }));
    }

    function setMode(mode) {
        state.setMode(mode);
        render(true);
    }

    function onHotkey(event) {
        if (kind !== "multi" || !root?.isConnected || root.closest("[hidden], .hidden")) return;
        const index = hotkeyIndex(event);
        if (index < 0 || index >= state.order.length) return;
        event.preventDefault();
        state.focusIndex(index);
        render(true);
        announceFocus();
    }

    // ------------------------------------------------------------- render
    // Only publishers whose screen audio is attached are affected; the
    // controller ignores the rest. Leaving multiview sends an empty map.
    function applyGains() {
        const gains = kind === "multi" ? state.gains(DUCK_LEVEL) : new Map();
        const signature = JSON.stringify([...gains].sort());
        if (signature === lastGains) return;
        lastGains = signature;
        V()?.shareAudioCtl?.setStageGains?.(gains);
    }

    function renderHead(count) {
        const mode = state.mode();
        head.querySelector(".stage-heading").textContent = t("stage.multiview");
        head.querySelector(".stage-count").textContent = t("stage.count", { count });
        const modes = head.querySelector(".stage-modes");
        modes.setAttribute("aria-label", t("stage.layout"));
        for (const button of modes.querySelectorAll(".stage-mode")) {
            const on = button.dataset.mode === mode;
            button.querySelector("span").textContent = t(`stage.mode.${button.dataset.mode}`);
            button.setAttribute("aria-checked", String(on));
            button.tabIndex = on ? 0 : -1;
        }
        const source = tiles().get(state.audioSource());
        const audioButton = head.querySelector(".stage-audio");
        head.querySelector(".stage-audio-label").textContent = t("stage.audio", { name: source ? displayName(source.clientID) : "—" });
        setLabel(audioButton, t("stage.audioHelp"));
        audioButton.setAttribute("aria-label", `${head.querySelector(".stage-audio-label").textContent} · ${t("stage.audioHelp")}`);
        renderAdd();
        const full = document.fullscreenElement === root;
        const fsButton = head.querySelector(".stage-fullscreen");
        fsButton.querySelector("svg").outerHTML = svg(full ? "exitFullscreen" : "fullscreen");
        head.querySelector(".stage-fullscreen-label").textContent = t(full ? "stage.exitFullscreen" : "stage.fullscreen");
        fsButton.setAttribute("aria-pressed", String(full));
    }

    function renderAdd() {
        if (!root) return;
        const count = availableStreams().length;
        for (const button of root.querySelectorAll(".stage-add")) {
            button.querySelector(".stage-add-label").textContent = t("stage.add");
            button.disabled = count === 0;
            setLabel(button, t(count ? "stage.addHelp" : "stage.addNone"));
            button.hidden = button.classList.contains("stage-info-add") && count === 0;
        }
    }

    function renderFoot(count) {
        const source = tiles().get(state.audioSource());
        foot.querySelector(".stage-foot-audio span").textContent = state.audio === AUDIO_FOLLOW
            ? t("stage.footFollow", { percent: Math.round(DUCK_LEVEL * 100) })
            : t("stage.footFixed", { name: source ? displayName(source.clientID) : "—", percent: Math.round(DUCK_LEVEL * 100) });
        foot.querySelector("kbd").textContent = t("stage.hotkeyRange", { count: Math.min(HOTKEY_TILES, count) });
        foot.querySelector(".stage-foot-keys > span").textContent = t("stage.hotkeys");
    }

    function renderTile(tile, index) {
        ensureTileChrome(tile);
        const focused = kind === "multi" && tile.key === state.focus;
        const pinned = state.isPinned(tile.key);
        tile.el.style.order = String(index);
        tile.el.classList.toggle("stage-focus", focused);
        tile.el.classList.toggle("stage-pinned", pinned);
        tile.el.classList.toggle("stage-audio-source", kind === "multi" && tile.key === state.audioSource());
        if (focused) tile.el.setAttribute("aria-current", "true"); else tile.el.removeAttribute("aria-current");
        const name = displayName(tile.clientID);
        const pin = tile.stageTools.querySelector(".stage-pin");
        setLabel(pin, `${t(pinned ? "stage.unpin" : "stage.pin")} · ${name}`);
        pin.setAttribute("aria-pressed", String(pinned));
        const volume = tile.stageTools.querySelector(".stage-volume");
        const audio = hasAudio(tile) ? V().shareAudioCtl.get(tile.clientID) : null;
        volume.disabled = !audio;
        volume.innerHTML = svg(audio?.muted ? "speakerOff" : "speaker");
        setLabel(volume, audio ? `${t(audio.muted ? "polish.sharedAudioUnmute" : "polish.sharedAudioMute")} · ${name}` : t("stage.noAudio"));
        volume.setAttribute("aria-pressed", String(!!audio?.muted));
        const swap = tile.stageTools.querySelector(".stage-swap");
        setLabel(swap, `${t("stage.swap")} · ${name}`);
        swap.hidden = focused;
        if (index < HOTKEY_TILES) tile.el.setAttribute("aria-keyshortcuts", String(index + 1));
        else tile.el.removeAttribute("aria-keyshortcuts");
        paintAvatar(tile.stageTag.querySelector(".stage-tag-avatar"), tile.clientID);
        tile.stageTag.querySelector(".stage-tag-name").textContent = name;
        tile.stageTag.querySelector(".stage-live").textContent = t("stage.live");
        tile.stageTag.querySelector(".stage-tag-res").textContent = stageResolution(tile.diagnostics);
    }

    function renderInfo(tile) {
        if (!tile) { info.remove(); return; }
        if (info.parentElement !== tile.el) tile.el.append(info);
        paintAvatar(info.querySelector(".stage-info-avatar"), tile.clientID);
        info.querySelector(".stage-info-name").textContent = displayName(tile.clientID);
        info.querySelector(".stage-live").textContent = t("stage.live");
        info.querySelector(".stage-info-stats").textContent = stageStats(tile.diagnostics) || t("stage.measuring");
        const volume = info.querySelector(".stage-info-volume");
        const audio = hasAudio(tile) ? V().shareAudioCtl.get(tile.clientID) : null;
        volume.disabled = !audio;
        volume.innerHTML = svg(audio?.muted ? "speakerOff" : "speaker");
        setLabel(volume, audio ? t("stage.volume") : t("stage.noAudio"));
        const pip = info.querySelector(".stage-info-pip");
        pip.disabled = !!tile.el.querySelector(".vtile-pip")?.disabled;
        setLabel(pip, t("polish.floating"));
        const theater = info.querySelector(".stage-info-theater");
        setLabel(theater, t(state.theater() ? "stage.exitTheater" : "stage.theater"));
        theater.setAttribute("aria-pressed", String(state.theater()));
        setLabel(info.querySelector(".stage-info-fullscreen"), t("polish.fullscreen"));
        renderAdd();
    }

    function renderPanel(pane, count) {
        const side = kind === "multi" || (kind === "single" && state.theater());
        const panel = state.panel();
        pane.dataset.stage = kind;
        pane.classList.toggle("stage-side", side);
        pane.classList.toggle("stage-split", kind === "single" && !side);
        pane.classList.toggle("stage-panel-collapsed", side && panel.collapsed);
        pane.classList.toggle("stage-panel-viewers", side && !panel.collapsed && panel.tab === "viewers");
        if (kind === "none") return;
        ensurePanel(pane);
        const members = voiceMembers();
        for (const tab of panelHead.querySelectorAll(".stage-tab")) {
            const on = tab.dataset.tab === panel.tab;
            tab.textContent = tab.dataset.tab === "chat" ? t("stage.chat") : t("stage.viewers", { count: members.length });
            tab.setAttribute("aria-selected", String(on));
            tab.tabIndex = on ? 0 : -1;
        }
        panelHead.setAttribute("aria-label", t("stage.panel"));
        const collapse = panelHead.querySelector(".stage-panel-collapse");
        setLabel(collapse, t("stage.collapse"));
        collapse.setAttribute("aria-expanded", "true");
        rail.setAttribute("aria-label", t("stage.panel"));
        const railLabels = { chat: t("stage.chat"), viewers: t("stage.viewers", { count: members.length }), expand: t("stage.expand") };
        for (const button of rail.querySelectorAll("button")) setLabel(button, railLabels[button.dataset.open]);
        renderViewers(members);
        splitter.querySelector(".stage-splitter-hint").textContent = t("stage.splitterHelp");
        setLabel(splitter, t("stage.splitter"));
        splitter.title = t("stage.splitterHelp");
        const channel = V()?.state?.channels?.find(c => c.ChannelID === V().state.myChannelID);
        chatHead.querySelector(".stage-chat-name").textContent = channel?.Name || "";
        chatHead.querySelector(".stage-chat-count").textContent = t("stage.inVoice", { count: members.length });
        void count;
    }

    function voiceMembers() {
        const v = V()?.state;
        if (!v?.clients) return [];
        return v.clients.filter(c => v.myChannelID != null && c.channel_id === v.myChannelID);
    }

    function renderViewers(members) {
        const list = viewers.querySelector(".stage-viewer-list");
        const live = new Set(visibleTiles().map(tile => String(tile.clientID)));
        const signature = JSON.stringify([members.map(c => [c.client_id, c.nickname, c.unique_id, live.has(String(c.client_id))]), t("stage.live")]);
        if (list.dataset.signature === signature) return;
        list.dataset.signature = signature;
        list.replaceChildren(...members.map(c => {
            const item = document.createElement("li");
            item.className = "stage-viewer";
            const avatar = document.createElement("span");
            avatar.className = "avatar stage-viewer-avatar";
            avatar.setAttribute("aria-hidden", "true");
            paintAvatar(avatar, c.client_id);
            const name = document.createElement("span");
            name.className = "stage-viewer-name";
            name.textContent = c.nickname || c.unique_id || String(c.client_id);
            item.append(avatar, name);
            if (live.has(String(c.client_id))) {
                const badge = document.createElement("span");
                badge.className = "stage-live";
                badge.textContent = t("stage.live");
                item.append(badge);
            }
            return item;
        }));
    }

    // render re-applies the stage. Cheap when nothing changed: layoutGrid
    // calls it after every tile update, the stats poll every three seconds.
    function render(force = false) {
        if (!ensureRoot()) return;
        const visible = visibleTiles();
        const keys = visible.map(tile => tile.key);
        const nextKind = stageKind(visible.length);
        const order = state.setTiles(keys);
        const mode = state.mode();
        const changed = force || nextKind !== kind || root.dataset.layout !== mode || root.dataset.order !== order.join(",") || root.dataset.focus !== String(state.focus);
        kind = nextKind;
        root.dataset.stage = kind;
        root.dataset.layout = mode;
        root.dataset.order = order.join(",");
        root.dataset.focus = String(state.focus);
        root.setAttribute("aria-label", t("stage.region"));
        const grid = gridEl();
        const shape = mode === "focus" ? { cols: Math.max(1, order.length - 1), rows: 2 } : gridShape(order.length);
        grid.style.setProperty("--stage-cols", String(shape.cols));
        grid.style.setProperty("--stage-rows", String(shape.rows));
        const byKey = tiles();
        order.forEach((key, index) => { const tile = byKey.get(key); if (tile) renderTile(tile, index); });
        if (kind === "multi") { renderHead(order.length); renderFoot(order.length); }
        renderInfo(kind === "single" ? visible[0] : null);
        const pane = chatPane();
        if (pane) renderPanel(pane, order.length);
        applyGains();
        // Quality keeps its existing rule: only a focused large view asks
        // for the high layer (receive-quality.js), grid and stack stay auto.
        const nextQuality = kind === "multi" && mode === "focus" ? state.focus : null;
        if (nextQuality !== qualityFocus) {
            qualityFocus = nextQuality;
            onFocusChange?.(qualityFocus);
        }
        return changed;
    }

    // tileClicked: in multiview a click focuses the tile (audio follows).
    function tileClicked(key) {
        if (kind !== "multi" || state.focus === key) return;
        state.setFocus(key);
        render(true);
        announceFocus();
    }

    return {
        render, tileClicked,
        get qualityFocus() { return qualityFocus; },
        get kind() { return kind; },
        state,
    };
}
