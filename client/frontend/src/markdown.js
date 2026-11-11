import MarkdownIt from "markdown-it";
import { previewImage } from "./preview-image.js";
import { t as translate } from "./i18n.js";
// markdown.js — (91/92/93) XSS-safe chat markdown: CommonMark + GFM tables
// and strikethrough, parsed by markdown-it with raw HTML disabled. Every
// renderer rule below emits only escaped text or fixed noXa markup; links
// and images keep the delegated md-link / click-to-load placeholder forms.

// ~50 common shortcode -> unicode emoji mappings (95).
export const EMOJI = {
    smile: "😄", smiley: "😃", grin: "😁", laughing: "😆", sweat_smile: "😅",
    joy: "😂", rofl: "🤣", wink: "😉", blush: "😊", yum: "😋",
    sunglasses: "😎", heart_eyes: "😍", kissing_heart: "😘", thinking: "🤔", neutral_face: "😐",
    expressionless: "😑", rolling_eyes: "🙄", smirk: "😏", worried: "😟", cry: "😢",
    sob: "😭", angry: "😠", rage: "🤬", scream: "😱", sleeping: "😴",
    mask: "😷", nerd: "🤓", innocent: "😇", upside_down: "🙃", salute: "🫡",
    thumbsup: "👍", "+1": "👍", thumbsdown: "👎", "-1": "👎", ok_hand: "👌",
    clap: "👏", wave: "👋", muscle: "💪", pray: "🙏", eyes: "👀",
    eye: "👁️", fire: "🔥", sparkles: "✨", star: "⭐", tada: "🎉",
    heart: "❤️", broken_heart: "💔", hundred: "💯", "100": "💯", boom: "💥",
    zap: "⚡", rocket: "🚀", check: "✅", x: "❌", warning: "⚠️",
    question: "❓", exclamation: "❗", bulb: "💡", lock: "🔒", unlock: "🔓",
    microphone: "🎙️", speaker: "🔊", mute: "🔇", computer: "💻", bug: "🐛",
    poop: "💩", skull: "💀", ghost: "👻", robot: "🤖", pizza: "🍕",
};

const KEYWORDS = /\b(func|package|import|return|if|else|for|range|go|defer|var|const|let|function|class|new|typeof|instanceof|await|async|yield|type|struct|interface|map|chan|nil|true|false|null|undefined|switch|case|default|break|continue|fallthrough|select|this|export|from|extends|try|catch|finally|throw|in|of|do|while)\b/g;

// escapeHTML escapes the five HTML-significant characters.
export function escapeHTML(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({
        "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
    }[c]));
}

// highlight applies light syntax colouring to ALREADY-escaped code text:
// strings, // and /* */ comments, and a shared js/go keyword set (92).
// Keywords run FIRST so they can't match inside the span markup injected by
// the string/comment passes; nested spans (keyword inside a string) are fine.
function highlight(escaped) {
    return escaped
        .replace(KEYWORDS, `<span class="tk-kw">$1</span>`)
        .replace(/(&quot;[^\n]*?&quot;|&#39;[^\n]*?&#39;)/g, `<span class="tk-str">$1</span>`)
        .replace(/(\/\/[^\n]*|\/\*[\s\S]*?\*\/)/g, `<span class="tk-com">$1</span>`);
}

// (603) Line numbers for multi-line blocks. The gutter is outside <code>, so
// copying or selecting the code never includes the numbers.
function codeGutter(body) {
    const lines = body.split("\n").length;
    if (lines < 2) return "";
    return `<span class="md-gutter" aria-hidden="true">${Array.from({ length: lines }, (_, i) => i + 1).join("\n")}</span>`;
}

// (603) Copy button; code-blocks.js handles the click (no inline handlers
// under the CSP).
function copyCodeButton() {
    const label = escapeHTML(translate("chat.copyCode"));
    return `<button type="button" class="md-copy" title="${label}" aria-label="${label}">⧉</button>`;
}

function detectLanguage(code) {
    if (/^\s*(package\s+\w+|func\s+\w+|go\s+func|type\s+\w+\s+struct)\b/m.test(code)) return "go";
    if (/\b(const|let|var)\s+\w+\s*=|=>|\b(document|window)\./.test(code)) return "javascript";
    if (/^\s*(def|class|from\s+\w+\s+import|import\s+\w+)\b/m.test(code)) return "python";
    if (/^\s*(SELECT|INSERT|UPDATE|DELETE|CREATE\s+TABLE)\b/im.test(code)) return "sql";
    if (/^\s*[.#]?[\w-]+\s*\{[^}]*:[^}]*\}/m.test(code)) return "css";
    if (/^\s*[<{][\s\S]*[>}]\s*$/.test(code)) return "markup";
    return "plain";
}

function codeBlock(code, info) {
    const body = code.replace(/\n$/, "");
    const language = escapeHTML(((info.match(/^[\w+#-]+/) || [""])[0] || detectLanguage(body)).toLowerCase());
    return `<pre class="md-pre language-${language}" data-lang="${language}">${codeGutter(body)}` +
        `<code>${highlight(escapeHTML(body))}</code>${copyCodeButton()}</pre>`;
}

// Raw HTML stays text, single newlines stay line breaks (chat habit), and
// only bare http(s) URLs autolink. Indented code blocks are off: chat users
// indent prose, and fences cover code.
const md = new MarkdownIt({ html: false, linkify: true, typographer: false, breaks: true });
md.linkify.set({ fuzzyLink: false, fuzzyEmail: false, fuzzyIP: false });
md.disable("code");
// Links and images: http(s) only. Anything else stays literal text.
md.validateLink = (url) => /^https?:\/\//i.test(url);

// GFM task items show a display-only box; the source stays plain text.
md.core.ruler.after("inline", "noxa_tasks", (state) => {
    const tokens = state.tokens;
    for (let i = 2; i < tokens.length; i++) {
        if (tokens[i].type !== "inline" || tokens[i - 2].type !== "list_item_open") continue;
        const first = tokens[i].children?.[0];
        if (first?.type === "text") first.content = first.content.replace(/^\[([ xX])\]\s+/, (_m, c) => (c === " " ? "☐ " : "☑ "));
    }
});

const rules = md.renderer.rules;
// :shortcode: emoji (95) on escaped text only.
rules.text = (tokens, i) => escapeHTML(tokens[i].content).replace(/:([a-z0-9_+-]+):/gi, (m, name) => EMOJI[name.toLowerCase()] || m);
rules.code_inline = (tokens, i) => `<code class="md-code">${escapeHTML(tokens[i].content)}</code>`;
rules.fence = (tokens, i) => codeBlock(tokens[i].content, tokens[i].info.trim());
// Opening happens via click delegation (runtime.BrowserOpenURL) in chat-ui.js
// — never via a raw href navigation inside the webview. Link titles are
// dropped: the tooltip always shows the real target.
rules.link_open = (tokens, i) => {
    const url = escapeHTML(tokens[i].attrGet("href"));
    return `<a href="#" class="md-link" data-url="${url}" title="${url}">`;
};
// Images (channel descriptions; 113). Remote images are never fetched on
// render: a click-to-load placeholder keeps senders from learning each
// reader's IP address and read time.
rules.image = (tokens, i, options, env, self) => {
    const src = tokens[i].attrGet("src");
    const alt = escapeHTML(self.renderInlineAsText(tokens[i].children, options, env));
    // The loader only fetches https; an http image stays an ordinary link.
    if (!/^https:\/\//i.test(src)) {
        const url = escapeHTML(src);
        return `<a href="#" class="md-link" data-url="${url}" title="${url}">${alt || url}</a>`;
    }
    return `<button type="button" class="md-img-load" data-img="${imageID(src)}" data-alt="${alt}" title="${escapeHTML(src)}">🖼 ${alt || "image"}</button>`;
};
// __text__ keeps its noXa meaning (underline); the format toolbar emits it.
rules.strong_open = (tokens, i) => (tokens[i].markup === "__" ? "<u>" : "<strong>");
rules.strong_close = (tokens, i) => (tokens[i].markup === "__" ? "</u>" : "</strong>");
rules.s_open = () => "<del>";
rules.s_close = () => "</del>";
rules.table_open = () => `<div class="md-table-wrap"><table class="md-table">`;
rules.table_close = () => "</table></div>";
// Alignment would need inline style attributes; cells keep the table CSS.
rules.th_open = () => "<th>";
rules.td_open = () => "<td>";

// renderMarkdown converts user text to a safe HTML string. A single
// paragraph renders without a <p> wrapper so plain chat lines keep their
// inline layout.
export function renderMarkdown(text) {
    if (!text) return "";
    const env = {};
    const tokens = md.parse(String(text), env);
    if (tokens.length === 3 && tokens[0].type === "paragraph_open") {
        return md.renderer.renderInline(tokens[1].children, md.options, env);
    }
    return md.renderer.render(tokens, md.options, env).trimEnd();
}

// Image URLs validated by renderMarkdown, keyed by the opaque id a placeholder
// carries. The source is never read back from DOM attributes, so nothing in
// the page can make a placeholder fetch an unvalidated URL. One entry exists
// per distinct (unescaped) URL.
const imageURLs = new Map();
const imageIDs = new Map();
// Bounded: remote senders choose these URLs. The oldest placeholders stop
// loading once evicted (their messages have long left the capped chat log).
const IMAGE_ID_LIMIT = 2000;
let nextImageID = 0;

function imageID(url) {
    let id = imageIDs.get(url);
    if (id === undefined) {
        id = String(++nextImageID);
        imageIDs.set(url, id);
        imageURLs.set(id, url);
        while (imageIDs.size > IMAGE_ID_LIMIT) {
            const [oldURL, oldID] = imageIDs.entries().next().value;
            imageIDs.delete(oldURL);
            imageURLs.delete(oldID);
        }
    }
    return id;
}

// A reader's explicit click requests an inert raster image from their server.
export async function loadImagePlaceholder(button) {
    const src = imageURLs.get(button?.getAttribute?.("data-img") || "") || "";
    if (!/^https:\/\//i.test(src)) return null;
    const state = globalThis.window?.__noxa?.state;
    const tabID = state?.activeTabID, generation = state?.serverGeneration;
    if (!tabID || button.disabled) return null;
    button.disabled = true;
    try {
        const result = await window.go.main.App.ImagePreviewForTab(tabID, src);
        const data = previewImage(result?.thumbnail);
        if (!data || !button.isConnected || window.__noxa.state.activeTabID !== tabID || window.__noxa.state.serverGeneration !== generation) return null;
        const img = button.ownerDocument.createElement("img");
        img.className = "md-img";
        img.alt = button.getAttribute("data-alt") || "";
        img.decoding = "async";
        img.referrerPolicy = "no-referrer";
        img.src = data;
        button.replaceWith(img);
        return img;
    } catch { return null; }
    finally { button.disabled = false; }
}

// bindImagePlaceholders installs one delegated click handler that loads a
// placeholder's image only after the reader asks for it (idempotent per root).
export function bindImagePlaceholders(root) {
    if (!root?.addEventListener || root.__noxaImagePlaceholders) return;
    root.__noxaImagePlaceholders = true;
    root.addEventListener("click", (event) => {
        const button = event.target?.closest?.(".md-img-load");
        if (!button) return;
        event.preventDefault();
        event.stopPropagation();
        loadImagePlaceholder(button);
    }, true);
}
