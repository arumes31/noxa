import { t } from "./i18n.js";
import "./window-controls.css";

// Match main.go: other desktop platforms retain their native title bar.
export async function initWindowControls() {
    const runtime = window.runtime;
    if (!runtime?.Environment || document.getElementById("window-controls")) return;
    let environment;
    try { environment = await runtime.Environment(); } catch { return; }
    if (environment?.platform !== "windows" || document.getElementById("window-controls")) return;

    const root = document.documentElement;
    const app = document.getElementById("app");
    const tools = document.querySelector("#topbar .topbar-tools");
    if (!app || !tools) return;
    const caption = document.createElement("div");
    caption.id = "window-caption";
    const brand = document.createElement("span");
    brand.textContent = "noXa";
    caption.append(brand);
    document.body.prepend(caption);
    const controls = document.createElement("div");
    controls.id = "window-controls";
    controls.setAttribute("role", "group");
    const shapes = {
        minimise: '<path d="M3 8h10"/>',
        maximise: '<rect x="3.5" y="3.5" width="9" height="9"/>',
        restore: '<path d="M5.5 3.5v-2h9v9h-2"/><rect x="2.5" y="5.5" width="9" height="9"/>',
        close: '<path d="m3 3 10 10M3 13 13 3"/>',
    };
    const buttons = {};
    let maximised = false;
    let disposed = false;
    let stateRequest = 0;
    const paint = () => {
        controls.setAttribute("aria-label", t("window.controls"));
        for (const [name, button] of Object.entries(buttons)) {
            const action = name === "maximise" && maximised ? "restore" : name;
            button.title = t(`window.${action}`);
            button.setAttribute("aria-label", button.title);
            button.innerHTML = `<svg viewBox="0 0 16 16" aria-hidden="true">${shapes[action]}</svg>`;
        }
    };
    const syncState = async () => {
        const request = ++stateRequest;
        try {
            const value = await runtime.WindowIsMaximised();
            if (disposed || request !== stateRequest) return;
            maximised = value === true;
            paint();
        } catch { /* Keep the last known state while the native window changes. */ }
    };
    for (const name of ["minimise", "maximise", "close"]) {
        const button = document.createElement("button");
        button.type = "button";
        button.dataset.windowAction = name;
        button.addEventListener("click", () => {
            if (name === "minimise") runtime.WindowMinimise();
            else if (name === "maximise") { runtime.WindowToggleMaximise(); void syncState(); }
            // Runtime Quit invokes OnBeforeClose, preserving close-to-tray and
            // closing audio. App.Quit is the separate explicit exit command.
            else runtime.Quit();
        });
        buttons[name] = button;
        controls.append(button);
    }
    const place = () => {
        const fallback = app.classList.contains("hidden") || document.body.classList.contains("compact");
        const host = fallback ? caption : tools;
        if (controls.parentElement !== host) host.append(controls);
        caption.hidden = !fallback;
        root.dataset.windowCaption = fallback ? "fallback" : "integrated";
    };
    const doubleClick = event => {
        if (event.button !== 0 || !(event.target instanceof Element)) return;
        if (!event.target.closest("#topbar, #window-caption")) return;
        if (getComputedStyle(event.target).getPropertyValue("--wails-draggable").trim() !== "drag") return;
        runtime.WindowToggleMaximise();
        void syncState();
    };
    root.classList.add("native-window");
    paint();
    place();
    void syncState();
    const observer = new MutationObserver(place);
    observer.observe(app, { attributes: true, attributeFilter: ["class"] });
    observer.observe(document.body, { attributes: true, attributeFilter: ["class"] });
    window.addEventListener("resize", syncState);
    window.addEventListener("focus", syncState);
    window.addEventListener("noxa-language-changed", paint);
    document.addEventListener("dblclick", doubleClick);
    window.addEventListener("pagehide", () => {
        disposed = true;
        observer.disconnect();
        window.removeEventListener("resize", syncState);
        window.removeEventListener("focus", syncState);
        window.removeEventListener("noxa-language-changed", paint);
        document.removeEventListener("dblclick", doubleClick);
    }, { once: true });
}
