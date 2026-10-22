import { t } from "./i18n.js";
import { loadPreviewMetadata } from "./preview-metadata.js";

const numeric = /^[1-9][0-9]{0,19}$/;
const clipID = /^[A-Za-z0-9_-]{1,128}$/;
const channelID = /^[a-z0-9_]{1,25}$/;
const dailyID = /^[A-Za-z0-9]{1,20}$/;
const reservedTwitch = new Set(["directory", "downloads", "settings", "videos", "clip", "search", "subscriptions", "inventory", "wallet", "jobs", "p", "turbo"]);

export function videoPortal(value) {
    try {
        const u = new URL(value);
        if (!["https:", "http:"].includes(u.protocol) || u.username || u.password || u.port) return null;
        const host = u.hostname.toLowerCase(), path = u.pathname;
        const result = (provider, label, id, url) => ({ provider, label, id, url, key: `${provider}:${id}` });
        if (["whatnot.com", "www.whatnot.com"].includes(host)) {
            if (path === "/") return result("whatnot", "Whatnot", "home", "https://www.whatnot.com/");
            const match = path.match(/^\/(live|clip|listing|s|user)\/([A-Za-z0-9_.-]{1,64})\/?$/);
            if (match) {
                const [, kind, id] = match;
                const valid = ["live", "clip", "listing"].includes(kind) ? /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/i.test(id) : kind === "s" ? /^[A-Za-z0-9_-]+$/.test(id) : ![".", ".."].includes(id);
                if (valid) return result("whatnot", "Whatnot", `${kind}:${id}`, `https://www.whatnot.com/${kind}/${id}`);
            }
        }
        if (["twitch.tv", "www.twitch.tv", "m.twitch.tv", "clips.twitch.tv", "player.twitch.tv"].includes(host)) {
            let clip, video, channel;
            if (host === "clips.twitch.tv") clip = path === "/embed" ? u.searchParams.get("clip") : path.match(/^\/([^/]+)\/?$/)?.[1];
            else if (host === "player.twitch.tv") { channel = u.searchParams.get("channel")?.toLowerCase(); video = u.searchParams.get("video")?.replace(/^v/, ""); }
            else { clip = path.match(/^\/[^/]+\/clip\/([^/]+)\/?$/)?.[1]; video = path.match(/^\/videos\/([^/]+)\/?$/)?.[1]; channel = path.match(/^\/([^/]+)\/?$/)?.[1]?.toLowerCase(); }
            if (channelID.test(channel || "") && !reservedTwitch.has(channel)) return result("twitch", "Twitch", `channel:${channel}`, `https://www.twitch.tv/${channel}`);
            if (clipID.test(clip || "")) return result("twitch", "Twitch", `clip:${clip}`, `https://clips.twitch.tv/${clip}`);
            if (numeric.test(video || "")) return result("twitch", "Twitch", `video:${video}`, `https://www.twitch.tv/videos/${video}`);
        }
        if (["vimeo.com", "www.vimeo.com", "player.vimeo.com"].includes(host)) {
            const id = host === "player.vimeo.com" ? path.match(/^\/video\/([0-9]+)\/?$/)?.[1] : path.match(/^\/(?:channels\/[^/]+\/|groups\/[^/]+\/videos\/)?([0-9]+)\/?$/)?.[1];
            if (numeric.test(id || "")) return result("vimeo", "Vimeo", id, `https://vimeo.com/${id}`);
        }
        if (["dailymotion.com", "www.dailymotion.com", "dai.ly"].includes(host)) {
            const id = (host === "dai.ly" ? path.match(/^\/([^/]+)\/?$/)?.[1] : path.match(/^\/(?:embed\/)?video\/([^/]+)\/?$/)?.[1])?.split("_")[0];
            if (dailyID.test(id || "")) return result("dailymotion", "Dailymotion", id, `https://www.dailymotion.com/video/${id}`);
        }
    } catch { /* Not a supported public video URL. */ }
    return null;
}

export function portalThumbnail(provider, value) {
    try {
        if (typeof value !== "string" || value.length > 2048) return null;
        const u = new URL(value), host = u.hostname.toLowerCase();
        if (u.protocol !== "https:" || u.username || u.password || u.port) return null;
        const allowed = provider === "whatnot" ? ["images.whatnot.com", "assets.whatnot.com", "www.whatnot.com"].includes(host) : provider === "twitch" ? host === "static-cdn.jtvnw.net" || /^clips-media-assets[0-9]?\.twitch\.tv$/.test(host)
            : provider === "vimeo" ? ["i.vimeocdn.com", "secure-b.vimeocdn.com"].includes(host)
                : provider === "dailymotion" && /^s[0-9]?\.dmcdn\.net$/.test(host);
        return allowed ? u.href : null;
    } catch { return null; }
}

export function renderVideoPortalPreviews(container, { enabled, scope, isCurrent }) {
    if (!enabled) return;
    const seen = new Set();
    for (const link of container.querySelectorAll(".md-link[data-url]")) {
        const video = videoPortal(link.dataset.url);
        if (!video || seen.has(video.key) || seen.size >= 3) continue;
        seen.add(video.key);
        const card = document.createElement("button");
        card.type = "button"; card.className = "youtube-preview video-portal-preview"; card.dataset.portalKey = video.key; card.dataset.provider = video.provider;
        const visual = document.createElement("span"); visual.className = "youtube-visual"; visual.setAttribute("aria-hidden", "true");
        const placeholder = document.createElement("span"); placeholder.className = "portal-placeholder"; placeholder.textContent = video.label;
        const image = document.createElement("img"); image.alt = ""; image.hidden = true; image.loading = "lazy"; image.decoding = "async"; image.referrerPolicy = "no-referrer"; image.width = 480; image.height = 270;
        const play = document.createElement("span"); play.className = "youtube-play"; play.textContent = "▶";
        visual.append(placeholder, image, play);
        const info = document.createElement("span"); info.className = "youtube-info";
        const provider = document.createElement("span"); provider.className = "youtube-provider"; provider.textContent = video.label;
        const title = document.createElement("strong"); title.className = "youtube-title"; title.textContent = t("midnight.portalVideo", { provider: video.label });
        const author = document.createElement("span"); author.className = "youtube-author"; author.textContent = t("midnight.portalOpen", { provider: video.label });
        const domain = document.createElement("span"); domain.className = "youtube-domain"; domain.textContent = new URL(video.url).hostname;
        info.append(provider, title, author, domain); card.append(visual, info); container.append(card);
        void loadPreviewMetadata(`portal:${video.provider}`, video.id, scope, () => card.isConnected && isCurrent(), () => window.go.main.App.VideoPortalPreviewForTab(scope.tabID, video.provider, video.id)).then(result => {
            if (!card.isConnected || !isCurrent()) return;
            if (!result) { author.textContent = t("midnight.portalUnavailable", { provider: video.label }); return; }
            if (typeof result.title === "string" && result.title) title.textContent = result.title.slice(0, 200);
            const subtitle = result.author || result.description;
            if (typeof subtitle === "string" && subtitle) author.textContent = subtitle.slice(0, 160);
            const thumbnail = portalThumbnail(video.provider, result.thumbnail);
            if (thumbnail) {
                image.onload = () => { if (card.isConnected && isCurrent()) { image.hidden = false; placeholder.hidden = true; } };
                image.onerror = () => image.remove(); image.hidden = false; image.src = thumbnail;
            }
        });
        card.onclick = () => {
            if (!card.isConnected || !isCurrent()) return;
            Promise.resolve().then(() => window.runtime.BrowserOpenURL(video.url)).catch(() => {
                if (isCurrent()) window.__noxa.toast(t("midnight.portalOpenFailed", { provider: video.label }), "warn");
            });
        };
    }
}
