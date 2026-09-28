import { t } from "./i18n.js";
import "./gaming-overlay-settings.css";

export function gamingOverlaySettings(settings, { row, checkbox, slider, hint }) {
    const panel = document.createElement("section"); panel.className = "gaming-overlay-settings";
    const available = window.go.main.App.GamingOverlayAvailable;
    const position = document.createElement("select");
    const monitor = document.createElement("select");
    const option = (select, value, text) => { const item = document.createElement("option"); item.value = value; item.textContent = text; select.append(item); };
    for (const corner of ["top-left", "top-right", "bottom-left", "bottom-right", "custom"]) option(position, corner, t("overlay." + corner));
    position.value = settings.gaming_overlay_position || "top-right";
    option(monitor, "", t("overlay.primary"));
    const savedMonitor = settings.gaming_overlay_monitor || "";
    if (savedMonitor) option(monitor, savedMonitor, savedMonitor);
    monitor.value = savedMonitor;
    const preview = document.createElement("div"); preview.className = "overlay-position-preview";
    const sample = document.createElement("button"); sample.type = "button"; sample.className = "overlay-position-sample";
    sample.setAttribute("aria-label", t("overlay.drag"));
    sample.textContent = t("overlay.sample"); preview.append(sample);
    const coordinates = document.createElement("output"); coordinates.className = "overlay-position-readout"; coordinates.setAttribute("aria-live", "polite");
    const render = () => {
        const custom = position.value === "custom";
        const x = custom ? settings.gaming_overlay_x || 0 : position.value.endsWith("right") ? 100 : 0;
        const y = custom ? settings.gaming_overlay_y || 0 : position.value.startsWith("bottom") ? 100 : 0;
        sample.style.left = `${x}%`; sample.style.top = `${y}%`; sample.style.transform = `translate(-${x}%, -${y}%)`;
        sample.style.opacity = String((settings.gaming_overlay_opacity ?? 88) / 100);
        sample.style.fontSize = `${(settings.gaming_overlay_scale ?? 100) / 100}em`;
        sample.textContent = t(settings.gaming_overlay_speakers_only ? "overlay.sampleSpeaking" : "overlay.sample");
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
        const y = position.value === "custom" ? settings.gaming_overlay_y || 0 : position.value.startsWith("bottom") ? 100 : 0;
        const step = event.shiftKey ? 10 : 1;
        setPosition(x + (event.key === "ArrowLeft" ? -step : event.key === "ArrowRight" ? step : 0), y + (event.key === "ArrowUp" ? -step : event.key === "ArrowDown" ? step : 0));
    };
    position.onchange = () => { settings.gaming_overlay_position = position.value; render(); };
    monitor.onchange = () => { settings.gaming_overlay_monitor = monitor.value; };
    panel.append(row(t("overlay.enabled"), checkbox(settings.gaming_overlay !== false, value => { settings.gaming_overlay = value; }), "saved"),
        row(t("overlay.monitor"), monitor, "saved"), row(t("overlay.position"), position, "saved"),
        row(t("overlay.scale"), slider(settings.gaming_overlay_scale ?? 100, 75, 200, value => { settings.gaming_overlay_scale = value; render(); }, true), "saved"),
        row(t("overlay.opacity"), slider(settings.gaming_overlay_opacity ?? 88, 20, 100, value => { settings.gaming_overlay_opacity = value; render(); }, true), "saved"),
        row(t("overlay.speakersOnly"), checkbox(settings.gaming_overlay_speakers_only, value => { settings.gaming_overlay_speakers_only = value; render(); }), "saved"),
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
