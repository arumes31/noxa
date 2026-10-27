import { blockedPreviewPlaceholder, resolvePreviewImage } from "./preview-source-policy.js";
import { t } from "./i18n.js";
import { loadPreviewMetadata } from "./preview-metadata.js";

// blockedThumbnail swaps the image for the visible "server only" marker (181)
// when the metadata points at a third-party host instead of server bytes.
function blockedThumbnail(visual, image, host) {
    image.hidden = true;
    image.removeAttribute?.("src");
    const marker = blockedPreviewPlaceholder(visual.ownerDocument || document, {
        label: t("midnight.previewBlocked"),
        title: t("midnight.previewBlockedHelp", { host: host || t("midnight.previewBlockedUnknownHost") }),
        host,
    });
    if (marker) visual.insertBefore(marker, image);
}

// Parse only YouTube video URLs. Neither arbitrary hosts nor provider HTML
// are used by the native metadata request or the image renderer.
export function youtubeVideo(value) {
    try {
        const url = new URL(value);
        if (!["https:", "http:"].includes(url.protocol) || url.username || url.password || url.port) return null;
        const host = url.hostname.toLowerCase();
        let id;
        if (host === "youtu.be") id = url.pathname.slice(1).split("/")[0];
        else if (["youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com", "www.youtube-nocookie.com"].includes(host)) {
            if (url.pathname === "/watch") id = url.searchParams.get("v");
            else id = url.pathname.match(/^\/(?:shorts|embed|live)\/([^/]+)\/?$/)?.[1];
        }
        if (!/^[\w-]{11}$/.test(id || "")) return null;
        url.protocol = "https:";
        return { id, url: url.href };
    } catch { return null; }
}

function loadMetadata(video, scope, isCurrent) {
    return loadPreviewMetadata("youtube", video.id, scope, isCurrent, () => window.go.main.App.YouTubePreviewForTab(scope.tabID, video.id));
}

export function renderYouTubePreviews(container, { enabled, scope, isCurrent, conversation }) {
    if (!enabled || conversation !== "channel") return;
    const seen = new Set();
    for (const link of container.querySelectorAll(".md-link[data-url]")) {
        const video = youtubeVideo(link.dataset.url);
        if (!video || seen.has(video.id) || seen.size >= 3) continue;
        seen.add(video.id);
        const card = document.createElement("button");
        card.type = "button";
        card.className = "youtube-preview";
        card.setAttribute("aria-label", t("midnight.openVideo"));
        const visual = document.createElement("span"); visual.className = "youtube-visual";
        const image = document.createElement("img");
        image.alt = ""; image.loading = "lazy"; image.decoding = "async"; image.referrerPolicy = "no-referrer";
        image.width = 480; image.height = 270;
        const play = document.createElement("span"); play.className = "youtube-play"; play.textContent = "▶"; play.setAttribute("aria-hidden", "true");
        visual.append(image, play);
        const info = document.createElement("span"); info.className = "youtube-info";
        const provider = document.createElement("span"); provider.className = "youtube-provider"; provider.textContent = "▶ YouTube";
        const title = document.createElement("strong"); title.className = "youtube-title"; title.textContent = t("midnight.youtubeVideo");
        const author = document.createElement("span"); author.className = "youtube-author"; author.textContent = t("midnight.openVideo");
        const domain = document.createElement("span"); domain.className = "youtube-domain"; domain.textContent = "youtube.com";
        info.append(provider, title, author, domain); card.append(visual, info); container.append(card);
        let started = false;
        const load = async () => {
            if (started || !card.isConnected || !isCurrent()) return;
            started = true;
            const result = await loadMetadata(video, scope, () => card.isConnected && isCurrent());
            if (!result || !card.isConnected || !isCurrent()) return;
            if (typeof result.title === "string" && result.title) {
                title.textContent = result.title.slice(0, 200);
                card.setAttribute("aria-label", t("midnight.openVideoTitle", { title: title.textContent }));
            }
            if (typeof result.author === "string" && result.author) author.textContent = result.author.slice(0, 120);
            const thumbnail = resolvePreviewImage(result);
            if (thumbnail.src) { image.hidden = false; image.src = thumbnail.src; }
            else if (thumbnail.blocked) blockedThumbnail(visual, image, thumbnail.host);
        };
        image.hidden = true;
        image.onerror = () => { image.hidden = true; };
        queueMicrotask(() => { void load(); });
        card.onclick = () => {
            if (!card.isConnected || !isCurrent()) return;
            Promise.resolve().then(() => window.runtime.BrowserOpenURL(video.url)).catch(() => {
                if (isCurrent()) window.__noxa.toast(t("midnight.videoOpenFailed"), "warn");
            });
        };
    }
}
