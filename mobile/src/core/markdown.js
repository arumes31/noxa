// markdown.js — the chat's mini-markdown, parsed into a tree instead of
// HTML so the screens can build DOM nodes with textContent only. The syntax
// matches the desktop (client/frontend/src/markdown.js): fenced and inline
// code, **bold**, __underline__, ~~strike~~, *italic*, http(s) autolinks,
// :shortcode: emoji and pipe tables.
//
// Nodes: {type:"text", text} | {type:"code", code, language} (block) |
// {type:"inline-code", code} | {type:"strong"|"u"|"del"|"em", children} |
// {type:"link", url} | {type:"emoji", name, char} | {type:"custom-emoji", name} |
// {type:"br"} | {type:"table", header:[cells], rows:[[cells]]} where cells are
// node arrays.

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

const INLINE_CODE = /`([^`\n]+)`/;
const URL_RE = /https?:\/\/[^\s<>"'\]}]+/;

function cells(line) {
    return line.trim().replace(/^\||\|$/g, "").split("|").map(cell => cell.trim());
}

/** Splits text into block nodes, parsing inline markup within paragraphs. */
export function parseMarkdown(text, options = {}) {
    if (!text) return [];
    const out = [];
    const source = String(text).replace(/\r\n?/g, "\n");
    const fence = /```(\w*)\n?([\s\S]*?)(?:```|$)/g;
    let last = 0;
    let match;
    while ((match = fence.exec(source))) {
        if (match.index > last) pushParagraphs(out, source.slice(last, match.index), options);
        const code = match[2].replace(/\n$/, "");
        out.push({ type: "code", code, language: (match[1] || "").toLowerCase() });
        last = match.index + match[0].length;
    }
    if (last < source.length) pushParagraphs(out, source.slice(last), options);
    return out;
}

function pushParagraphs(out, text, options) {
    const lines = text.split("\n");
    let buffer = [];
    const flushBuffer = () => {
        if (!buffer.length) return;
        const joined = buffer.join("\n");
        buffer = [];
        // Trim a single leading/trailing newline from around fences.
        const nodes = parseInline(joined.replace(/^\n/, "").replace(/\n$/, ""), options);
        if (nodes.length) out.push(...nodes);
    };
    for (let i = 0; i < lines.length; i++) {
        const header = cells(lines[i]);
        const separator = i + 1 < lines.length ? cells(lines[i + 1]) : [];
        const isTable = lines[i].includes("|") && header.length > 1 && separator.length === header.length && separator.every(cell => /^:?-+:?$/.test(cell));
        if (!isTable) {
            buffer.push(lines[i]);
            continue;
        }
        flushBuffer();
        const table = { type: "table", header: header.map(cell => parseInline(cell, options)), rows: [] };
        i += 2;
        while (i < lines.length && lines[i].includes("|")) {
            const row = cells(lines[i]);
            if (row.length !== header.length) break;
            table.rows.push(row.map(cell => parseInline(cell, options)));
            i++;
        }
        i--;
        out.push(table);
    }
    flushBuffer();
}

/**
 * Parses inline markup. options.customEmoji is a Set of names the server
 * knows; those become custom-emoji nodes, other :names: use the built-in map.
 * options.mentionNames maps "@name" strings to unique IDs for mention nodes.
 */
export function parseInline(text, options = {}) {
    const nodes = [];
    let rest = text;
    while (rest.length) {
        const code = INLINE_CODE.exec(rest);
        const url = URL_RE.exec(rest);
        const next = [code, url].filter(Boolean).sort((a, b) => a.index - b.index)[0];
        if (!next) {
            nodes.push(...parseEmphasis(rest, options));
            break;
        }
        if (next.index > 0) nodes.push(...parseEmphasis(rest.slice(0, next.index), options));
        if (next === code) nodes.push({ type: "inline-code", code: code[1] });
        else {
            // A trailing ) or . after a URL is usually punctuation, not part of it.
            let link = url[0];
            while (/[.,;:!?)]$/.test(link) && !(link.endsWith(")") && (link.match(/\(/g) || []).length >= (link.match(/\)/g) || []).length)) link = link.slice(0, -1);
            nodes.push({ type: "link", url: link });
            rest = rest.slice(next.index + link.length);
            continue;
        }
        rest = rest.slice(next.index + next[0].length);
    }
    return nodes;
}

const EMPHASIS = [
    { re: /\*\*((?:[^*\n]|\*[^*\n]+\*)+?)\*\*/, type: "strong" },
    { re: /__([^_\n]+)__/, type: "u" },
    { re: /~~([^~\n]+)~~/, type: "del" },
    { re: /(^|[\s(])\*([^*\n]+)\*(?=[\s).,;!?]|$)/, type: "em", prefix: true },
];

function parseEmphasis(text, options) {
    let first = null;
    for (const rule of EMPHASIS) {
        const m = rule.re.exec(text);
        if (m && (!first || m.index < first.m.index)) first = { m, rule };
    }
    if (!first) return parseLeaves(text, options);
    const { m, rule } = first;
    const before = rule.prefix ? text.slice(0, m.index) + m[1] : text.slice(0, m.index);
    const inner = rule.prefix ? m[2] : m[1];
    const after = text.slice(m.index + m[0].length);
    return [...parseLeaves(before, options), { type: rule.type, children: parseEmphasis(inner, options) }, ...parseEmphasis(after, options)];
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

/** The plain text of a node tree, for previews and accessibility. */
export function plainText(nodes) {
    let out = "";
    nodes.forEach((node, index) => {
        const block = node.type === "table" || node.type === "code";
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
            case "link": return node.url;
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
