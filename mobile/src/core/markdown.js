// markdown.js — the chat's markdown (CommonMark + GFM tables, strikethrough
// and task items, via markdown-it like the desktop), parsed into a tree
// instead of HTML so the screens build DOM nodes with textContent only.
// __text__ is underline (noXa), not bold.
//
// Nodes: {type:"text", text} | {type:"code", code, language} (block) |
// {type:"inline-code", code} | {type:"strong"|"u"|"del"|"em", children} |
// {type:"link", url, children?} (no children: a bare URL) | {type:"emoji", name, char} |
// {type:"custom-emoji", name} | {type:"mention", ...} | {type:"br"} |
// {type:"paragraph"|"blockquote"|"item", children} | {type:"heading", level, children} |
// {type:"list", ordered, start, children:[items]} | {type:"hr"} |
// {type:"table", header:[cells], rows:[[cells]]} where cells are node arrays.

import MarkdownIt from "markdown-it";

// ~50 common shortcode -> unicode emoji mappings, shared with the desktop.
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

/** Quick reactions offered first in pickers. */
export const QUICK_EMOJI = ["👍", "❤️", "😂", "😮", "😢", "🔥", "🎉", "👀"];

/** A browsable set for the emoji picker, grouped. */
export const EMOJI_GROUPS = [
    { key: "smileys", chars: "😀 😃 😄 😁 😆 😅 😂 🤣 😊 😇 🙂 🙃 😉 😍 🥰 😘 😋 😎 🤓 🧐 🤔 🤨 😐 😑 😶 🙄 😏 😣 😥 😮 🤐 😯 😪 😫 🥱 😴 😌 😛 😜 😝 🤤 😒 😓 😔 😕 🙁 😖 😞 😟 😤 😢 😭 😦 😧 😨 😩 🤯 😬 😰 😱 🥵 🥶 😳 🤪 😵 🥴 😠 😡 🤬 😷 🤒 🤕 🤢 🤮 🤧 🥳 🥺 🤠 🤡 🤥 🤫 🤭 🫡 🤗 👻 💀 🤖 💩".split(" ") },
    { key: "gestures", chars: "👍 👎 👌 ✌️ 🤞 🤟 🤘 🤙 👋 🤚 ✋ 🖖 👏 🙌 🤝 🙏 💪 👀 👁️ 🧠 🫶 🤌 ☝️ 👆 👇 👈 👉".split(" ") },
    { key: "symbols", chars: "❤️ 🧡 💛 💚 💙 💜 🖤 🤍 💔 💯 💥 ⚡ 🔥 ✨ ⭐ 🎉 🎊 ✅ ❌ ⚠️ ❓ ❗ 💡 🔒 🔓 🔔 🔕 🎙️ 🔊 🔇 💻 📱 🐛 🚀 🏆 🎯 🎮 🎵 🎧 📌 📎 ✏️ 🗑️ ⏰ 📅".split(" ") },
    { key: "nature", chars: "🐶 🐱 🐭 🐹 🐰 🦊 🐻 🐼 🐨 🐯 🦁 🐮 🐷 🐸 🐵 🐔 🐧 🐦 🦆 🦉 🐺 🐝 🦋 🐢 🐍 🐙 🦀 🐟 🐬 🐳 🌸 🌼 🌻 🌲 🍀 🌈 ☀️ 🌙 ⛅ ❄️".split(" ") },
    { key: "food", chars: "🍕 🍔 🍟 🌭 🍿 🧀 🥚 🍳 🥞 🍗 🌮 🌯 🥗 🍝 🍜 🍣 🍱 🍩 🍪 🎂 🍰 🍫 🍬 🍦 ☕ 🍵 🧃 🥤 🍺 🍻 🥂 🍷".split(" ") },
    { key: "activity", chars: "⚽ 🏀 🏈 ⚾ 🎾 🏐 🎱 🏓 🏸 🥊 🎿 🏂 🚴 🏊 🧗 🎪 🎭 🎨 🎬 🎤 🎹 🥁 🎲 🧩 ♟️".split(" ") },
    { key: "travel", chars: "🚗 🚕 🚌 🚎 🏎️ 🚓 🚑 🚒 🚚 🚲 🛵 ✈️ 🚀 🛸 🚁 ⛵ 🚢 🏠 🏢 🏰 🗼 🗽 🏖️ 🏔️ 🌋 🗺️".split(" ") },
];

// Same parser and options as the desktop renderer (client/frontend/src/markdown.js):
// raw HTML stays text, single newlines are line breaks, only bare http(s)
// URLs autolink, no indented code blocks, links and images http(s) only.
const md = new MarkdownIt({ html: false, linkify: true, typographer: false, breaks: true });
md.linkify.set({ fuzzyLink: false, fuzzyEmail: false, fuzzyIP: false });
md.disable("code");
md.validateLink = url => /^https?:\/\//i.test(url);
// GFM task items show a display-only box; the source stays plain text.
md.core.ruler.after("inline", "noxa_tasks", state => {
    const tokens = state.tokens;
    for (let i = 2; i < tokens.length; i++) {
        if (tokens[i].type !== "inline" || tokens[i - 2].type !== "list_item_open") continue;
        const first = tokens[i].children?.[0];
        if (first?.type === "text") first.content = first.content.replace(/^\[([ xX])\]\s+/, (_m, c) => (c === " " ? "☐ " : "☑ "));
    }
});

const BLOCKS = {
    paragraph_open: () => ({ type: "paragraph", children: [] }),
    heading_open: token => ({ type: "heading", level: Number(token.tag.slice(1)), children: [] }),
    blockquote_open: () => ({ type: "blockquote", children: [] }),
    bullet_list_open: () => ({ type: "list", ordered: false, start: 1, children: [] }),
    ordered_list_open: token => ({ type: "list", ordered: true, start: Number(token.attrGet("start")) || 1, children: [] }),
    list_item_open: () => ({ type: "item", children: [] }),
};

/**
 * Parses markdown into block and inline nodes. A message that is a single
 * paragraph returns its inline nodes directly, like the desktop renderer.
 */
export function parseMarkdown(text, options = {}) {
    if (!text) return [];
    const root = [];
    const stack = [root];
    let table = null, row = null, inHead = false;
    for (const token of md.parse(String(text), {})) {
        const children = stack[stack.length - 1];
        if (BLOCKS[token.type]) {
            // Tight list items hide their paragraphs: the text sits in the item.
            if (token.hidden) continue;
            const node = BLOCKS[token.type](token);
            children.push(node);
            stack.push(node.children);
        } else if (token.nesting === -1 && BLOCKS[token.type.replace(/_close$/, "_open")]) {
            if (!token.hidden) stack.pop();
        } else if (token.type === "table_open") {
            table = { type: "table", header: [], rows: [] };
            children.push(table);
        } else if (token.type === "table_close") table = null;
        else if (token.type === "thead_open") inHead = true;
        else if (token.type === "thead_close") inHead = false;
        else if (token.type === "tr_open") row = [];
        else if (token.type === "tr_close") {
            if (inHead) table.header = row;
            else table.rows.push(row);
            row = null;
        } else if (token.type === "inline") {
            const nodes = inlineNodes(token.children || [], options);
            if (row) row.push(nodes);
            else children.push(...nodes);
        } else if (token.type === "fence") {
            children.push({ type: "code", code: token.content.replace(/\n$/, ""), language: ((token.info.match(/^[\w+#-]+/) || [""])[0]).toLowerCase() });
        } else if (token.type === "hr") children.push({ type: "hr" });
    }
    return root.length === 1 && root[0].type === "paragraph" ? root[0].children : root;
}

const INLINE = {
    strong_open: token => ({ type: token.markup === "__" ? "u" : "strong", children: [] }),
    em_open: () => ({ type: "em", children: [] }),
    s_open: () => ({ type: "del", children: [] }),
};

function inlineNodes(tokens, options) {
    const out = [];
    const stack = [out];
    let autolink = false;
    for (const token of tokens) {
        const children = stack[stack.length - 1];
        if (INLINE[token.type]) {
            const node = INLINE[token.type](token);
            children.push(node);
            stack.push(node.children);
        } else if (/^(strong|em|s)_close$/.test(token.type)) stack.pop();
        else if (token.type === "link_open") {
            const url = token.attrGet("href");
            // Bare URLs keep the shortened display; named links show their text.
            autolink = token.markup === "linkify" || token.markup === "autolink";
            const node = autolink ? { type: "link", url } : { type: "link", url, children: [] };
            children.push(node);
            stack.push(autolink ? [] : node.children);
        } else if (token.type === "link_close") {
            stack.pop();
            autolink = false;
        } else if (token.type === "text") {
            if (!autolink) children.push(...parseLeaves(token.content, options));
        } else if (token.type === "softbreak" || token.type === "hardbreak") children.push({ type: "br" });
        else if (token.type === "code_inline") children.push({ type: "inline-code", code: token.content });
        else if (token.type === "image") {
            // No remote fetch on render: an image is a link labelled with its alt text.
            const alt = (token.children || []).map(child => child.content).join("");
            children.push({ type: "link", url: token.attrGet("src"), children: [{ type: "text", text: alt || token.attrGet("src") }] });
        }
    }
    return out;
}

const LEAF = /:([a-z0-9_+-]+):|@([\p{L}\p{N}_.-]+)|\n/giu;

function parseLeaves(text, options) {
    const nodes = [];
    let last = 0;
    for (const m of text.matchAll(LEAF)) {
        if (m[0] === "\n") {
            if (m.index > last) nodes.push({ type: "text", text: text.slice(last, m.index) });
            nodes.push({ type: "br" });
            last = m.index + 1;
            continue;
        }
        if (m[1] !== undefined) {
            const name = m[1].toLowerCase();
            const node = options.customEmoji?.has(m[1]) ? { type: "custom-emoji", name: m[1] } : EMOJI[name] ? { type: "emoji", name, char: EMOJI[name] } : null;
            if (!node) continue;
            if (m.index > last) nodes.push({ type: "text", text: text.slice(last, m.index) });
            nodes.push(node);
            last = m.index + m[0].length;
            continue;
        }
        const mention = m[2];
        const special = ["here", "everyone", "channel"].includes(mention.toLowerCase());
        const uid = options.mentionNames?.get(mention) || options.mentionNames?.get(mention.toLowerCase());
        if (!special && !uid) continue;
        if (m.index > last) nodes.push({ type: "text", text: text.slice(last, m.index) });
        nodes.push({ type: "mention", name: mention, uid: uid || "", everyone: special });
        last = m.index + m[0].length;
    }
    if (last < text.length) nodes.push({ type: "text", text: text.slice(last) });
    return nodes;
}

const BLOCK_TYPES = new Set(["table", "code", "paragraph", "heading", "blockquote", "list", "item", "hr"]);

/** The plain text of a node tree, for previews and accessibility. */
export function plainText(nodes) {
    let out = "";
    nodes.forEach((node, index) => {
        const block = BLOCK_TYPES.has(node.type);
        if (block && out && !out.endsWith("\n")) out += "\n";
        out += nodeText(node);
        if (block && index < nodes.length - 1) out += "\n";
    });
    return out;
}

function nodeText(node) {
    return (() => {
        switch (node.type) {
            case "text": return node.text;
            case "code": return node.code;
            case "inline-code": return node.code;
            case "link": return node.children ? plainText(node.children) : node.url;
            case "hr": return "";
            case "emoji": return node.char;
            case "custom-emoji": return `:${node.name}:`;
            case "mention": return `@${node.name}`;
            case "br": return "\n";
            case "table": return [node.header, ...node.rows].map(row => row.map(plainText).join(" | ")).join("\n");
            default: return plainText(node.children || []);
        }
    })();
}

/** URLs linked in a message, in order, without duplicates. */
export function linksIn(nodes) {
    const out = [];
    const walk = list => {
        for (const node of list) {
            if (node.type === "link" && !out.includes(node.url)) out.push(node.url);
            if (node.children) walk(node.children);
            if (node.type === "table") [node.header, ...node.rows].forEach(row => row.forEach(walk));
        }
    };
    walk(nodes);
    return out;
}

/** Classifies a URL for the server's preview service. */
export function previewProvider(url) {
    let host;
    try { host = new URL(url).hostname.toLowerCase().replace(/^www\./, ""); } catch { return null; }
    if (host === "youtube.com" || host === "m.youtube.com" || host === "youtu.be") return "youtube";
    if (host === "x.com" || host === "twitter.com") return "x";
    if (host === "twitch.tv" || host === "m.twitch.tv" || host === "clips.twitch.tv") return "twitch";
    if (host === "vimeo.com") return "vimeo";
    if (host === "dailymotion.com" || host === "dai.ly") return "dailymotion";
    if (host === "whatnot.com") return "whatnot";
    return url.startsWith("https://") ? "web" : null;
}
