// Settings › Fullscreen overlay page.
//
// Edits the existing overlay preferences (gaming_overlay_*). The preview
// sample reuses the overlay's own light halo + comb preview untouched; the game
// frame, no-go zones and anchors around it are settings-only placement aids
// (see overlay-zones.js). Nothing here changes how the native overlay renders.
import { t } from "./i18n.js";
import "./gaming-overlay-settings.css";
import { lightHaloPreview } from "./gaming-overlay-animation.js";
import { overlayColorPreferences, overlayBlueColor } from "./overlay-colors.js";
import {
    GAME_UI_PRESET_IDS, OVERLAY_ANCHORS, anchorAvailability, anchorOf, anchorPlacement, gameUIPreset,
    nearestFreeAnchor, nominalOverlayBox, placementAllowed, placementOf, placementRect, presetZones, zonesHit,
} from "./overlay-zones.js";

const SVG_NS = "http://www.w3.org/2000/svg";

// A static night-time game scene for the preview frame. No animation.
function gameScene() {
    const svg = document.createElementNS(SVG_NS, "svg");
    svg.setAttribute("viewBox", "0 0 960 540");
    svg.setAttribute("preserveAspectRatio", "xMidYMid slice");
    svg.setAttribute("aria-hidden", "true");
    svg.classList.add("ovl-scene");
    const stars = [[90, 60], [180, 120], [260, 40], [340, 150], [420, 70], [610, 50], [700, 130], [780, 80], [860, 160], [520, 140], [140, 190], [900, 40]]
        .map(([x, y], i) => `<circle cx="${x}" cy="${y}" r="${i % 3 ? 1.2 : 1.8}" fill="#cfe9ff" opacity="${i % 2 ? .55 : .85}"/>`).join("");
    svg.innerHTML = `
        <defs>
            <linearGradient id="ovl-sky" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0" stop-color="#0b1a2c"/><stop offset=".55" stop-color="#1c3550"/><stop offset="1" stop-color="#2a4560"/>
            </linearGradient>
            <radialGradient id="ovl-moon" cx=".5" cy=".5" r=".5">
                <stop offset="0" stop-color="#e9f6ff"/><stop offset=".55" stop-color="#cbe8ff"/><stop offset="1" stop-color="#cbe8ff" stop-opacity="0"/>
            </radialGradient>
            <linearGradient id="ovl-road" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0" stop-color="#22374d"/><stop offset="1" stop-color="#121f2e"/>
            </linearGradient>
        </defs>
        <rect width="960" height="540" fill="url(#ovl-sky)"/>
        ${stars}
        <path d="M0 90 C 200 70, 420 120, 960 80 L960 110 C 520 150, 260 100, 0 125Z" fill="#9fe3ff" opacity=".06"/>
        <circle cx="610" cy="210" r="70" fill="url(#ovl-moon)" opacity=".55"/>
        <circle cx="610" cy="210" r="38" fill="#dff1ff"/>
        <path d="M0 360 L130 250 L220 320 L330 220 L450 330 L560 260 L640 330 L760 240 L880 320 L960 280 L960 540 L0 540Z" fill="#14263a"/>
        <path d="M150 360 v-80 h14 v-22 h10 v22 h12 v-40 h8 v-18 h6 v18 h8 v40 h12 v-22 h10 v22 h14 v80Z" fill="#0c1826"/>
        <path d="M0 420 L240 370 L480 400 L720 360 L960 410 L960 540 L0 540Z" fill="#0f1d2c"/>
        <path d="M420 540 L470 400 L490 400 L560 540Z" fill="url(#ovl-road)"/>
        <path d="M505 470 l40 -60" stroke="#8fd8ff" stroke-width="5" stroke-linecap="round" opacity=".85"/>
        <path d="M480 520 l14 -40 l14 40Z" fill="#0a1420"/>
        ${[[300, 430], [700, 425], [760, 440]].map(([x, y]) => `<path d="M${x - 12} ${y} l12 -22 l12 22Z" fill="#0a1420"/><circle cx="${x}" cy="${y - 26}" r="2" fill="#ff8f8f" opacity=".7"/>`).join("")}`;
    return svg;
}

export function gamingOverlaySettings(settings, { row, checkbox, slider, hint, group, page }) {
    const available = window.go.main.App.GamingOverlayAvailable;
    const option = (select, value, text) => { const item = document.createElement("option"); item.value = value; item.textContent = text; select.append(item); };

    // The only design is the noXa light halo + comb; legacy styles migrate.
    settings.gaming_overlay_style = "default";
    const preferences = overlayColorPreferences(settings);
    settings.gaming_overlay_self_color = preferences.self;
    settings.gaming_overlay_blue_start = preferences.start;
    settings.gaming_overlay_blue_end = preferences.end;
    settings.gaming_overlay_game_ui = gameUIPreset(settings.gaming_overlay_game_ui);
    const avoidZones = () => settings.gaming_overlay_avoid_zones !== false;
    const onlySpeaking = () => settings.gaming_overlay_only_speaking !== false;
    const chatPeek = () => settings.gaming_overlay_chat_peek === true;

    // --- preview frame ---------------------------------------------------------
    const preview = document.createElement("div");
    preview.className = "overlay-position-preview ovl-stage";
    preview.append(gameScene());
    const zoneLayer = document.createElement("div"); zoneLayer.className = "ovl-zones"; zoneLayer.setAttribute("aria-hidden", "true");
    const handleLayer = document.createElement("div"); handleLayer.className = "ovl-handles";
    preview.append(zoneLayer, handleLayer);
    const sample = document.createElement("button"); sample.type = "button"; sample.className = "overlay-position-sample";
    sample.setAttribute("aria-label", t("overlay.drag"));
    // Alex and Sam are speaking; Robin is a quiet member, shown only with
    // "Only show who's speaking" off. The peek line mirrors chat peek.
    for (const name of ["Alex", "Sam", "Robin"]) {
        const person = document.createElement("span"); person.className = "overlay-sample-person";
        if (name === "Robin") person.classList.add("overlay-sample-quiet");
        person.dataset.overlayId = `preview-${name.toLowerCase()}`;
        const avatar = document.createElement("span"); avatar.className = "overlay-sample-avatar"; avatar.textContent = name[0];
        const details = document.createElement("span"); details.className = "overlay-sample-details";
        const label = document.createElement("span"); label.className = "overlay-sample-name"; label.textContent = name; details.append(label);
        person.append(avatar, details); sample.append(person);
    }
    const quietSample = sample.querySelector(".overlay-sample-quiet");
    const peekSample = document.createElement("span"); peekSample.className = "overlay-sample-peek";
    const peekName = document.createElement("span"); peekName.className = "overlay-sample-peek-name"; peekName.textContent = "Sam";
    const peekText = document.createElement("span"); peekText.className = "overlay-sample-peek-text"; peekText.textContent = t("overlay.peekSample");
    peekSample.append(peekName, peekText); sample.append(peekSample);
    preview.append(sample);
    const showDesign = lightHaloPreview(sample);
    const coordinates = document.createElement("output"); coordinates.className = "overlay-position-readout"; coordinates.setAttribute("aria-live", "polite");
    const status = hint(""); status.classList.add("ovl-status"); status.setAttribute("role", "status");
    const announce = text => { status.textContent = text; };

    // --- anchor picker ---------------------------------------------------------
    const anchorLabelId = "ovl-anchor-label";
    const anchorHead = document.createElement("div"); anchorHead.className = "ovl-side-label"; anchorHead.id = anchorLabelId;
    anchorHead.textContent = t("overlay.anchor");
    const picker = document.createElement("div"); picker.className = "ovl-anchor-grid";
    picker.setAttribute("role", "radiogroup"); picker.setAttribute("aria-labelledby", anchorLabelId);
    const cells = new Map();
    for (let index = 0; index < 9; index++) {
        const anchor = OVERLAY_ANCHORS.find(item => item.row * 3 + item.col === index);
        if (!anchor) {
            const centre = document.createElement("span"); centre.className = "ovl-anchor-center"; centre.setAttribute("aria-hidden", "true");
            picker.append(centre);
            continue;
        }
        const cell = document.createElement("button"); cell.type = "button"; cell.className = "ovl-anchor";
        cell.setAttribute("role", "radio"); cell.dataset.anchor = anchor.id;
        cell.onclick = () => pickAnchor(anchor.id);
        picker.append(cell); cells.set(anchor.id, cell);
    }
    picker.addEventListener("keydown", event => {
        const current = event.target.closest?.(".ovl-anchor");
        if (!current) return;
        const moves = { ArrowLeft: [0, -1], ArrowRight: [0, 1], ArrowUp: [-1, 0], ArrowDown: [1, 0] };
        if (event.key === " " || event.key === "Enter") { event.preventDefault(); pickAnchor(current.dataset.anchor); return; }
        if (!moves[event.key]) return;
        event.preventDefault();
        const from = OVERLAY_ANCHORS.find(item => item.id === current.dataset.anchor);
        let [r, c] = [from.row, from.col];
        for (let step = 0; step < 3; step++) {
            r = (r + moves[event.key][0] + 3) % 3; c = (c + moves[event.key][1] + 3) % 3;
            const next = OVERLAY_ANCHORS.find(item => item.row === r && item.col === c);
            if (next) { cells.get(next.id).focus(); break; }
        }
    });
    const anchorName = document.createElement("strong"); anchorName.className = "ovl-anchor-name";
    const anchorFree = document.createElement("span"); anchorFree.className = "ovl-anchor-free";
    const anchorSummary = document.createElement("div"); anchorSummary.className = "ovl-anchor-summary";
    anchorSummary.append(anchorName, anchorFree);
    const anchorBlock = document.createElement("div"); anchorBlock.className = "ovl-side-block";
    const anchorRow = document.createElement("div"); anchorRow.className = "ovl-anchor-row";
    anchorRow.append(picker, anchorSummary);
    anchorBlock.append(anchorHead, anchorRow);

    // --- side controls -----------------------------------------------------------
    const sideField = (labelText, control, id) => {
        const block = document.createElement("div"); block.className = "ovl-side-block";
        const label = document.createElement("label"); label.className = "ovl-side-label"; label.textContent = labelText;
        const target = control.matches("select, input") ? control : control.querySelector('input[type="range"], select');
        target.id = id; label.htmlFor = id;
        block.append(label, control);
        return block;
    };
    const talk = document.createElement("select"); talk.className = "ovl-select";
    option(talk, "halo", t("overlay.talk.halo"));
    option(talk, "still", t("overlay.talk.still"));
    talk.value = settings.gaming_overlay_animate === false ? "still" : "halo";
    talk.onchange = () => { settings.gaming_overlay_animate = talk.value !== "still"; render(); };
    const opacity = slider(settings.gaming_overlay_opacity ?? 88, 20, 100, value => { settings.gaming_overlay_opacity = value; render(); }, true);
    opacity.querySelector(".audio-percent")?.setAttribute("aria-label", t("overlay.opacity") + " (%)");
    const preset = document.createElement("select"); preset.className = "ovl-select";
    for (const id of GAME_UI_PRESET_IDS) option(preset, id, t("overlay.preset." + id));
    preset.value = settings.gaming_overlay_game_ui;
    preset.onchange = () => { settings.gaming_overlay_game_ui = gameUIPreset(preset.value); keepClearOfZones(); render(); };
    const side = document.createElement("div"); side.className = "ovl-side";
    side.append(anchorBlock,
        sideField(t("overlay.talkAnimation"), talk, "ovl-talk"),
        sideField(t("overlay.opacity"), opacity, "ovl-opacity"),
        sideField(t("overlay.gameUI"), preset, "ovl-preset"));

    const layout = document.createElement("div"); layout.className = "ovl-layout";
    const stageWrap = document.createElement("div"); stageWrap.className = "ovl-stage-wrap";
    stageWrap.append(preview, coordinates);
    layout.append(stageWrap, side);

    // --- placement ----------------------------------------------------------------
    const box = () => {
        const frame = preview.getBoundingClientRect(), item = sample.getBoundingClientRect();
        if (frame.width > 0 && item.width > 0) return { w: Math.min(100, item.width / frame.width * 100), h: Math.min(100, item.height / frame.height * 100) };
        return nominalOverlayBox(settings.gaming_overlay_scale ?? 80);
    };
    const applyPlacement = placement => {
        settings.gaming_overlay_position = placement.position;
        if (placement.position === "custom") {
            settings.gaming_overlay_x = placement.x;
            settings.gaming_overlay_y = placement.y;
        }
    };
    const zoneNames = ids => ids.map(id => t("overlay.zone." + id)).join(", ");
    function pickAnchor(id) {
        const blocked = anchorAvailability(settings.gaming_overlay_game_ui, box()).find(item => item.id === id);
        if (blocked?.blocked && avoidZones()) {
            announce(t("overlay.anchorBlocked", { anchor: t("overlay.anchor." + id), zones: zoneNames(blocked.blockedBy) }));
            return;
        }
        applyPlacement(anchorPlacement(id));
        announce(t("overlay.anchorChosen", { anchor: t("overlay.anchor." + id) }));
        render();
        sample.dispatchEvent(new Event("change", { bubbles: true }));
    }
    // keepClearOfZones moves the overlay to the nearest free anchor when the
    // current placement lands in a no-go zone (preset or avoid toggle changed).
    function keepClearOfZones() {
        const placement = placementOf(settings);
        if (placementAllowed(placement, settings.gaming_overlay_game_ui, box(), avoidZones())) return;
        const next = nearestFreeAnchor(placement, settings.gaming_overlay_game_ui, box());
        if (!next) { announce(t("overlay.noFreeAnchor")); return; }
        applyPlacement(anchorPlacement(next));
        announce(t("overlay.movedClear", { anchor: t("overlay.anchor." + next) }));
    }
    const setPosition = (x, y) => {
        applyPlacement({ position: "custom", x: Math.round(Math.min(100, Math.max(0, x))), y: Math.round(Math.min(100, Math.max(0, y))) });
        render();
    };

    // --- render -------------------------------------------------------------------
    const render = () => {
        sample.querySelectorAll(".overlay-sample-person").forEach((person, index) =>
            person.style.setProperty("--overlay-color", index === 0 ? settings.gaming_overlay_self_color
                : overlayBlueColor(person === quietSample ? settings.gaming_overlay_blue_end : settings.gaming_overlay_blue_start)));
        quietSample.hidden = onlySpeaking();
        peekSample.hidden = !chatPeek();
        for (const control of [firstBlue, lastBlue]) {
            const hue = settings[control.field];
            control.input.value = hue; control.input.setAttribute("aria-valuetext", `${hue}°`);
            control.output.textContent = `${hue}°`; control.swatch.style.background = overlayBlueColor(hue);
        }
        const placement = placementOf(settings);
        const custom = placement.position === "custom";
        const x = custom ? placement.x : placement.position.endsWith("right") ? 100 : 0;
        const y = custom ? placement.y : placement.position.startsWith("bottom") ? 100 : placement.position === "center-left" ? 45 : 0;
        const translateY = placement.position === "center-left" ? 50 : y;
        sample.style.left = `${x}%`; sample.style.top = `${y}%`; sample.style.transform = `translate(-${x}%, -${translateY}%)`;
        sample.style.opacity = String((settings.gaming_overlay_opacity ?? 88) / 100);
        // The frame models a PREVIEW_SCREEN-sized desktop, so the sample keeps
        // the overlay's proportions at the chosen size.
        const frameWidth = preview.clientWidth || 560;
        const scale = (settings.gaming_overlay_scale ?? 80) / 100 * frameWidth / 960;
        sample.style.setProperty("--overlay-scale", String(scale));
        sample.style.fontSize = `${16 * scale}px`;
        sample.classList.toggle("overlay-motion-disabled", settings.gaming_overlay_animate === false);
        talk.value = settings.gaming_overlay_animate === false ? "still" : "halo";
        showDesign(settings.gaming_overlay_animate !== false);
        coordinates.textContent = t("overlay.coordinates", { x, y });

        // Zones, anchors and the picker.
        const presetId = settings.gaming_overlay_game_ui;
        const zones = presetZones(presetId);
        const currentBox = box();
        const hit = new Set(zonesHit(placementRect(placement, currentBox), zones).map(item => item.id));
        zoneLayer.replaceChildren(...zones.map(item => {
            const el = document.createElement("div"); el.className = "ovl-zone";
            el.classList.toggle("is-hit", hit.has(item.id));
            Object.assign(el.style, { left: `${item.x}%`, top: `${item.y}%`, width: `${item.w}%`, height: `${item.h}%` });
            const name = document.createElement("span"); name.textContent = t("overlay.zone." + item.id); el.append(name);
            return el;
        }));
        preview.classList.toggle("is-blocked", hit.size > 0 && avoidZones());
        const availability = anchorAvailability(presetId, currentBox);
        const active = anchorOf(placement);
        const free = availability.filter(item => !item.blocked).length;
        handleLayer.replaceChildren(...availability.map(item => {
            const rect = placementRect(anchorPlacement(item.id), currentBox);
            const handle = document.createElement("span"); handle.className = "ovl-handle";
            handle.setAttribute("aria-hidden", "true");
            handle.classList.toggle("is-blocked", item.blocked);
            handle.classList.toggle("is-on", item.id === active);
            const anchor = OVERLAY_ANCHORS.find(a => a.id === item.id);
            Object.assign(handle.style, {
                left: `${anchor.col === 0 ? rect.x + 1.5 : anchor.col === 2 ? rect.x + rect.w - 1.5 : rect.x + rect.w / 2}%`,
                top: `${anchor.row === 0 ? rect.y + 3 : anchor.row === 2 ? rect.y + rect.h - 3 : rect.y + rect.h / 2}%`,
            });
            handle.onclick = () => pickAnchor(item.id);
            return handle;
        }));
        for (const item of availability) {
            const cell = cells.get(item.id);
            const on = item.id === active;
            cell.classList.toggle("is-on", on);
            cell.classList.toggle("is-blocked", item.blocked);
            cell.setAttribute("aria-checked", String(on));
            cell.setAttribute("aria-disabled", String(item.blocked && avoidZones()));
            const name = t("overlay.anchor." + item.id);
            const label = item.blocked ? t("overlay.anchorBlockedLabel", { anchor: name, zones: zoneNames(item.blockedBy) }) : name;
            cell.setAttribute("aria-label", label); cell.title = label;
        }
        // Roving tab stop: the chosen anchor, else the first free one.
        const focusable = active || availability.find(item => !item.blocked)?.id || OVERLAY_ANCHORS[0].id;
        for (const [id, cell] of cells) cell.tabIndex = id === focusable ? 0 : -1;
        anchorName.textContent = active ? t("overlay.anchor." + active) : t("overlay.custom");
        anchorFree.textContent = presetId === "none" ? t("overlay.anchorsAllFree")
            : t("overlay.anchorsFree", { free, preset: t("overlay.presetShort." + presetId) });
    };

    // --- dragging -------------------------------------------------------------------
    let drag = null;
    sample.onpointerdown = event => {
        if (event.button !== 0) return;
        const bounds = sample.getBoundingClientRect(); drag = { x: event.clientX - bounds.left, y: event.clientY - bounds.top };
        sample.setPointerCapture(event.pointerId); event.preventDefault(); sample.focus();
    };
    sample.onpointermove = event => {
        if (!drag) return;
        const bounds = preview.getBoundingClientRect(), item = sample.getBoundingClientRect();
        setPosition((event.clientX - bounds.left - drag.x) / Math.max(1, bounds.width - item.width) * 100,
            (event.clientY - bounds.top - drag.y) / Math.max(1, bounds.height - item.height) * 100);
    };
    const endDrag = () => {
        if (!drag) return;
        drag = null;
        const placement = placementOf(settings);
        if (placementAllowed(placement, settings.gaming_overlay_game_ui, box(), avoidZones())) return;
        const zones = zonesHit(placementRect(placement, box()), presetZones(settings.gaming_overlay_game_ui)).map(item => item.id);
        const next = nearestFreeAnchor(placement, settings.gaming_overlay_game_ui, box());
        if (!next) { announce(t("overlay.noFreeAnchor")); return; }
        applyPlacement(anchorPlacement(next));
        announce(t("overlay.snapped", { anchor: t("overlay.anchor." + next), zones: zoneNames(zones) }));
        render();
    };
    sample.onpointerup = sample.onpointercancel = sample.onlostpointercapture = endDrag;
    sample.onkeydown = event => {
        if (!["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(event.key)) return;
        event.preventDefault();
        const placement = placementOf(settings);
        let x = placement.position === "custom" ? placement.x : placement.position.endsWith("right") ? 100 : 0;
        let y = placement.position === "custom" ? placement.y : placement.position.startsWith("bottom") ? 100 : placement.position === "center-left" ? 45 : 0;
        const step = event.shiftKey ? 10 : 1;
        const dx = event.key === "ArrowLeft" ? -step : event.key === "ArrowRight" ? step : 0;
        const dy = event.key === "ArrowUp" ? -step : event.key === "ArrowDown" ? step : 0;
        // With zone avoidance on, keep stepping until the overlay is clear of
        // every zone, so keys jump across a zone instead of entering it.
        for (let tries = 0; tries <= 100; tries++) {
            x = Math.min(100, Math.max(0, x + dx)); y = Math.min(100, Math.max(0, y + dy));
            const next = { position: "custom", x, y };
            if (placementAllowed(next, settings.gaming_overlay_game_ui, box(), avoidZones())) { setPosition(x, y); return; }
            if ((x === 0 || x === 100) && dx || (y === 0 || y === 100) && dy) break;
        }
        announce(t("overlay.keyBlocked"));
    };

    // --- toggles -------------------------------------------------------------------
    // The in-game overlay applies these; the preview shows quiet members and
    // a peek line. Auto-hide has no static preview: it reacts to the mouse.
    const behaviour = group(t("overlay.behaviour"), "behaviour",
        row(t("overlay.onlySpeaking"), checkbox(onlySpeaking(), value => { settings.gaming_overlay_only_speaking = value; render(); }), "",
            { key: "gaming_overlay_only_speaking", help: t("overlay.onlySpeakingHelp") }),
        row(t("overlay.chatPeek"), checkbox(chatPeek(), value => { settings.gaming_overlay_chat_peek = value; render(); }), "",
            { key: "gaming_overlay_chat_peek", help: t("overlay.chatPeekHelp") }),
        row(t("overlay.autoHide"), checkbox(settings.gaming_overlay_auto_hide === true, value => { settings.gaming_overlay_auto_hide = value; }), "",
            { key: "gaming_overlay_auto_hide", help: t("overlay.autoHideHelp") }),
        row(t("overlay.avoidZones"), checkbox(avoidZones(), value => { settings.gaming_overlay_avoid_zones = value; keepClearOfZones(); render(); }), "",
            { key: "gaming_overlay_avoid_zones", help: t("overlay.avoidZonesHelp") }));

    // --- general overlay options (pre-existing preferences) -------------------------
    const monitor = document.createElement("select");
    option(monitor, "", t("overlay.primary"));
    const savedMonitor = settings.gaming_overlay_monitor || "";
    if (savedMonitor) option(monitor, savedMonitor, savedMonitor);
    monitor.value = savedMonitor;
    monitor.onchange = () => { settings.gaming_overlay_monitor = monitor.value; };
    const ownColor = document.createElement("input"); ownColor.type = "color"; ownColor.value = preferences.self;
    ownColor.oninput = () => { settings.gaming_overlay_self_color = ownColor.value; render(); };
    const blueSlider = (field, min, max) => {
        const element = document.createElement("div"); element.className = "set-slider overlay-blue-slider";
        const input = document.createElement("input"); input.type = "range"; input.min = min; input.max = max;
        const swatch = document.createElement("span"); swatch.className = "overlay-color-chip"; swatch.setAttribute("aria-hidden", "true");
        const output = document.createElement("output");
        input.oninput = () => {
            settings[field] = Number(input.value);
            if (field === "gaming_overlay_blue_start") settings.gaming_overlay_blue_end = Math.max(settings.gaming_overlay_blue_end, settings[field] + 20);
            else settings.gaming_overlay_blue_start = Math.min(settings.gaming_overlay_blue_start, settings[field] - 20);
            render();
        };
        element.append(input, swatch, output);
        return { element, input, swatch, output, field };
    };
    const firstBlue = blueSlider("gaming_overlay_blue_start", 180, 220), lastBlue = blueSlider("gaming_overlay_blue_end", 200, 240);
    const general = group(t("overlay.general"), "general",
        row(t("overlay.enabled"), checkbox(settings.gaming_overlay !== false, value => { settings.gaming_overlay = value; }), "", { key: "gaming_overlay" }),
        row(t("overlay.monitor"), monitor, "", { key: "gaming_overlay_monitor" }),
        row(t("overlay.scale"), slider(settings.gaming_overlay_scale ?? 80, 75, 200, value => { settings.gaming_overlay_scale = value; render(); }, true), "", { key: "gaming_overlay_scale" }),
        row(t("overlay.selfColor"), ownColor, "", { key: "gaming_overlay_self_color" }),
        row(t("overlay.firstBlue"), firstBlue.element, "", { key: "gaming_overlay_blue_start" }),
        row(t("overlay.lastBlue"), lastBlue.element, "", { key: "gaming_overlay_blue_end", help: t("overlay.colorsHint") }),
        hint(t("settings.timing.saved") + " " + t("overlay.hint")));

    const placementGroup = group("", "placement", layout, status);
    placementGroup.dataset.setting = "gaming_overlay_position";
    const panel = page(placementGroup, behaviour, general);
    panel.classList.add("gaming-overlay-settings");

    // --- native preview ("Preview in game") ------------------------------------------
    const nativePreview = document.createElement("button"); nativePreview.type = "button"; nativePreview.textContent = t("overlay.preview");
    nativePreview.className = "ovl-preview-button";
    nativePreview.onclick = async () => {
        if (nativePreview.disabled) return;
        nativePreview.disabled = true;
        const draft = Object.fromEntries(Object.entries(settings).filter(([key]) => key.startsWith("gaming_overlay")));
        try {
            const error = await window.go.main.App.PreviewGamingOverlay(draft);
            if (error) throw new Error(error);
            announce(t("overlay.previewShown"));
        } catch (error) { announce(t("overlay.failed", { error: error.message || String(error) })); }
        finally { if (panel.isConnected && !panel.dataset.unavailable) nativePreview.disabled = false; }
    };
    panel.footerActions = [nativePreview];

    render();
    // Layout is only known once the page is in the dialog; re-render then and
    // whenever the frame is resized so zones, anchors and sample stay in step.
    if (typeof ResizeObserver === "function") {
        let width = 0;
        const observer = new ResizeObserver(() => {
            if (!panel.isConnected) { observer.disconnect(); return; }
            if (preview.clientWidth === width) return;
            width = preview.clientWidth;
            render();
        });
        observer.observe(preview);
    }
    if (available) void available().then(supported => {
        if (supported !== false || !panel.isConnected) return;
        panel.dataset.unavailable = "true";
        for (const control of [...panel.querySelectorAll("input, select, button"), nativePreview]) control.disabled = true;
        announce(t("overlay.unavailable"));
    }).catch(() => {});
    if (window.go.main.App.GetGamingOverlayMonitors) void window.go.main.App.GetGamingOverlayMonitors().then(monitors => {
        if (!panel.isConnected || !Array.isArray(monitors)) return;
        monitor.replaceChildren(); option(monitor, "", t("overlay.primary"));
        for (const display of monitors) option(monitor, display.id, `${display.name} · ${display.width} × ${display.height}${display.primary ? ` · ${t("overlay.primary")}` : ""}`);
        if (savedMonitor && !monitors.some(display => display.id === savedMonitor)) option(monitor, savedMonitor, t("overlay.missingMonitor", { name: savedMonitor }));
        monitor.value = settings.gaming_overlay_monitor || "";
    }).catch(() => {});
    return panel;
}
