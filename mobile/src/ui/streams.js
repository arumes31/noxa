// streams.js — watching channel streams on the voice screen: the list of
// published cameras and screens, a stage that shows several at once (grid,
// or focus with a strip of small tiles), receive quality per stream,
// fullscreen with a tap-to-toggle overlay, picture-in-picture, and the
// phone's own camera controls (flip, effects, server limits).
import * as settings from "../core/settings.js";
import { QUALITIES, StageState } from "../core/video.js";
import { t } from "../i18n.js";
import { h, toast } from "./dom.js";
import { icon } from "./icons.js";
import {
    cameraSheet, enterFullscreen, exitFullscreen, fullscreenElement, fullscreenSupported, iconButton,
    pipSupported, togglePictureInPicture, videoIcon, videoSettingsSheet,
} from "./video-kit.js";

const DOUBLE_TAP = 300;

function publisherName(session, id) {
    return session.clients().find(c => c.client_id === id)?.nickname || t("dm.someone");
}

/** The shared-audio element that belongs to a watched screen. */
function streamAudio(voice, key) {
    return key.endsWith("|screen") ? voice.audio.get(`${key}audio`) : null;
}

function qualitySelect(voice, key, label) {
    const select = h("select", { class: "input quality-select", "aria-label": label, onchange: async event => {
        try { await voice.setStreamQuality(key, event.target.value); } catch (error) { toast(error.message, "warn"); }
    } }, QUALITIES.map(q => h("option", { value: q, selected: (voice.quality.get(key) || "auto") === q }, t(`video.quality.${q}`))));
    return select;
}

/**
 * The streams block of the voice screen. Returns an element with
 * dispose(); it follows voice events and polls for new publications.
 */
export function streamsPanel(voice, session) {
    const stage = new StageState();
    /** key -> {tile, video, stream, track, muted, select} */
    const tiles = new Map();
    let listed = new Map();
    let listSignature = "";
    let hudHidden = false;

    const list = h("div", { class: "stream-list" });
    const tileBox = h("div", { class: "stage-tiles" });
    const hudName = h("span", { class: "stage-hud-name" });
    const hudMute = h("button", { class: "btn stage-hud-btn", type: "button", onclick: event => { event.stopPropagation(); toggleMute(stage.focusKey || stage.keys[0]); } });
    const hudQuality = h("span", { class: "stage-hud-quality" });
    const hudPip = pipSupported() ? iconButton("pip", t("video.pip"), event => { event.stopPropagation(); pip(); }) : null;
    const hudExit = iconButton("exitFullscreen", t("video.exitFullscreen"), event => { event.stopPropagation(); exitFullscreen(); });
    const hud = h("div", { class: "stage-hud", role: "toolbar", "aria-label": t("video.stage") }, hudName, hudMute, hudQuality, hudPip, hudExit);
    const rotate = h("p", { class: "stage-rotate", role: "status", hidden: true }, t("video.rotate"));
    const stageEl = h("div", { class: "stage", role: "region", "aria-label": t("video.stage"), dataset: { layout: stage.layout }, hidden: true }, tileBox, hud, rotate);

    const layoutButton = h("button", { class: "btn stage-layout", type: "button", onclick: () => {
        stage.setLayout(stage.layout === "grid" ? "focus" : "grid");
        applyLayout();
    } });
    const fullscreenButton = fullscreenSupported() ? iconButton("fullscreen", t("video.fullscreen"), async () => {
        const focus = tiles.get(stage.focusKey || stage.keys[0]);
        try { await enterFullscreen(stageEl, focus?.video); } catch (error) { toast(error.message, "warn"); }
    }) : null;
    const pipButton = pipSupported() ? iconButton("pip", t("video.pip"), () => pip()) : null;
    const toolbar = h("div", { class: "stage-toolbar", hidden: true }, layoutButton, fullscreenButton, pipButton);

    const flipButton = h("button", { class: "btn", type: "button", "aria-label": t("video.flip"), onclick: async () => {
        flipButton.disabled = true;
        try { await voice.flipCamera(); } catch (error) { toast(error.message, "warn"); }
        flipButton.disabled = false;
        renderCamera();
    } }, videoIcon("flip"), " ", t("video.flip"));
    const cameraRow = h("div", { class: "camera-row", hidden: true }, flipButton,
        h("button", { class: "btn", type: "button", onclick: () => cameraSheet(voice, session).then(renderCamera) }, videoIcon("settings"), " ", t("video.cameraOptions")));

    const element = h("div", { class: "streams" },
        cameraRow,
        list,
        toolbar,
        stageEl,
        h("button", { class: "btn btn-block stage-settings", type: "button", onclick: () => videoSettingsSheet(voice, session) }, videoIcon("settings"), " ", t("video.settings")));

    function renderCamera() {
        cameraRow.hidden = !voice.camera;
        flipButton.dataset.facing = voice.facingMode;
        flipButton.setAttribute("aria-pressed", String(voice.facingMode === "environment"));
    }

    function label(stream) {
        const name = publisherName(session, stream.publisher_id);
        return t(stream.slot === "screen" ? "video.screenOf" : "video.cameraOf", { name });
    }

    async function pip() {
        const tile = tiles.get(stage.focusKey || stage.keys[0]);
        if (!tile) return;
        const channel = session.channels().find(c => c.id === voice.channelID);
        try { await togglePictureInPicture(tile.video, { title: label(tile.stream), artist: channel?.name || "noXa" }); } catch (error) { toast(error.message, "warn"); }
    }

    function toggleMute(key) {
        const tile = tiles.get(key);
        if (!tile) return;
        tile.muted = !tile.muted;
        applyVolumes();
        renderHud();
    }

    function applyVolumes() {
        for (const key of stage.keys) {
            const audio = streamAudio(voice, key);
            if (audio) audio.volume = tiles.get(key)?.muted ? 0 : stage.volume(key);
        }
    }

    function renderHud() {
        const key = stage.focusKey || stage.keys[0];
        const tile = tiles.get(key);
        hudName.textContent = tile ? label(tile.stream) : "";
        hudMute.textContent = tile?.muted ? t("video.unmuteStream") : t("video.muteStream");
        hudMute.setAttribute("aria-pressed", String(!!tile?.muted));
        hudMute.hidden = !streamAudio(voice, key || "");
        hudQuality.replaceChildren(tile ? qualitySelect(voice, key, t("video.qualityFor", { name: label(tile.stream) })) : "");
        stageEl.classList.toggle("hud-hidden", hudHidden);
        const fullscreen = fullscreenElement() === stageEl;
        stageEl.classList.toggle("is-fullscreen", fullscreen);
        const portrait = globalThis.matchMedia?.("(orientation: portrait)").matches;
        const wide = tile?.video.videoWidth > tile?.video.videoHeight;
        rotate.hidden = !(fullscreen && portrait && wide);
    }

    function applyLayout() {
        stageEl.dataset.layout = stage.layout;
        const focus = stage.focusKey;
        const order = focus ? [focus, ...stage.others] : stage.keys;
        for (const key of order) {
            const tile = tiles.get(key);
            if (!tile) continue;
            tile.tile.classList.toggle("focused", key === focus);
            tile.tile.setAttribute("aria-current", key === focus ? "true" : "false");
            tile.swap.hidden = !focus || key === focus;
            tileBox.append(tile.tile);
        }
        layoutButton.replaceChildren(videoIcon(stage.layout === "grid" ? "focus" : "grid"), " ", t(stage.layout === "grid" ? "video.layoutFocus" : "video.layoutGrid"));
        layoutButton.setAttribute("aria-label", `${t("video.layout")}: ${t(stage.layout === "grid" ? "video.layoutFocus" : "video.layoutGrid")}`);
        layoutButton.hidden = stage.keys.length < 2;
        stageEl.hidden = !stage.keys.length;
        toolbar.hidden = !stage.keys.length;
        voice.focusKey = focus;
        applyVolumes();
        renderHud();
        voice.applyQualities().catch(() => {});
    }

    function makeTile(key, stream, track) {
        const video = h("video", { class: "stream-video", autoplay: true, playsinline: true, muted: true, "aria-label": label(stream) });
        video.srcObject = new MediaStream([track]);
        video.addEventListener("resize", renderHud);
        // One tap focuses the tile (or toggles the overlay in fullscreen);
        // a double tap switches between fit and fill.
        let lastTap = 0;
        let pending = 0;
        video.addEventListener("pointerup", () => {
            const now = Date.now();
            if (now - lastTap < DOUBLE_TAP) {
                clearTimeout(pending);
                video.classList.toggle("fill");
                lastTap = 0;
                return;
            }
            lastTap = now;
            pending = setTimeout(() => {
                if (fullscreenElement() === stageEl) {
                    hudHidden = !hudHidden;
                    renderHud();
                    return;
                }
                stage.tap(key);
                applyLayout();
            }, DOUBLE_TAP);
        });
        const swap = iconButton("swap", t("video.swap"), () => {
            const focus = stage.focusKey;
            if (focus) stage.swap(focus, key);
            applyLayout();
        });
        const select = qualitySelect(voice, key, t("video.qualityFor", { name: label(stream) }));
        const tile = h("div", { class: "stream-tile stage-tile", dataset: { key } }, video,
            h("div", { class: "tile-bar" },
                h("span", { class: "tile-label" }, label(stream)),
                select,
                swap,
                h("button", { class: "btn stream-close", type: "button", onclick: () => voice.watch(stream, false).catch(() => {}) }, t("voice.stopWatching"))));
        return { tile, video, stream, track, swap, select, muted: false };
    }

    function syncTiles() {
        const keys = [];
        for (const [key, stream] of listed) {
            const track = voice.watching.has(key) ? voice.video.get(key) : null;
            if (!track) continue;
            keys.push(key);
            const existing = tiles.get(key);
            if (existing && existing.track === track && existing.stream.generation === stream.generation) continue;
            existing?.tile.remove();
            tiles.set(key, makeTile(key, stream, track));
        }
        for (const [key, tile] of tiles) {
            if (keys.includes(key)) continue;
            tile.video.srcObject = null;
            tile.tile.remove();
            tiles.delete(key);
        }
        stage.sync(keys);
        for (const [key, tile] of tiles) {
            const confirmed = voice.confirmed.get(key);
            if (confirmed) tile.tile.dataset.quality = confirmed;
            else delete tile.tile.dataset.quality;
        }
        applyLayout();
    }

    function renderList() {
        const signature = [...listed].map(([key, s]) => `${key}|${s.generation}|${voice.watching.has(key)}`).join(",");
        if (signature === listSignature) return;
        listSignature = signature;
        list.replaceChildren(...[...listed].filter(([key]) => !voice.watching.has(key)).map(([, stream]) => {
            const publisher = publisherName(session, stream.publisher_id);
            return h("button", { class: "card row stream-card", type: "button", onclick: () => voice.watch(stream).catch(error => toast(error.message, "warn")) },
                icon("screen"), h("span", { class: "row-main" }, h("span", { class: "row-title" }, t(stream.slot === "screen" ? "voice.watchScreen" : "voice.watchCamera", { name: publisher }))));
        }));
    }

    async function refresh() {
        let streams = [];
        try { streams = await voice.streams(); } catch { streams = []; }
        listed = new Map(streams.map(stream => [`${stream.publisher_id}|${stream.slot}`, stream]));
        renderList();
        syncTiles();
    }

    const onTracks = () => { renderList(); syncTiles(); refresh(); };
    const onQuality = event => {
        const { key, quality, error } = event.detail || {};
        const tile = tiles.get(key);
        if (tile) {
            if (quality) tile.tile.dataset.quality = quality;
            else delete tile.tile.dataset.quality;
        }
        if (error) toast(error.message, "warn");
    };
    const onFullscreen = () => { hudHidden = false; renderHud(); };
    const onOrientation = () => renderHud();
    voice.addEventListener("tracks", onTracks);
    voice.addEventListener("quality", onQuality);
    voice.addEventListener("state", renderCamera);
    document.addEventListener("fullscreenchange", onFullscreen);
    document.addEventListener("webkitfullscreenchange", onFullscreen);
    globalThis.addEventListener("orientationchange", onOrientation);
    const stopSettings = settings.onChange(({ changed }) => {
        if (changed.some(name => name === "receiveQuality" || name === "lowBandwidth")) voice.applyQualities().catch(() => {});
    });
    // New publications show up without an event of their own.
    const timer = setInterval(refresh, 5000);
    renderCamera();
    refresh();

    element.dispose = () => {
        clearInterval(timer);
        voice.removeEventListener("tracks", onTracks);
        voice.removeEventListener("quality", onQuality);
        voice.removeEventListener("state", renderCamera);
        document.removeEventListener("fullscreenchange", onFullscreen);
        document.removeEventListener("webkitfullscreenchange", onFullscreen);
        globalThis.removeEventListener("orientationchange", onOrientation);
        stopSettings();
        if (fullscreenElement() === stageEl) exitFullscreen();
        // Ducked shared audio returns to full volume off this screen.
        for (const key of stage.keys) { const audio = streamAudio(voice, key); if (audio) audio.volume = 1; }
        voice.focusKey = null;
    };
    return element;
}
