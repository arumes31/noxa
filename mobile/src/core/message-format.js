// message-format.js — DOM-free helpers behind the rich message view: file
// token splitting, link display, media kinds, timestamps and day keys,
// emoji search. Kept apart from ui/rich-text.js so Node can test them.
import { EMOJI, linksIn } from "./markdown.js";

/** Splits a body into text and attachment segments, in order. */
export function splitFileTokens(text, refs) {
    const out = [];
    let rest = String(text ?? "");
    for (const ref of refs) {
        const at = rest.indexOf(ref.token);
        if (at < 0) continue;
        const before = rest.slice(0, at).trim();
        if (before) out.push({ type: "text", text: before });
        out.push({ type: "file", ref });
        rest = rest.slice(at + ref.token.length);
    }
    if (rest.trim()) out.push({ type: "text", text: refs.length ? rest.trim() : rest });
    return out;
}

/** The URL when it is http(s), else null: nothing else becomes a link target. */
export function safeURL(url) {
    try {
        const parsed = new URL(url);
        return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed.href : null;
    } catch {
        return null;
    }
}

/** A URL shortened for display: at most max characters, ellipsis in the middle of the path. */
export function displayURL(url, max = 60) {
    const text = String(url);
    if (text.length <= max) return text;
    const head = Math.ceil((max - 1) * 0.7);
    const tail = max - 1 - head;
    return `${text.slice(0, head)}…${tail > 0 ? text.slice(-tail) : ""}`;
}

const TWITCH_RESERVED = new Set(["directory", "downloads", "settings", "videos", "clip", "search", "subscriptions", "inventory", "wallet", "jobs", "p", "turbo"]);

/** A video portal resource as the server expects it (client/frontend video-portal-previews.js). */
function portal(u) {
    const host = u.hostname.toLowerCase();
    const path = u.pathname;
    if (host === "youtu.be" || ["youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com", "www.youtube-nocookie.com"].includes(host)) {
        const id = host === "youtu.be" ? path.slice(1).split("/")[0] : path === "/watch" ? u.searchParams.get("v") : path.match(/^\/(?:shorts|embed|live)\/([^/]+)\/?$/)?.[1];
        return /^[\w-]{11}$/.test(id || "") ? { provider: "youtube", resource: id } : null;
    }
    if (["x.com", "www.x.com", "mobile.x.com", "twitter.com", "www.twitter.com", "mobile.twitter.com"].includes(host)) {
        const id = path.match(/^\/(?:[A-Za-z0-9_]{1,15}\/status|i\/(?:web\/)?status)\/([1-9][0-9]{0,19})(?:\/(?:photo|video)\/[1-4])?\/?$/)?.[1];
        return id ? { provider: "x", resource: id } : null;
    }
    if (["whatnot.com", "www.whatnot.com"].includes(host)) {
        if (path === "/") return { provider: "whatnot", resource: "home" };
        const match = path.match(/^\/(live|clip|listing|s|user)\/([A-Za-z0-9_.-]{1,64})\/?$/);
        if (!match) return null;
        const [, kind, id] = match;
        const valid = ["live", "clip", "listing"].includes(kind) ? /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/i.test(id) : kind === "s" ? /^[A-Za-z0-9_-]+$/.test(id) : ![".", ".."].includes(id);
        return valid ? { provider: "whatnot", resource: `${kind}:${id}` } : null;
    }
    if (["twitch.tv", "www.twitch.tv", "m.twitch.tv", "clips.twitch.tv", "player.twitch.tv"].includes(host)) {
        let clip, video, channel;
        if (host === "clips.twitch.tv") clip = path === "/embed" ? u.searchParams.get("clip") : path.match(/^\/([^/]+)\/?$/)?.[1];
        else if (host === "player.twitch.tv") { channel = u.searchParams.get("channel")?.toLowerCase(); video = u.searchParams.get("video")?.replace(/^v/, ""); }
        else { clip = path.match(/^\/[^/]+\/clip\/([^/]+)\/?$/)?.[1]; video = path.match(/^\/videos\/([^/]+)\/?$/)?.[1]; channel = path.match(/^\/([^/]+)\/?$/)?.[1]?.toLowerCase(); }
        if (/^[a-z0-9_]{1,25}$/.test(channel || "") && !TWITCH_RESERVED.has(channel)) return { provider: "twitch", resource: `channel:${channel}` };
        if (/^[A-Za-z0-9_-]{1,128}$/.test(clip || "")) return { provider: "twitch", resource: `clip:${clip}` };
        if (/^[1-9][0-9]{0,19}$/.test(video || "")) return { provider: "twitch", resource: `video:${video}` };
        return null;
    }
    if (["vimeo.com", "www.vimeo.com", "player.vimeo.com"].includes(host)) {
        const id = host === "player.vimeo.com" ? path.match(/^\/video\/([0-9]+)\/?$/)?.[1] : path.match(/^\/(?:channels\/[^/]+\/|groups\/[^/]+\/videos\/)?([0-9]+)\/?$/)?.[1];
        return /^[1-9][0-9]{0,19}$/.test(id || "") ? { provider: "vimeo", resource: id } : null;
    }
    if (["dailymotion.com", "www.dailymotion.com", "dai.ly"].includes(host)) {
        const id = (host === "dai.ly" ? path.match(/^\/([^/]+)\/?$/)?.[1] : path.match(/^\/(?:embed\/)?video\/([^/]+)\/?$/)?.[1])?.split("_")[0];
        return /^[A-Za-z0-9]{1,20}$/.test(id || "") ? { provider: "dailymotion", resource: id } : null;
    }
    return undefined;
}

/**
 * The server preview request for a URL: {provider, resource} where portals
 * take an ID (YouTube video, X post, twitch "clip:<id>", ...) and other https
 * pages go to the "web" provider with the URL. null when nothing applies.
 */
export function previewRequest(url) {
    let u;
    try { u = new URL(url); } catch { return null; }
    if (!["https:", "http:"].includes(u.protocol) || u.username || u.password) return null;
    const video = u.port ? undefined : portal(u);
    if (video) return video;
    return u.protocol === "https:" ? { provider: "web", resource: u.href } : null;
}

/** The first link of a message the server can preview, or null. */
export function previewLink(nodes) {
    return linksIn(nodes).find(url => previewRequest(url)) || null;
}

const IMAGE = /\.(png|jpe?g|gif|webp|avif)$/i;
const VIDEO = /\.(mp4|webm)$/i;
const MIME = {
    png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg", gif: "image/gif", webp: "image/webp", avif: "image/avif",
    mp4: "video/mp4", webm: "video/webm", txt: "text/plain", pdf: "application/pdf", json: "application/json",
};

/** "image", "video" or "" for an attachment name. */
export function mediaKind(name) {
    if (IMAGE.test(name || "")) return "image";
    if (VIDEO.test(name || "")) return "video";
    return "";
}

export function mimeOf(name) {
    return MIME[String(name || "").split(".").pop().toLowerCase()] || "application/octet-stream";
}

export function formatSize(bytes) {
    if (!bytes) return "";
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
    return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

/** Local calendar day of a Unix time in seconds, "YYYY-MM-DD". */
export function dayKey(seconds) {
    const d = new Date(seconds * 1000);
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

/** Whole days between two local days (b - a). */
export function daysBetween(a, b) {
    const [ay, am, ad] = a.split("-").map(Number);
    const [by, bm, bd] = b.split("-").map(Number);
    return Math.round((Date.UTC(by, bm - 1, bd) - Date.UTC(ay, am - 1, ad)) / 86400000);
}

/** A short relative time ("2 min ago") in the given locale. */
export function relativeTime(seconds, now = Date.now() / 1000, locale = "en") {
    const diff = Math.round(seconds - now);
    const abs = Math.abs(diff);
    const format = new Intl.RelativeTimeFormat(locale, { numeric: "auto", style: "short" });
    if (abs < 45) return format.format(0, "second");
    if (abs < 3600) return format.format(Math.round(diff / 60), "minute");
    if (abs < 86400) return format.format(Math.round(diff / 3600), "hour");
    if (abs < 86400 * 30) return format.format(Math.round(diff / 86400), "day");
    return new Date(seconds * 1000).toLocaleDateString(locale);
}

/** Whether a sender's unique ID is on the blocked list. */
export function isBlocked(uid, blocked) {
    return !!uid && Array.isArray(blocked) && blocked.includes(uid);
}

/**
 * Emoji matching a search: built-in shortcodes and custom server emoji whose
 * name contains the query. An empty query matches nothing (the picker then
 * shows its groups).
 */
export function searchEmoji(query, customNames = []) {
    const needle = String(query || "").trim().toLowerCase().replace(/^:|:$/g, "");
    if (!needle) return { builtin: [], custom: [] };
    const seen = new Set();
    const builtin = [];
    for (const [name, char] of Object.entries(EMOJI)) {
        if (!name.includes(needle) || seen.has(char)) continue;
        seen.add(char);
        builtin.push({ name, char });
    }
    return { builtin, custom: customNames.filter(name => name.toLowerCase().includes(needle)) };
}

/** Inserts text into a value at a selection, returning {value, caret}. */
export function insertText(value, start, end, text) {
    const before = value.slice(0, start);
    return { value: before + text + value.slice(end), caret: before.length + text.length };
}
