import { t } from "./i18n.js";

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
        return { id, url: url.href, thumbnail: `https://i.ytimg.com/vi/${id}/hqdefault.jpg` };
    } catch { return null; }
}

const metadata = new Map();
const queue = [];
let running = 0;
function drain() {
    while (running < 4 && queue.length) {
        const job = queue.shift();
        running++;
        Promise.resolve().then(job.run).then(job.resolve, () => job.resolve(null)).finally(() => { running--; drain(); });
    }
}
function loadMetadata(video, scope, isCurrent) {
    const key = `${scope.generation}:${scope.tabID}:${video.id}`;
    if (!metadata.has(key)) {
        if (metadata.size >= 128) metadata.delete(metadata.keys().next().value);
        metadata.set(key, new Promise(resolve => {
            queue.push({ resolve, run: () => isCurrent() ? window.go.main.App.YouTubePreviewForTab(scope.tabID, video.id) : null });
            drain();
        }));
    }
    return metadata.get(key);
}

export function renderYouTubePreviews(container, { enabled, scope, isCurrent }) {
    if (!enabled) return;
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
        };
        image.onload = load;
        image.onerror = () => { image.hidden = true; void load(); };
        image.src = video.thumbnail;
        card.onclick = () => {
            if (!card.isConnected || !isCurrent()) return;
            Promise.resolve().then(() => window.runtime.BrowserOpenURL(video.url)).catch(() => {
                if (isCurrent()) window.__noxa.toast(t("midnight.videoOpenFailed"), "warn");
            });
        };
    }
}
