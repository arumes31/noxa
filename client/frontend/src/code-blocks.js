// code-blocks.js — copy buttons and full syntax highlighting for rendered
// code blocks (603). markdown.js renders immediately with its small built-in
// highlighter; this module then loads highlight.js (about 190 languages) on
// first use and re-highlights blocks whose language it knows. highlight.js
// escapes the code it returns, so its markup is as safe as the escaped text
// it replaces. Nothing is fetched: the library is a local build chunk.
import { copyToClipboard } from "./clipboard.js";
import { t } from "./i18n.js";

// Languages from markdown.js's own detection that highlight.js names differently.
const ALIASES = Object.freeze({ markup: "xml" });
const SKIP = new Set(["", "plain", "txt", "text"]);

let library = null;
// A failed chunk load is forgotten, so a later block can try again.
const loadLibrary = () => (library ||= import("highlight.js").then(module => module.default, error => {
    library = null;
    throw error;
}));

/** Re-highlights one rendered block. Returns the state it ends in. */
export async function upgradeCodeBlock(pre, load = loadLibrary) {
    if (!pre?.dataset || pre.dataset.hl) return pre?.dataset?.hl || "";
    const language = ALIASES[pre.dataset.lang] || pre.dataset.lang || "";
    if (SKIP.has(language)) return (pre.dataset.hl = "skip");
    pre.dataset.hl = "pending";
    let hljs;
    try {
        hljs = await load();
    } catch {
        return (pre.dataset.hl = "unavailable"); // the built-in colouring stays
    }
    const code = pre.querySelector("code");
    if (!code || !pre.isConnected || !hljs.getLanguage(language)) return (pre.dataset.hl = "unknown");
    code.innerHTML = hljs.highlight(code.textContent, { language, ignoreIllegals: true }).value;
    code.classList.add("hljs");
    return (pre.dataset.hl = "done");
}

/** Copies a block's code (never its line numbers). */
export function copyCodeBlock(button) {
    const code = button.closest(".md-pre")?.querySelector("code");
    if (!code) return Promise.resolve(false);
    return copyToClipboard(code.textContent, { success: t("chat.codeCopied") });
}

/**
 * Installs one delegated copy handler on root and upgrades code blocks as
 * they are rendered anywhere below it.
 */
export function bindCodeBlocks(root = document, { observe = globalThis.MutationObserver, load = loadLibrary } = {}) {
    if (!root?.addEventListener || root.__noxaCodeBlocks) return;
    root.__noxaCodeBlocks = true;
    root.addEventListener("click", event => {
        const button = event.target?.closest?.(".md-copy");
        if (!button) return;
        event.preventDefault();
        event.stopPropagation();
        void copyCodeBlock(button);
    }, true);
    const upgradeWithin = node => {
        if (node?.nodeType !== 1) return;
        const blocks = node.matches?.("pre.md-pre[data-lang]") ? [node] : [...(node.querySelectorAll?.("pre.md-pre[data-lang]") || [])];
        for (const pre of blocks) void upgradeCodeBlock(pre, load);
    };
    upgradeWithin(root.body || root.documentElement || root);
    if (!observe) return;
    new observe(records => {
        for (const record of records) for (const node of record.addedNodes) upgradeWithin(node);
    }).observe(root.body || root, { childList: true, subtree: true });
}
