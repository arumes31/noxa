import { t } from "./i18n.js";
import { loadPreviewMetadata } from "./preview-metadata.js";

export function xPost(value) {
    try {
        const url = new URL(value);
        if (!["https:", "http:"].includes(url.protocol) || url.username || url.password || url.port) return null;
        if (!["x.com", "www.x.com", "mobile.x.com", "twitter.com", "www.twitter.com", "mobile.twitter.com"].includes(url.hostname.toLowerCase())) return null;
        const match = url.pathname.match(/^\/(?:[A-Za-z0-9_]{1,15}\/status|i\/(?:web\/)?status)\/([1-9][0-9]{0,19})(?:\/(?:photo|video)\/[1-4])?\/?$/);
        if (!match) return null;
        return { id: match[1], url: `https://x.com/i/status/${match[1]}` };
    } catch { return null; }
}

export function renderXPostPreviews(container, { enabled, scope, isCurrent }) {
    if (!enabled) return;
    const seen = new Set();
    for (const link of container.querySelectorAll(".md-link[data-url]")) {
        const post = xPost(link.dataset.url);
        if (!post || seen.has(post.id) || seen.size >= 3) continue;
        seen.add(post.id);
        const card = document.createElement("button");
        card.type = "button"; card.className = "x-post-preview"; card.dataset.postId = post.id;
        const provider = document.createElement("span"); provider.className = "x-post-provider"; provider.textContent = "X"; provider.setAttribute("aria-hidden", "true");
        const author = document.createElement("strong"); author.className = "x-post-author"; author.textContent = t("midnight.xPost");
        const text = document.createElement("span"); text.className = "x-post-text"; text.textContent = t("midnight.loadingXPost");
        const domain = document.createElement("span"); domain.className = "x-post-domain"; domain.textContent = t("midnight.openXPost");
        card.append(provider, author, text, domain); container.append(card);
        void loadPreviewMetadata("x", post.id, scope, () => card.isConnected && isCurrent(), () => window.go.main.App.XPostPreviewForTab(scope.tabID, post.id)).then(result => {
            if (!card.isConnected || !isCurrent()) return;
            text.textContent = typeof result?.text === "string" && result.text ? result.text.slice(0, 2000) : t("midnight.xPostUnavailable");
            if (typeof result?.author === "string" && result.author) author.textContent = result.author.slice(0, 120);
        });
        card.onclick = () => {
            if (!card.isConnected || !isCurrent()) return;
            Promise.resolve().then(() => window.runtime.BrowserOpenURL(post.url)).catch(() => {
                if (isCurrent()) window.__noxa.toast(t("midnight.xPostOpenFailed"), "warn");
            });
        };
    }
}
