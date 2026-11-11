// rich-paste.js — formatted text pasted into the composer arrives as HTML on
// the clipboard; this turns it into the chat's markdown so bold, links,
// lists, headings, quotes, code and tables survive the paste. Turndown loads
// on the first formatted paste only. Shared with the PWA as a copy
// (mobile/src/core/rich-paste.js); keep both identical.

// Real formatting tags. Clipboard HTML made only of styled spans and divs (an
// IDE or terminal copy) pastes as plain text instead of escaped markdown.
const FORMATTED = /<(b|strong|i|em|u|ins|s|del|strike|a|h[1-6]|ul|ol|li|blockquote|pre|table|hr)[\s>]/i;

/** True when clipboard HTML carries formatting worth converting. */
export function hasFormatting(html) {
    return typeof html === "string" && FORMATTED.test(html);
}

let service = null;

async function turndown() {
    if (service) return service;
    const [{ default: TurndownService }, gfm] = await Promise.all([import("turndown"), import("turndown-plugin-gfm")]);
    const td = new TurndownService({ headingStyle: "atx", codeBlockStyle: "fenced", bulletListMarker: "-", emDelimiter: "*", strongDelimiter: "**", hr: "---" });
    td.use([gfm.tables, gfm.taskListItems]);
    // Turndown pads list markers to four columns; chat text reads better with
    // one space. Nested lines indent by the marker's width so they still nest.
    td.addRule("listItem", {
        filter: "li",
        replacement: (content, node, options) => {
            const parent = node.parentNode;
            let prefix = `${options.bulletListMarker} `;
            if (parent.nodeName === "OL") prefix = `${Number(parent.getAttribute("start") || 1) + Array.prototype.indexOf.call(parent.children, node)}. `;
            const body = content.replace(/^\n+/, "").replace(/\n+$/, "\n").replace(/\n/gm, `\n${" ".repeat(prefix.length)}`);
            return prefix + body + (node.nextSibling && !/\n$/.test(body) ? "\n" : "");
        },
    });
    // The plugin's single-tilde strikethrough is not what the renderer reads.
    td.addRule("strike", { filter: ["del", "s", "strike"], replacement: content => (content.trim() ? `~~${content}~~` : content) });
    // __text__ is underline in noXa markdown.
    td.addRule("underline", { filter: ["u", "ins"], replacement: content => (content.trim() ? `__${content}__` : content) });
    // Only http(s) links render; other targets keep their text.
    td.addRule("safeLinks", {
        filter: node => node.nodeName === "A",
        replacement: (content, node) => {
            const href = node.getAttribute("href") || "";
            if (!/^https?:\/\//i.test(href) || !content.trim()) return content;
            return content.trim() === href ? href : `[${content}](${href.replace(/\(/g, "%28").replace(/\)/g, "%29")})`;
        },
    });
    // Pasted pictures are files; remote image URLs and active content are
    // dropped. addRule beats turndown's built-in image rule, remove() does not.
    td.addRule("dropMedia", {
        filter: ["img", "picture", "svg", "video", "audio", "iframe", "object", "embed", "script", "style", "noscript", "meta", "link", "head", "title"],
        replacement: () => "",
    });
    service = td;
    return td;
}

/** Converts clipboard HTML to chat markdown. */
export async function htmlToMarkdown(html) {
    const td = await turndown();
    return td.turndown(html).replace(/\n{3,}/g, "\n\n").trim();
}

/**
 * Converts a paste of formatted HTML into markdown at the caret. Returns true
 * when it took over the paste. plain skips conversion (Ctrl+Shift+V).
 */
export function pasteFormatted(event, plain = false) {
    const html = event.clipboardData?.getData("text/html");
    if (plain || event.clipboardData?.files?.length || !hasFormatting(html)) return false;
    event.preventDefault();
    const input = event.currentTarget;
    const { selectionStart: start, selectionEnd: end, value } = input;
    const fallback = event.clipboardData.getData("text/plain");
    void htmlToMarkdown(html).catch(() => fallback).then((md) => {
        if (!input.isConnected) return;
        const text = md || fallback;
        // The converter loads on first use; text typed meanwhile keeps the caret.
        if (input.value === value) input.setSelectionRange(start, end);
        input.focus();
        // insertText keeps the paste undoable and fires the field's input event.
        if (!input.ownerDocument.execCommand?.("insertText", false, text)) {
            input.setRangeText(text, input.selectionStart, input.selectionEnd, "end");
            input.dispatchEvent(new Event("input", { bubbles: true }));
        }
    });
    return true;
}

/** Formatted paste for a composer; Ctrl+Shift+V stays a plain paste. */
export function bindRichPaste(input) {
    let plain = false;
    input.addEventListener("keydown", (event) => {
        plain = (event.ctrlKey || event.metaKey) && event.shiftKey && event.key.toLowerCase() === "v";
    });
    input.addEventListener("paste", (event) => {
        const wasPlain = plain;
        plain = false;
        pasteFormatted(event, wasPlain);
    });
}
