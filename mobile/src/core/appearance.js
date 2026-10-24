// appearance.js — pure helpers behind the Appearance settings: theme names,
// accent colour variables and font scale. ui/appearance.js applies the
// result to <html>; the colours themselves live in themes.css.

export const THEMES = Object.freeze(["midnight", "arctic", "light", "contrast", "system"]);
export const ACCENTS = Object.freeze(["#34d9ef", "#7c9cff", "#b18cff", "#ff6b81", "#f5b041", "#4ade80"]);
export const FONT_SCALE_MIN = 0.9;
export const FONT_SCALE_MAX = 1.3;

/** A stored theme name, or the default for anything unknown. */
export function normalizeTheme(name) {
    return THEMES.includes(name) ? name : "midnight";
}

/** The concrete palette: "system" follows the phone's light/dark choice. */
export function resolveTheme(name, prefersLight = false) {
    const theme = normalizeTheme(name);
    if (theme !== "system") return theme;
    return prefersLight ? "light" : "midnight";
}

/** "#abc" / "abc" / "#aabbcc" -> "#aabbcc"; "" for anything else. */
export function normalizeHex(input) {
    const value = String(input || "").trim().replace(/^#/, "").toLowerCase();
    if (/^[0-9a-f]{3}$/.test(value)) return `#${[...value].map(c => c + c).join("")}`;
    if (/^[0-9a-f]{6}$/.test(value)) return `#${value}`;
    return "";
}

export function hexToRGB(hex) {
    const value = normalizeHex(hex);
    if (!value) return null;
    return [1, 3, 5].map(i => parseInt(value.slice(i, i + 2), 16));
}

/**
 * CSS variables for a custom accent: the colour itself plus the translucent
 * fill and strong border the themes derive from it. Empty for no override.
 */
export function accentVars(hex) {
    const rgb = hexToRGB(hex);
    if (!rgb) return {};
    const [r, g, b] = rgb;
    return {
        "--accent": normalizeHex(hex),
        "--accent-dim": `rgba(${r}, ${g}, ${b}, 0.14)`,
        "--border-strong": `rgba(${r}, ${g}, ${b}, 0.45)`,
    };
}

/** Font scale clamped to 0.9–1.3 in steps of 0.05. */
export function clampScale(value) {
    const number = Number(value);
    if (!Number.isFinite(number)) return 1;
    const clamped = Math.min(FONT_SCALE_MAX, Math.max(FONT_SCALE_MIN, number));
    return Math.round(clamped * 20) / 20;
}

/**
 * The attributes and CSS variables <html> gets for a settings object:
 * {attributes: {name: value|null}, vars: {name: value|null}}. null removes.
 */
export function appearanceFor(settings) {
    const accent = accentVars(settings.accent);
    const scale = clampScale(settings.fontScale ?? 1);
    return {
        attributes: {
            "data-theme": normalizeTheme(settings.theme),
            "data-compact": settings.compact ? "" : null,
            "data-reduce-motion": settings.reduceMotion ? "" : null,
        },
        vars: {
            "--accent": accent["--accent"] ?? null,
            "--accent-dim": accent["--accent-dim"] ?? null,
            "--border-strong": accent["--border-strong"] ?? null,
            "--font-scale": scale === 1 ? null : String(scale),
        },
    };
}

/** Writes appearanceFor(settings) onto an element (normally <html>). */
export function applyAppearance(element, settings) {
    const { attributes, vars } = appearanceFor(settings);
    for (const [name, value] of Object.entries(attributes)) {
        if (value === null) element.removeAttribute(name);
        else element.setAttribute(name, value);
    }
    for (const [name, value] of Object.entries(vars)) {
        if (value === null) element.style.removeProperty(name);
        else element.style.setProperty(name, value);
    }
}
