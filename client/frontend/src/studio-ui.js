// studio-ui.js — the compact OLED Midnight Studio panel and its ownership.
//
// Ownership rules (explicit, tested):
// - A Studio session (engine, capture registry, audio graph, image bitmaps)
//   exists only after the user opens the panel. Nothing is captured on open:
//   a restored layout shows "recapture" until the user picks each source.
// - Closing the panel while live keeps the program on air (its captures stay
//   held by the program) and stops captures used only by the draft. Closing
//   while off air ends the session and stops every capture.
// - Stop (here, in the share bar, or the voice button) ends the publication
//   and the whole session: capture tracks, audio nodes, timers, canvases and
//   bitmaps. Account/server/channel switch and disconnect call disposeStudio().
// - The only way a draft reaches the program is Take.
import { t } from "./i18n.js";
import { mountServerDialog, closeDialog, topDialog } from "./modal.js";
import { labelButton } from "./icons.js";
import { retainDisplayCapture, releaseDisplayCapture } from "./display-capture-lease.js";
import { displayAudioOptions, validateDisplayAudio } from "./display-audio.js";
import { cameraConstraints } from "./camera-capture.js";
import { startStudioShare, studioShareActive, stopShare } from "./video.js";
import {
    MAX_SCENES, MAX_SOURCES, TRANSITION_MIN_MS, TRANSITION_MAX_MS, StudioStateError,
    parseStudio, serializeStudio, selectedScene, addScene, renameScene, removeScene, selectScene, setTransition,
    addSource, updateSource, removeSource, moveSource, take, assetRefs, captureRefs,
} from "./studio-state.js";
import { createCaptureRegistry } from "./studio-captures.js";
import { createAssetStore, MAX_IMAGE_BYTES, StudioImageError } from "./studio-assets.js";
import { studioLayoutKey, studioAssetDB, dropLegacyStudio } from "./studio-storage.js";
import { createStudioAudio } from "./studio-audio.js";
import { StudioEngine, outputSize, STUDIO_FPS } from "./studio-compositor.js";
import "./studio.css";

const V = () => window.__noxa;
let session = null;
let panel = null;
let panelID = 0;
let sessionID = 0;

function storage() {
    try { return globalThis.localStorage; } catch { return null; }
}

// The layout and images belong to the identity the Studio runs for
// (studio-storage.js); without a known identity nothing is persisted.
function loadStudio(uid) {
    const key = studioLayoutKey(uid);
    try { return parseStudio(key ? storage()?.getItem(key) || "" : ""); } catch { return parseStudio(""); }
}

function saveStudio(uid, studio) {
    const key = studioLayoutKey(uid);
    if (!key) return;
    try { storage()?.setItem(key, serializeStudio(studio)); } catch { /* storage is optional */ }
}

let legacyDropped = false;

function errorText(error) {
    const code = error instanceof StudioStateError || error instanceof StudioImageError ? error.code : "";
    return code && t(`studio.error.${code}`) !== `studio.error.${code}` ? t(`studio.error.${code}`) : (error?.message || t("studio.error.generic"));
}

function createSession() {
    const id = ++sessionID;
    const registry = createCaptureRegistry({
        onChange: () => { if (session?.id === id) { session.engine?.renderProgram(performance.now()); syncAudio(); schedule(); } },
        onRelease: stream => releaseDisplayCapture(stream),
    });
    const uid = String(V()?.state?.myUniqueID || "");
    if (!legacyDropped) { legacyDropped = true; void dropLegacyStudio(); }
    const assetDB = studioAssetDB(uid);
    const assets = assetDB ? createAssetStore({ name: assetDB }) : createAssetStore({ indexedDB: null });
    const { width, height } = outputSize(V()?.state?.mediaLimits);
    const engine = new StudioEngine({ registry, assets, width, height, fps: STUDIO_FPS,
        label: source => source.kind === "image" ? t("studio.sourceUnavailable") : t(`studio.state.${registry.state(source.capture)}`) });
    const studio = loadStudio(uid);
    session = { id, uid, registry, assets, engine, audio: null, studio, live: false, starting: false,
        selectedSource: "", width, height, meterTimer: null, restored: studio.scenes.some(scene => scene.sources.some(source => source.kind === "display" || source.kind === "camera")) };
    engine.setDraft(selectedScene(studio), studio.scenes);
    void assets.load(assetRefs(studio)).then(() => { if (session?.id === id) { engine.renderPreview(); schedule(); } });
    return session;
}

function audioGraph() {
    if (!session.audio) session.audio = createStudioAudio();
    return session.audio;
}

// Audio rule (Take applies audio exactly like pictures):
// - "Stream this capture's audio" is a property of a draft source
//   (source.audio). Editing it, capturing with audio or recapturing changes
//   only the draft and never the published audio.
// - The routed set is computed from the PROGRAM snapshot only: a capture's
//   app/system audio is routed iff a VISIBLE source of the live program
//   references it with audio enabled and its audio track is live; once per
//   capture, however many sources reference it. Hiding a source takes its
//   audio off too (privacy first) - at the next Take, like the picture.
// - It changes only at Take (and when a routed track ends or a capture is
//   released). During a crossfade only the incoming program counts: the
//   outgoing scene's audio stops at Take and a fade never adds audio.
function programAudio(owner) {
    const ids = new Set();
    for (const source of owner.engine.live?.sources || []) if (source.kind === "display" && source.visible && source.audio && source.capture) ids.add(source.capture);
    return ids;
}

function syncAudio() {
    if (!session) return;
    const enabled = programAudio(session);
    if (!session.audio && !enabled.size) return;
    audioGraph().sync(session.registry.audioTracks(), enabled);
}

function disposeSession() {
    const current = session;
    if (!current) return;
    session = null;
    // Never leave a publication running on a stopped program (it would show
    // a frozen frame): end it first. Its onEnded sees a retired session.
    if (current.live && studioShareActive()) void stopShare();
    clearInterval(current.meterTimer);
    current.engine.dispose(); // registry, captures, bitmaps, timer, canvases
    current.audio?.dispose();
}

function commit(next, { prune = false } = {}) {
    if (!session) return;
    session.studio = next;
    saveStudio(session.uid, next);
    session.engine.setDraft(selectedScene(next), next.scenes);
    // Draft edits never touch audio routing (see programAudio).
    if (prune) { const owner = session; void owner.assets.prune(() => assetRefs(owner.studio, [owner.engine.live, owner.engine.outgoing])); }
    schedule();
}

function apply(update, options) {
    if (!session) return false;
    try { commit(update(session.studio), options); return true; }
    catch (error) { showError(errorText(error)); return false; }
}

function showError(message) {
    const node = panel?.overlay.querySelector(".studio-error");
    if (node) node.textContent = message;
}

// ---- actions -----------------------------------------------------------

function doTake() {
    if (!session) return;
    const snapshot = take(session.studio);
    session.engine.take(snapshot, session.studio.transition);
    syncAudio(); // the only place a draft audio choice reaches the program
    schedule();
}

async function captureDisplay(audioMode) {
    const options = { video: { frameRate: { ideal: STUDIO_FPS, max: STUDIO_FPS } }, ...displayAudioOptions(audioMode) };
    let stream;
    try { stream = await navigator.mediaDevices.getDisplayMedia(options); }
    catch (failure) {
        if (audioMode === "none" || ["NotAllowedError", "AbortError"].includes(failure.name)) throw failure;
        stream = await navigator.mediaDevices.getDisplayMedia({ ...options, ...displayAudioOptions("none") });
        audioMode = "none";
    }
    audioMode = validateDisplayAudio(stream, audioMode);
    return { stream, audio: audioMode !== "none" };
}

// capture runs only from an explicit user action. `replace` recaptures a
// source in the draft; the program keeps its own capture until the next Take.
async function capture(kind, { replace = "" } = {}) {
    const owner = session;
    if (!owner) return;
    showError("");
    const audioMode = panel?.overlay.querySelector(".studio-capture-audio")?.value || "none";
    let result;
    try {
        if (kind === "camera") {
            const stream = await navigator.mediaDevices.getUserMedia({ video: cameraConstraints(V()?.state?.settings, V()?.state?.mediaLimits), audio: false });
            result = { stream, audio: false };
        } else result = await captureDisplay(audioMode);
    } catch (error) {
        if (session === owner) showError(t("studio.captureFailed", { error: error.message || error.name }));
        return;
    }
    if (session !== owner) { result.stream.getTracks().forEach(track => track.stop()); return; }
    const video = result.stream.getVideoTracks()[0];
    const surface = video?.getSettings?.().displaySurface === "window" ? "window" : "monitor";
    let id;
    try {
        if (kind === "display") retainDisplayCapture(result.stream);
        id = owner.registry.add(result.stream, { kind, label: video?.label || "", surface, audio: result.audio });
    } catch (error) { releaseDisplayCapture(result.stream); showError(t("studio.captureFailed", { error: error.message })); return; }
    // Captured audio is enabled on the DRAFT source only; it reaches the
    // program (and the publication) with the next Take.
    const scene = selectedScene(owner.studio);
    const ok = apply(studio => {
        if (replace) return updateSource(studio, scene.id, replace, { capture: id, surface, audio: result.audio });
        const added = addSource(studio, scene.id, { kind, name: t(`studio.kind.${kind}`), capture: id, surface, audio: result.audio });
        owner.selectedSource = added.source.id;
        return added.studio;
    });
    // A rejected add must not keep the capture alive.
    if (!ok) owner.registry.setHolder("draft", captureRefs(owner.studio.scenes));
}

function reuseCapture(id) {
    if (!session?.registry.get(id)) return;
    const entry = session.registry.get(id);
    const scene = selectedScene(session.studio);
    apply(studio => {
        const added = addSource(studio, scene.id, { kind: entry.kind, name: t(`studio.kind.${entry.kind}`), capture: id, surface: entry.surface, audio: !!entry.audioTrack });
        session.selectedSource = added.source.id;
        return added.studio;
    });
}

async function addImage(file) {
    const owner = session;
    if (!owner || !file) return;
    showError("");
    if (file.size > MAX_IMAGE_BYTES) { showError(t("studio.error.image_too_large")); return; }
    try {
        const bytes = new Uint8Array(await file.arrayBuffer());
        const asset = await owner.assets.add(bytes);
        if (session !== owner) return;
        const scene = selectedScene(owner.studio);
        // Fit the picture's aspect inside the canvas.
        const aspect = (asset.width / asset.height) / (owner.width / owner.height);
        const w = aspect >= 1 ? 1 : aspect, h = aspect >= 1 ? 1 / aspect : 1;
        apply(studio => {
            const added = addSource(studio, scene.id, { kind: "image", name: file.name || t("studio.kind.image"), asset: asset.id, w, h, x: (1 - w) / 2, y: (1 - h) / 2 });
            owner.selectedSource = added.source.id;
            return added.studio;
        });
        // The add committed (or failed): the asset is no longer protected as
        // in-flight, so a later removal (or this failure) prunes it now.
        owner.assets.settle(asset.id);
        if (session === owner) void owner.assets.prune(() => assetRefs(owner.studio, [owner.engine.live, owner.engine.outgoing]));
    } catch (error) { if (session === owner) showError(errorText(error)); }
}

function addText() {
    if (!session) return;
    const scene = selectedScene(session.studio);
    apply(studio => {
        const added = addSource(studio, scene.id, { kind: "text", name: t("studio.kind.text"), text: t("studio.defaultText"), x: 0.05, y: 0.8, w: 0.9, h: 0.15, size: 48 });
        session.selectedSource = added.source.id;
        return added.studio;
    });
}

async function goLive() {
    const owner = session;
    if (!owner || owner.live || owner.starting) return;
    showError("");
    owner.starting = true;
    schedule();
    const stream = owner.engine.outputStream(audioGraph().track);
    try {
        const started = await startStudioShare(stream, { width: owner.width, height: owner.height, fps: STUDIO_FPS,
            onEnded: () => studioEnded(owner), change: () => openStudio() });
        if (session !== owner) {
            // Retired while starting (panel closed, teardown): end what started.
            if (started && studioShareActive()) void stopShare();
            stream.getTracks().forEach(track => track.stop());
            return;
        }
        owner.live = !!started;
        if (!started) { stream.getTracks().forEach(track => track.stop()); showError(t("studio.liveFailed", { error: t("share.startFailed") })); }
    } catch (error) {
        stream.getTracks().forEach(track => track.stop());
        if (session === owner) showError(t("studio.liveFailed", { error: error.message || String(error) }));
    } finally {
        if (session === owner) {
            owner.starting = false;
            // Panel closed while starting and the start failed: nothing owns the session.
            if (!panel && !owner.live) disposeSession(); else schedule();
        }
    }
}

// studioEnded: the publication stopped (Stop anywhere). The whole session
// ends; an open panel continues with a fresh, capture-free session.
function studioEnded(owner) {
    if (session !== owner) return;
    disposeSession();
    if (panel) { createSession(); render(); }
}

function stopStudio() {
    if (!session) return;
    if (session.live && studioShareActive()) { void stopShare(); return; }
    disposeSession();
    if (panel) { createSession(); render(); }
}

// ---- rendering ---------------------------------------------------------

let scheduled = false;
function schedule() {
    if (scheduled || !panel) return;
    scheduled = true;
    queueMicrotask(() => { scheduled = false; render(); });
}

function element(tag, props = {}, children = []) {
    const node = document.createElement(tag);
    for (const [key, value] of Object.entries(props)) {
        if (key === "text") node.textContent = value;
        else if (key === "class") node.className = value;
        else if (key.startsWith("on")) node.addEventListener(key.slice(2), value);
        else if (key === "attrs") for (const [name, attr] of Object.entries(value)) node.setAttribute(name, attr);
        else node[key] = value;
    }
    node.append(...children.filter(Boolean));
    return node;
}

function iconButton(className, icon, label, onclick) {
    const button = element("button", { type: "button", class: className, onclick });
    labelButton(button, icon, label);
    button.setAttribute("aria-label", label);
    button.title = label;
    return button;
}

function buildPanel() {
    const id = `studio-${++panelID}`;
    const overlay = element("div", { class: "dlg-overlay studio-overlay" });
    const dialog = element("div", { class: "dlg studio" });
    overlay.append(dialog);
    dialog.innerHTML = `
        <header class="studio-head">
            <h3 id="${id}-title">${t("studio.title")}</h3>
            <span class="studio-air" role="status" aria-live="polite"></span>
            <button type="button" class="studio-close" aria-label="${t("studio.close")}" title="${t("studio.close")}"></button>
        </header>
        <div class="studio-monitors">
            <figure class="studio-monitor studio-preview"><figcaption>${t("studio.preview")}</figcaption></figure>
            <div class="studio-switcher">
                <button type="button" class="studio-take primary" title="${t("studio.takeHelp")}" aria-keyshortcuts="Control+Enter">${t("studio.take")}</button>
                <label class="studio-field"><span>${t("studio.transition")}</span>
                    <select class="studio-transition"><option value="cut">${t("studio.cut")}</option><option value="fade">${t("studio.fade")}</option></select></label>
                <label class="studio-field"><span>${t("studio.duration")}</span>
                    <input class="studio-duration" type="number" min="${TRANSITION_MIN_MS}" max="${TRANSITION_MAX_MS}" step="50" /></label>
            </div>
            <figure class="studio-monitor studio-program"><figcaption>${t("studio.program")}</figcaption></figure>
        </div>
        <div class="studio-body">
            <section class="studio-scenes" aria-labelledby="${id}-scenes"><h4 id="${id}-scenes">${t("studio.scenes")}</h4>
                <ul class="studio-scene-list"></ul>
                <div class="studio-scene-tools">
                    <input class="studio-scene-name" maxlength="48" aria-label="${t("studio.sceneName")}" />
                    <button type="button" class="studio-add-scene">${t("studio.addScene")}</button>
                    <button type="button" class="studio-remove-scene">${t("studio.removeScene")}</button>
                </div>
            </section>
            <section class="studio-sources" aria-labelledby="${id}-sources"><h4 id="${id}-sources">${t("studio.sources")}</h4>
                <div class="studio-add">
                    <button type="button" class="studio-add-display">${t("studio.addDisplay")}</button>
                    <label class="studio-field studio-inline"><span>${t("studio.captureAudio")}</span>
                        <select class="studio-capture-audio"><option value="none">${t("share.audioNone")}</option><option value="application">${t("share.audioApplication")}</option><option value="system">${t("share.audioSystem")}</option></select></label>
                    <button type="button" class="studio-add-camera">${t("studio.addCamera")}</button>
                    <button type="button" class="studio-add-image">${t("studio.addImage")}</button>
                    <input type="file" class="studio-image-file" accept="image/png,image/jpeg" hidden />
                    <button type="button" class="studio-add-text">${t("studio.addText")}</button>
                    <select class="studio-reuse" aria-label="${t("studio.reuse")}"></select>
                </div>
                <p class="set-hint studio-restored" hidden>${t("studio.restored")}</p>
                <ul class="studio-source-list"></ul>
            </section>
            <section class="studio-props" aria-labelledby="${id}-props"><h4 id="${id}-props">${t("studio.properties")}</h4><div class="studio-prop-body"></div></section>
        </div>
        <footer class="studio-foot">
            <p class="set-hint studio-keys">${t("studio.keys")}</p>
            <p class="set-hint studio-ownership"></p>
            <div class="studio-meter" role="meter" aria-label="${t("studio.audioLevel")}" aria-valuemin="0" aria-valuemax="100" aria-valuenow="0"><span></span></div>
            <p class="studio-error warn" role="alert"></p>
            <div class="dlg-buttons">
                <button type="button" class="studio-release">${t("studio.stopStudio")}</button>
                <button type="button" class="studio-stop">${t("studio.stopLive")}</button>
                <button type="button" class="studio-golive primary">${t("studio.goLive")}</button>
            </div>
        </footer>`;
    const $ = selector => dialog.querySelector(selector);
    labelButton($(".studio-close"), "close", t("studio.close"));
    $(".studio-close").onclick = () => closeDialog(overlay, "cancel");
    $(".studio-take").onclick = doTake;
    $(".studio-transition").onchange = event => apply(studio => setTransition(studio, { kind: event.target.value }));
    $(".studio-duration").onchange = event => apply(studio => setTransition(studio, { ms: Number(event.target.value) }));
    $(".studio-add-scene").onclick = () => apply(studio => addScene(studio, t("studio.scene", { number: studio.scenes.length + 1 })));
    $(".studio-remove-scene").onclick = () => apply(studio => removeScene(studio, studio.selected), { prune: true });
    $(".studio-scene-name").onchange = event => apply(studio => renameScene(studio, studio.selected, event.target.value));
    $(".studio-add-display").onclick = () => capture("display");
    $(".studio-add-camera").onclick = () => capture("camera");
    $(".studio-add-image").onclick = () => $(".studio-image-file").click();
    $(".studio-image-file").onchange = event => { const file = event.target.files?.[0]; event.target.value = ""; void addImage(file); };
    $(".studio-add-text").onclick = addText;
    $(".studio-reuse").onchange = event => { const value = event.target.value; event.target.value = ""; if (value) reuseCapture(value); };
    $(".studio-golive").onclick = goLive;
    $(".studio-stop").onclick = stopStudio;
    $(".studio-release").onclick = stopStudio;
    return { overlay, dialog, $ };
}

function editing(target) {
    return target instanceof HTMLElement && (target.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName));
}

// Studio shortcuts work wherever focus is while the Studio is the top dialog.
function onKey(event) {
    if (!session || !panel || topDialog() !== panel.overlay || event.defaultPrevented) return;
    if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
        event.preventDefault();
        // A value typed but not yet committed (fields commit on change) is
        // applied to the draft first, so Take takes what the user entered.
        const active = document.activeElement;
        if (editing(active) && panel.dialog.contains(active)) active.dispatchEvent(new Event("change", { bubbles: true }));
        doTake();
        return;
    }
    if (editing(event.target) || event.altKey && !["ArrowUp", "ArrowDown"].includes(event.key)) return;
    if (!event.ctrlKey && !event.metaKey && !event.altKey && /^[1-8]$/.test(event.key)) {
        const scene = session.studio.scenes[Number(event.key) - 1];
        if (scene) { event.preventDefault(); apply(studio => selectScene(studio, scene.id)); }
        return;
    }
    const row = event.target.closest?.("[data-source]");
    if (event.altKey && row && (event.key === "ArrowUp" || event.key === "ArrowDown")) {
        event.preventDefault();
        const sourceID = row.dataset.source;
        // Up in the list means higher in the stack.
        apply(studio => moveSource(studio, studio.selected, sourceID, event.key === "ArrowUp" ? "up" : "down"));
        queueMicrotask(() => panel?.overlay.querySelector(`[data-source="${sourceID}"] .studio-source-select`)?.focus());
    }
}

function numberField(label, value, onchange, { min = 0, max = 100, step = 1 } = {}) {
    const input = element("input", { type: "number", min, max, step, value: String(value), onchange: event => onchange(Number(event.target.value)) });
    return element("label", { class: "studio-field" }, [element("span", { text: label }), input]);
}

function renderProps(container, scene) {
    const source = scene.sources.find(item => item.id === session.selectedSource);
    container.replaceChildren();
    if (!source) { container.append(element("p", { class: "set-hint", text: t("studio.selectSource") })); return; }
    const set = patch => apply(studio => updateSource(studio, scene.id, source.id, patch));
    const percent = value => Math.round(value * 1000) / 10;
    const geometry = element("div", { class: "studio-grid" }, [
        numberField(t("studio.x"), percent(source.x), value => set({ x: value / 100 })),
        numberField(t("studio.y"), percent(source.y), value => set({ y: value / 100 })),
        numberField(t("studio.w"), percent(source.w), value => set({ w: value / 100 }), { min: 2 }),
        numberField(t("studio.h"), percent(source.h), value => set({ h: value / 100 }), { min: 2 }),
    ]);
    const crop = source.kind === "text" ? null : element("div", { class: "studio-grid" }, [
        numberField(t("studio.cropL"), percent(source.crop.l), value => set({ crop: { l: value / 100 } }), { max: 45 }),
        numberField(t("studio.cropT"), percent(source.crop.t), value => set({ crop: { t: value / 100 } }), { max: 45 }),
        numberField(t("studio.cropR"), percent(source.crop.r), value => set({ crop: { r: value / 100 } }), { max: 45 }),
        numberField(t("studio.cropB"), percent(source.crop.b), value => set({ crop: { b: value / 100 } }), { max: 45 }),
    ]);
    const name = element("label", { class: "studio-field" }, [element("span", { text: t("studio.name") }),
        element("input", { class: "studio-source-name", maxLength: 48, value: source.name, onchange: event => set({ name: event.target.value }) })]);
    container.append(name, geometry);
    if (crop) container.append(crop);
    if (source.kind === "text") {
        container.append(
            element("label", { class: "studio-field" }, [element("span", { text: t("studio.text") }),
                element("textarea", { class: "studio-text", maxLength: 280, rows: 3, value: source.text, onchange: event => set({ text: event.target.value }) })]),
            element("div", { class: "studio-grid" }, [
                element("label", { class: "studio-field" }, [element("span", { text: t("studio.color") }),
                    element("input", { type: "color", value: source.color, onchange: event => set({ color: event.target.value }) })]),
                numberField(t("studio.size"), source.size, value => set({ size: value }), { min: 8, max: 160 }),
                element("label", { class: "studio-field" }, [element("span", { text: t("studio.background") }),
                    element("input", { type: "color", value: source.background || "#000000", onchange: event => set({ background: event.target.value }) })]),
                element("button", { type: "button", class: "studio-no-background", text: t("studio.backgroundNone"), onclick: () => set({ background: "" }) }),
            ]));
    }
    const entry = source.capture ? session.registry.get(source.capture) : null;
    if (source.kind === "display" && entry?.audioTrack) {
        const toggle = element("input", { type: "checkbox", class: "studio-route-audio", checked: !!source.audio, onchange: event => set({ audio: event.target.checked }) });
        container.append(element("label", { class: "studio-check" }, [toggle, element("span", { text: t("studio.routeAudio") })]),
            element("p", { class: "set-hint", text: t("studio.routeAudioHelp") }));
    }
}

function renderSources(list, scene) {
    list.replaceChildren();
    if (!scene.sources.length) { list.append(element("li", { class: "set-hint studio-empty", text: t("studio.noSources") })); return; }
    // Top of the stack first.
    for (const source of [...scene.sources].reverse()) {
        const isCapture = source.kind === "display" || source.kind === "camera";
        const state = isCapture ? session.registry.state(source.capture) : "live";
        const selected = source.id === session.selectedSource;
        const row = element("li", { class: `studio-source${selected ? " selected" : ""}${source.visible ? "" : " hidden-source"}`, attrs: { "data-source": source.id, "data-state": state } });
        const select = element("button", { type: "button", class: "studio-source-select", attrs: { "aria-pressed": String(selected) },
            onclick: () => { session.selectedSource = source.id; schedule(); } }, [
            element("span", { class: "studio-source-name-label", text: source.name }),
            element("span", { class: "studio-source-kind", text: isCapture ? `${t(`studio.kind.${source.kind}`)} · ${t(`studio.state.${state}`)}` : t(`studio.kind.${source.kind}`) }),
        ]);
        row.append(...[select,
            iconButton("studio-raise", "chevron", t("studio.raise", { name: source.name }), () => apply(studio => moveSource(studio, scene.id, source.id, "up"))),
            iconButton("studio-lower", "chevron", t("studio.lower", { name: source.name }), () => apply(studio => moveSource(studio, scene.id, source.id, "down"))),
            iconButton("studio-visibility", source.visible ? "check" : "close", t(source.visible ? "studio.hide" : "studio.show", { name: source.name }),
                () => apply(studio => updateSource(studio, scene.id, source.id, { visible: !source.visible }))),
            isCapture && state !== "live" && state !== "muted" ? element("button", { type: "button", class: "studio-recapture", text: t("studio.recapture"), attrs: { "aria-label": t("studio.recaptureSource", { name: source.name }) }, onclick: () => capture(source.kind, { replace: source.id }) }) : null,
            iconButton("studio-remove", "trash", t("studio.remove", { name: source.name }), () => {
                if (session.selectedSource === source.id) session.selectedSource = "";
                apply(studio => removeSource(studio, scene.id, source.id), { prune: true });
            })].filter(Boolean));
        list.append(row);
    }
}

function render() {
    if (!panel || !session) return;
    const { $, overlay } = panel;
    const { studio, engine } = session;
    const scene = selectedScene(studio);
    const liveScene = engine.live;
    // Monitors: the engine's own canvases (draft preview, program output).
    const preview = $(".studio-preview"), program = $(".studio-program");
    if (engine.preview.parentNode !== preview) preview.append(engine.preview);
    if (engine.program.parentNode !== program) program.append(engine.program);
    engine.preview.setAttribute("role", "img");
    engine.preview.setAttribute("aria-label", t("studio.previewCanvas", { scene: scene.name }));
    engine.program.setAttribute("role", "img");
    engine.program.setAttribute("aria-label", t("studio.programCanvas"));
    const air = $(".studio-air");
    air.textContent = session.live ? t("studio.onAir") : t("studio.offAir");
    air.classList.toggle("live", session.live);
    $(".studio-transition").value = studio.transition.kind;
    $(".studio-duration").value = String(studio.transition.ms);
    $(".studio-duration").disabled = studio.transition.kind === "cut";
    // Scenes
    // Lists are rebuilt only when their content changed: a field committing
    // on blur (mousedown elsewhere) must not replace the button being clicked.
    const list = $(".studio-scene-list");
    const sceneKey = JSON.stringify([studio.selected, liveScene?.id || "", studio.scenes.map(item => [item.id, item.name])]);
    if (list.dataset.key !== sceneKey) {
        list.dataset.key = sceneKey;
        list.replaceChildren(...studio.scenes.map((item, index) => {
        const onAir = liveScene?.id === item.id;
        const label = t("studio.sceneSelect", { name: item.name, key: index + 1 });
        return element("li", {}, [element("button", { type: "button", class: `studio-scene${item.id === studio.selected ? " selected" : ""}${onAir ? " on-air" : ""}`,
            attrs: { "aria-pressed": String(item.id === studio.selected), "aria-label": onAir ? t("studio.sceneOnAir", { name: label }) : label, "data-scene": item.id },
            onclick: () => apply(next => selectScene(next, item.id)) }, [element("kbd", { text: String(index + 1) }), element("span", { text: item.name })])]);
    }));
    }
    const nameInput = $(".studio-scene-name");
    if (document.activeElement !== nameInput) nameInput.value = scene.name;
    $(".studio-add-scene").disabled = studio.scenes.length >= MAX_SCENES;
    $(".studio-remove-scene").disabled = studio.scenes.length <= 1;
    // Sources
    const full = scene.sources.length >= MAX_SOURCES;
    for (const button of [".studio-add-display", ".studio-add-camera", ".studio-add-image", ".studio-add-text"]) $(button).disabled = full;
    const reuse = $(".studio-reuse");
    const captures = session.registry.ids().map(id => session.registry.get(id)).filter(entry => entry?.state === "live" || entry?.state === "muted");
    reuse.replaceChildren(element("option", { value: "", text: t("studio.reuseNone") }),
        ...captures.map(entry => element("option", { value: entry.id, text: `${t(`studio.kind.${entry.kind}`)} · ${entry.label || entry.id}` })));
    reuse.disabled = full || !captures.length;
    $(".studio-restored").hidden = !session.restored || captures.length > 0;
    // Rows are rebuilt: keep keyboard focus on the same control of the same
    // source, else on the selected scene (shortcuts keep working).
    const hadFocus = panel.dialog.contains(document.activeElement);
    const focusedSource = document.activeElement?.closest?.("[data-source]")?.dataset.source;
    const focusedClass = [...(document.activeElement?.classList || [])].find(name => name.startsWith("studio-"));
    const sourceList = $(".studio-source-list");
    const sourceKey = JSON.stringify([scene.id, session.selectedSource, scene.sources.map(source => [source.id, source.name, source.visible,
        source.capture ? session.registry.state(source.capture) : ""])]);
    if (sourceList.dataset.key !== sourceKey) { sourceList.dataset.key = sourceKey; renderSources(sourceList, scene); }
    if (focusedSource) {
        const row = $(`[data-source="${focusedSource}"]`);
        (row?.querySelector(focusedClass ? `.${focusedClass}` : ".studio-source-select") || row?.querySelector(".studio-source-select"))?.focus();
    }
    if (hadFocus && !panel.dialog.contains(document.activeElement)) $(".studio-scene.selected")?.focus();
    const props = $(".studio-prop-body");
    if (!props.contains(document.activeElement)) renderProps(props, scene);
    // Footer
    $(".studio-ownership").textContent = session.live ? `${t("studio.closeLive")} ${t("studio.liveHint")}` : t("studio.closeIdle");
    $(".studio-golive").disabled = session.live || session.starting;
    $(".studio-stop").hidden = !session.live;
    $(".studio-release").hidden = session.live;
    overlay.dataset.live = String(session.live);
}

function startMeter() {
    clearInterval(session.meterTimer);
    const owner = session;
    owner.meterTimer = setInterval(() => {
        if (session !== owner || !panel) return;
        const level = Math.round((owner.audio?.level() || 0) * 100);
        const meter = panel.$(".studio-meter");
        meter.setAttribute("aria-valuenow", String(level));
        meter.firstElementChild.style.width = `${level}%`;
    }, 250);
}

// openStudio shows the panel; it never starts a capture by itself.
export function openStudio() {
    if (panel) { panel.dialog.querySelector(".studio-take")?.focus(); return; }
    if (!session) createSession();
    // Reopening a live Studio shows the draft again; captures released at
    // close need an explicit recapture.
    else session.engine.setDraft(selectedScene(session.studio), session.studio.scenes);
    panel = buildPanel();
    document.addEventListener("keydown", onKey);
    mountServerDialog(panel.overlay, { initialFocus: panel.dialog.querySelector(".studio-take"), onClose: reason => closePanel(reason) });
    startMeter();
    render();
}

function closePanel(reason) {
    if (!panel) return;
    panel = null;
    document.removeEventListener("keydown", onKey);
    if (!session) return;
    clearInterval(session.meterTimer);
    session.meterTimer = null;
    // Teardown reasons end everything; a user close keeps only a live program.
    // A publication still starting is treated like live: goLive settles it
    // (and disposes the session if it failed while the panel is closed).
    if ((session.live || session.starting) && !["server-change", "disconnected"].includes(reason)) session.engine.releaseDraft();
    else disposeSession();
}

// disposeStudio: account/server/channel switch, disconnect or voice teardown.
// The caller already stopped the published clone; this ends the session.
export function disposeStudio() {
    const overlay = panel?.overlay;
    disposeSession();
    if (overlay) closeDialog(overlay, "server-change");
    panel = null;
    document.removeEventListener("keydown", onKey);
}

export function studioActive() {
    return !!session;
}

// A deleted identity's open Studio ends (security-settings.js wipes its
// stored layout and images); nothing of it is saved again.
globalThis.addEventListener?.("noxa-identity-deleted", event => {
    if (session && event?.detail?.uid && session.uid === event.detail.uid) disposeStudio();
});

export function initStudio() {
    const bar = document.querySelector("#voice-bar .voice-buttons");
    if (!bar || document.getElementById("voice-studio")) return;
    const button = element("button", { type: "button", id: "voice-studio", class: "voice-control", onclick: () => openStudio() });
    labelButton(button, "streamLive", t("studio.open"));
    button.setAttribute("aria-label", t("studio.openHelp"));
    button.title = t("studio.openHelp");
    (bar.querySelector(".voice-secondary-actions") || bar).append(button);
}

// Test and diagnostics view of the current session (no media handles).
export function studioDiagnostics() {
    if (!session) return null;
    return {
        live: session.live, scene: session.studio.selected, program: session.engine.live?.id || "",
        captures: session.registry.ids().map(id => ({ id, refs: session.registry.refs(id), state: session.registry.state(id) })),
        routes: session.audio?.routed() || [], transitioning: session.engine.transitioning,
        width: session.width, height: session.height,
    };
}

// Test hook: the live session internals (engine canvases, registry).
export function studioSessionForTest() {
    return session;
}
