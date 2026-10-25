// preview-source-policy.js — the single decision point for where link-preview
// images and metadata may come from (181).
//
// The desktop client never contacts a third-party host for a preview. The
// connected noXa server fetches the page, transcodes the image and returns the
// bytes over the control connection as an inline data:image/jpeg URL (see
// client/link_preview.go and internal/netproto/link_preview.go). This module
// therefore accepts exactly two sources:
//
//   1. a server-transcoded inline JPEG (the current transport), and
//   2. an https URL on the connected server's own origin under a reserved
//      proxy path, for deployments that serve previews over the health
//      listener instead of inline.
//
// Everything else — http(s) URLs of other hosts, other MIME types, blob:,
// javascript:, lookalike hosts, credentials, malformed strings — is rejected.
// Callers must not drop a rejected source silently: resolvePreviewImage()
// reports `blocked` so the renderer can show blockedPreviewPlaceholder().
//
// Pure module: no DOM access except through the ownerDocument handed to
// blockedPreviewPlaceholder, no i18n (labels are passed in).

const SERVER_JPEG = /^data:image\/jpeg;base64,[A-Za-z0-9+/]+={0,2}$/;
const MAX_INLINE_LENGTH = 280000;
const PROXY_PATH = /^\/(?:preview|dl)\/[A-Za-z0-9_.~-]+$/;
const MAX_HOST_LENGTH = 80;

// previewServerOrigin normalises the connected server's origin for the
// policy: https only, no credentials, no path, query or hash. Returns null
// for anything else so a missing or broken origin never widens the policy.
export function previewServerOrigin(value) {
    if (typeof value !== "string" || !value.trim()) return null;
    try {
        const url = new URL(value.trim());
        if (url.protocol !== "https:" || !url.hostname || url.username || url.password ||
            url.search || url.hash || (url.pathname !== "" && url.pathname !== "/")) return null;
        return url.origin;
    } catch {
        return null;
    }
}

// isServerInlinePreviewImage accepts only the server-transcoded JPEG data URL
// grammar the Go client also enforces (client/link_preview.go).
export function isServerInlinePreviewImage(value) {
    return typeof value === "string" && value.length <= MAX_INLINE_LENGTH && SERVER_JPEG.test(value);
}

// isServerProxiedPreviewURL returns true only when `url` may be loaded by the
// client for a preview: an inline server JPEG, or an https URL on the exact
// connected-server origin under the reserved proxy path. Third-party hosts
// are never allowed, with or without a server origin.
export function isServerProxiedPreviewURL(url, serverOrigin) {
    if (isServerInlinePreviewImage(url)) return true;
    const origin = previewServerOrigin(serverOrigin);
    if (!origin || typeof url !== "string" || url.length > 2048) return false;
    try {
        const parsed = new URL(url);
        return parsed.origin === origin && !parsed.username && !parsed.password &&
            !parsed.hash && !parsed.search && PROXY_PATH.test(parsed.pathname);
    } catch {
        return false;
    }
}

// blockedPreviewHost extracts a display-safe hostname from a rejected source
// for the placeholder tooltip. Only the hostname is exposed, never the path or
// query, and non-URL garbage yields "".
export function blockedPreviewHost(value) {
    if (typeof value !== "string") return "";
    try {
        const parsed = new URL(value);
        // Allow-list instead of naming bad schemes (data:, blob:, javascript:,
        // vbscript:, ...): only web origins have a hostname worth showing.
        if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return "";
        return parsed.hostname.slice(0, MAX_HOST_LENGTH);
    } catch {
        return "";
    }
}

// resolvePreviewImage decides the image source for preview metadata.
//   { src: "<allowed url>", blocked: false, host: "" }   — load it
//   { src: null, blocked: true,  host: "cdn.example" }    — show a placeholder
//   { src: null, blocked: false, host: "" }               — no image offered
// `meta` is the server's LinkPreviewResult (thumbnail/image fields) or a
// bare string.
export function resolvePreviewImage(meta, serverOrigin) {
    const candidate = typeof meta === "string" ? meta
        : meta && typeof meta === "object" ? (meta.thumbnail ?? meta.image ?? meta.thumbnail_url ?? "") : "";
    if (typeof candidate !== "string" || candidate.trim() === "") return { src: null, blocked: false, host: "" };
    if (isServerProxiedPreviewURL(candidate, serverOrigin)) return { src: candidate, blocked: false, host: "" };
    return { src: null, blocked: true, host: blockedPreviewHost(candidate) };
}

// blockedPreviewPlaceholder renders the compact visible marker. Text comes
// from the caller (translated); it is set through textContent/title only.
export function blockedPreviewPlaceholder(ownerDocument, { label, title = "", className = "", host = "" } = {}) {
    if (!ownerDocument?.createElement) return null;
    const element = ownerDocument.createElement("span");
    element.className = ["preview-blocked", className].filter(Boolean).join(" ");
    element.textContent = String(label || "");
    if (title) element.title = String(title);
    element.setAttribute?.("role", "note");
    if (element.dataset) {
        element.dataset.previewBlocked = "";
        if (host) element.dataset.blockedHost = host;
    }
    return element;
}
