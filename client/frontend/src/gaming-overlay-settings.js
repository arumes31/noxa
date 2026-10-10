import { t } from "./i18n.js";
import "./gaming-overlay-settings.css";
import { mistAuroraPreview } from "./gaming-overlay-mist.js";
import { overlayColorPreferences, overlayBlueColor } from "./overlay-colors.js";

export function gamingOverlaySettings(settings, { row, checkbox, slider, hint }) {
    const panel = document.createElement("section"); panel.className = "gaming-overlay-settings";
    const available = window.go.main.App.GamingOverlayAvailable;
    const position = document.createElement("select");
    const monitor = document.createElement("select");
    const design = document.createElement("output"); design.textContent = t("overlay.default");
    settings.gaming_overlay_style = "default";
    const preferences = overlayColorPreferences(settings);
    settings.gaming_overlay_self_color = preferences.self;
    settings.gaming_overlay_blue_start = preferences.start;
    settings.gaming_overlay_blue_end = preferences.end;
    const ownColor = document.createElement("input"); ownColor.type = "color"; ownColor.value = preferences.self;
    ownColor.oninput = () => { settings.gaming_overlay_self_color = ownColor.value; render(); };
    const blueSlider = (field, min, max) => {
        const element = document.createElement("div"); element.className = "set-slider overlay-blue-slider";
        const input = document.createElement("input"); input.type = "range"; input.min = min; input.max = max;
        const swatch = document.createElement("span"); swatch.className = "overlay-color-chip"; swatch.setAttribute("aria-hidden", "true");
        const output = document.createElement("output");
        input.oninput = () => {
            settings[field] = Number(input.value);
            if (field === "gaming_overlay_blue_start") settings.gaming_overlay_blue_end = Math.max(settings.gaming_overlay_blue_end,settings[field]+20);
            else settings.gaming_overlay_blue_start = Math.min(settings.gaming_overlay_blue_start,settings[field]-20);
            render();
        };
        element.append(input,swatch,output);
        return {element,input,swatch,output,field};
    };
    const firstBlue = blueSlider("gaming_overlay_blue_start",180,220), lastBlue = blueSlider("gaming_overlay_blue_end",200,240);
    const option = (select, value, text) => { const item = document.createElement("option"); item.value = value; item.textContent = text; select.append(item); };
    for (const corner of ["center-left", "top-left", "top-right", "bottom-left", "bottom-right", "custom"]) option(position, corner, t("overlay." + corner));
    position.value = settings.gaming_overlay_position || "center-left";
    option(monitor, "", t("overlay.primary"));
    const savedMonitor = settings.gaming_overlay_monitor || "";
    if (savedMonitor) option(monitor, savedMonitor, savedMonitor);
    monitor.value = savedMonitor;
    const preview = document.createElement("div"); preview.className = "overlay-position-preview";
    const sample = document.createElement("button"); sample.type = "button"; sample.className = "overlay-position-sample";
    sample.setAttribute("aria-label", t("overlay.drag"));
    for (const name of ["Alex", "Sam"]) {
        const person = document.createElement("span"); person.className = "overlay-sample-person";
        const avatar = document.createElement("span"); avatar.className = "overlay-sample-avatar"; avatar.textContent = name[0];
        const details = document.createElement("span"); details.className = "overlay-sample-details";
        const label = document.createElement("span"); label.className = "overlay-sample-name"; label.textContent = name; details.append(label);
        person.append(avatar, details); sample.append(person);
    }
    preview.append(sample);
    const showDesign = mistAuroraPreview(sample);
    const coordinates = document.createElement("output"); coordinates.className = "overlay-position-readout"; coordinates.setAttribute("aria-live", "polite");
    const render = () => {
        sample.querySelectorAll(".overlay-sample-person").forEach((person,index) =>
            person.style.setProperty("--overlay-color", index === 0 ? settings.gaming_overlay_self_color : overlayBlueColor(settings.gaming_overlay_blue_start)));
        for (const control of [firstBlue,lastBlue]) {
            const hue = settings[control.field];
            control.input.value = hue; control.input.setAttribute("aria-valuetext", `${hue}°`);
            control.output.textContent = `${hue}°`; control.swatch.style.background = overlayBlueColor(hue);
        }
        const custom = position.value === "custom";
        const x = custom ? settings.gaming_overlay_x || 0 : position.value.endsWith("right") ? 100 : 0;
        const y = custom ? settings.gaming_overlay_y || 0 : position.value.startsWith("bottom") ? 100 : position.value === "center-left" ? 45 : 0;
        const translateY = position.value === "center-left" ? 50 : y;
        sample.style.left = `${x}%`; sample.style.top = `${y}%`; sample.style.transform = `translate(-${x}%, -${translateY}%)`;
        sample.style.opacity = String((settings.gaming_overlay_opacity ?? 88) / 100);
        const scale = (settings.gaming_overlay_scale ?? 80) / 100;
        sample.style.setProperty("--overlay-scale", String(scale));
        sample.style.fontSize = `${16 * scale}px`;
        sample.classList.toggle("overlay-motion-disabled", settings.gaming_overlay_animate === false);
        preview.style.height = `${Math.max(270, 256 * scale + 20)}px`;
        showDesign(settings.gaming_overlay_animate !== false);
        coordinates.textContent = t("overlay.coordinates", { x, y });
    };
    const setPosition = (x, y) => {
        settings.gaming_overlay_position = position.value = "custom";
        settings.gaming_overlay_x = Math.round(Math.min(100, Math.max(0, x)));
        settings.gaming_overlay_y = Math.round(Math.min(100, Math.max(0, y)));
        render();
    };
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
    sample.onpointerup = sample.onpointercancel = sample.onlostpointercapture = () => { drag = null; };
    sample.onkeydown = event => {
        if (!["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(event.key)) return;
        event.preventDefault();
        const x = position.value === "custom" ? settings.gaming_overlay_x || 0 : position.value.endsWith("right") ? 100 : 0;
        const y = position.value === "custom" ? settings.gaming_overlay_y || 0 : position.value.startsWith("bottom") ? 100 : position.value === "center-left" ? 45 : 0;
        const step = event.shiftKey ? 10 : 1;
        setPosition(x + (event.key === "ArrowLeft" ? -step : event.key === "ArrowRight" ? step : 0), y + (event.key === "ArrowUp" ? -step : event.key === "ArrowDown" ? step : 0));
    };
    position.onchange = () => { settings.gaming_overlay_position = position.value; render(); };
    monitor.onchange = () => { settings.gaming_overlay_monitor = monitor.value; };
    panel.append(row(t("overlay.enabled"), checkbox(settings.gaming_overlay !== false, value => { settings.gaming_overlay = value; }), "saved"),
        row(t("overlay.monitor"), monitor, "saved"), row(t("overlay.position"), position, "saved"),
        row(t("overlay.design"), design, "saved"),
        row(t("overlay.selfColor"), ownColor, "saved"),
        row(t("overlay.firstBlue"), firstBlue.element, "saved"), row(t("overlay.lastBlue"), lastBlue.element, "saved"),
        hint(t("overlay.colorsHint")),
        row(t("overlay.scale"), slider(settings.gaming_overlay_scale ?? 80, 75, 200, value => { settings.gaming_overlay_scale = value; render(); }, true), "saved"),
        row(t("overlay.opacity"), slider(settings.gaming_overlay_opacity ?? 88, 20, 100, value => { settings.gaming_overlay_opacity = value; render(); }, true), "saved"),
        row(t("overlay.animate"), checkbox(settings.gaming_overlay_animate !== false, value => { settings.gaming_overlay_animate = value; render(); }), "saved"),
        hint(t("overlay.animateHint")),
        preview, coordinates, hint(t("overlay.dragHelp")));
    const nativePreview = document.createElement("button"); nativePreview.type = "button"; nativePreview.textContent = t("overlay.preview");
    const status = hint(t("overlay.hint")); status.setAttribute("role", "status");
    nativePreview.onclick = async () => {
        if (nativePreview.disabled) return;
        nativePreview.disabled = true;
        const draft = Object.fromEntries(Object.entries(settings).filter(([key]) => key.startsWith("gaming_overlay")));
        try {
            const error = await window.go.main.App.PreviewGamingOverlay(draft);
            if (error) throw new Error(error);
            status.textContent = t("overlay.previewShown");
        } catch (error) { status.textContent = t("overlay.failed", { error: error.message || String(error) }); }
        finally { if (panel.isConnected) nativePreview.disabled = false; }
    };
    panel.append(nativePreview, status);
    render();
    if (available) void available().then(supported => {
        if (supported !== false || !panel.isConnected) return;
        for (const control of panel.querySelectorAll("input, select, button")) control.disabled = true;
        status.textContent = t("overlay.unavailable");
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
