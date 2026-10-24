// appearance.js — keeps <html> in step with the Appearance settings: theme,
// accent, font scale, compact and reduce motion. Imported once by main.js.
import { applyAppearance, resolveTheme } from "../core/appearance.js";
import { all, onChange } from "../core/settings.js";

const KEYS = new Set(["theme", "accent", "fontScale", "compact", "reduceMotion"]);
const lightQuery = globalThis.matchMedia?.("(prefers-color-scheme: light)");

/** The browser bar follows the theme's background. */
function themeColor(settings) {
    const meta = document.querySelector('meta[name="theme-color"]');
    if (!meta) return;
    const background = getComputedStyle(document.documentElement).getPropertyValue("--bg").trim();
    if (background) meta.setAttribute("content", background);
    else meta.setAttribute("content", resolveTheme(settings.theme, !!lightQuery?.matches) === "light" ? "#f4f7fa" : "#03070a");
}

export function refreshAppearance() {
    const settings = all();
    applyAppearance(document.documentElement, settings);
    themeColor(settings);
}

refreshAppearance();
onChange(({ changed }) => { if (changed.some(name => KEYS.has(name))) refreshAppearance(); });
lightQuery?.addEventListener?.("change", refreshAppearance);
