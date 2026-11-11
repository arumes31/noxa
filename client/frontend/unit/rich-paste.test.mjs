import assert from "node:assert/strict";
import test from "node:test";
import { bindRichPaste, hasFormatting, htmlToMarkdown, pasteFormatted } from "../src/rich-paste.js";
import { renderMarkdown } from "../src/markdown.js";

test("only real formatting tags trigger conversion", () => {
    assert.equal(hasFormatting("<p>Some <b>bold</b></p>"), true);
    assert.equal(hasFormatting('<a href="https://x.test">x</a>'), true);
    assert.equal(hasFormatting("<ul><li>a</li></ul>"), true);
    // An IDE or terminal copy is styled spans only: plain paste.
    assert.equal(hasFormatting('<div><span style="color:red">const</span> x</div>'), false);
    assert.equal(hasFormatting("<body><br></body>"), false, "<br> and <body> are not formatting");
    assert.equal(hasFormatting(""), false);
    assert.equal(hasFormatting(null), false);
});

test("pasted HTML becomes chat markdown that renders the same structure", async () => {
    const html = `<meta charset="utf-8"><h2>Plan</h2><p>Some <b>bold</b>, <i>it</i>, <u>under</u>, <s>gone</s> and <a href="https://example.test/a">a link</a>.</p>`
        + `<ul><li>one</li><li>two<ul><li>nested</li></ul></li></ul><ol><li>first</li></ol><blockquote>quoted</blockquote>`
        + `<pre><code>let x = 1;\nx++;</code></pre><table><thead><tr><th>A</th><th>B</th></tr></thead><tbody><tr><td>1</td><td>2</td></tr></tbody></table>`;
    const md = await htmlToMarkdown(html);
    assert.match(md, /^## Plan$/m);
    assert.match(md, /\*\*bold\*\*/);
    assert.match(md, /\*it\*/);
    assert.match(md, /__under__/);
    assert.match(md, /~~gone~~/);
    assert.match(md, /\[a link\]\(https:\/\/example\.test\/a\)/);
    assert.match(md, /^-\s+one$/m);
    assert.match(md, /^\s+-\s+nested$/m);
    assert.match(md, /^1\.\s+first$/m);
    assert.match(md, /^> quoted$/m);
    assert.match(md, /```\nlet x = 1;\nx\+\+;\n```/);
    assert.match(md, /\| A \| B \|/);
    assert.doesNotMatch(md, /\n{3,}/);

    const rendered = renderMarkdown(md);
    for (const tag of ["<h2>", "<strong>", "<em>", "<u>", "<del>", 'class="md-link"', "<ul>", "<ol>", "<blockquote>", 'class="md-pre', 'class="md-table"']) {
        assert.ok(rendered.includes(tag), `${tag} missing in ${rendered}`);
    }
});

test("unsafe links, images and active content are dropped", async () => {
    const md = await htmlToMarkdown(`<p><b>x</b> <a href="javascript:alert(1)">bad</a> <a href="/relative">rel</a> <a href="https://ok.test/p(1)">ok</a></p>`
        + `<img src="https://evil.test/p.png" alt="pic"><script>alert(1)</script><style>p{}</style>`);
    assert.match(md, /\bbad\b/);
    assert.doesNotMatch(md, /javascript:|\/relative|evil\.test|alert|p\{\}/);
    assert.match(md, /\[ok\]\(https:\/\/ok\.test\/p%281%29\)/, "parentheses cannot end the link early");
    assert.match(md, /\brel\b/);
});

test("a link whose text is its URL stays a bare URL", async () => {
    assert.equal(await htmlToMarkdown('<a href="https://example.test/">https://example.test/</a>'), "https://example.test/");
});

function pasteEvent(data, files = []) {
    let prevented = false;
    const inserted = [];
    const listeners = {};
    const input = {
        value: "say: ", selectionStart: 5, selectionEnd: 5, isConnected: true,
        ownerDocument: { execCommand: () => false },
        setSelectionRange(a, b) { this.selectionStart = a; this.selectionEnd = b; },
        focus() {},
        setRangeText(text, a, b) { inserted.push(text); this.value = this.value.slice(0, a) + text + this.value.slice(b); },
        dispatchEvent(event) { inserted.push(`event:${event.type}`); },
        addEventListener(type, fn) { listeners[type] = fn; },
    };
    const event = {
        currentTarget: input,
        clipboardData: { files, getData: (type) => data[type] || "" },
        preventDefault() { prevented = true; },
    };
    return { input, event, inserted, listeners, prevented: () => prevented };
}

test("a formatted paste is converted and inserted at the caret", async () => {
    const { input, event, inserted, prevented } = pasteEvent({ "text/html": "<b>hi</b> <a href=\"https://x.test/\">x</a>", "text/plain": "hi x" });
    assert.equal(pasteFormatted(event), true);
    assert.equal(prevented(), true);
    await new Promise(resolve => setTimeout(resolve, 50));
    assert.equal(input.value, "say: **hi** [x](https://x.test/)");
    assert.deepEqual(inserted.slice(-1), ["event:input"], "the composer hears an input event");
});

test("plain paste, files and unformatted HTML are left to the browser", () => {
    const plain = pasteEvent({ "text/html": "<b>hi</b>" });
    assert.equal(pasteFormatted(plain.event, true), false);
    assert.equal(plain.prevented(), false);
    const files = pasteEvent({ "text/html": "<b>hi</b>" }, [{ name: "a.png" }]);
    assert.equal(pasteFormatted(files.event), false);
    const spans = pasteEvent({ "text/html": "<span>code</span>" });
    assert.equal(pasteFormatted(spans.event), false);
});

test("Ctrl+Shift+V right before the paste keeps it plain", () => {
    const { input, event, listeners, prevented } = pasteEvent({ "text/html": "<b>hi</b>" });
    bindRichPaste(input);
    listeners.keydown({ ctrlKey: true, shiftKey: true, key: "V" });
    listeners.paste(event);
    assert.equal(prevented(), false);
});

test("the PWA copy of the paste converter is identical", async () => {
    const { readFile } = await import("node:fs/promises");
    const desktop = await readFile(new URL("../src/rich-paste.js", import.meta.url), "utf8");
    const pwa = await readFile(new URL("../../../mobile/src/core/rich-paste.js", import.meta.url), "utf8");
    assert.equal(pwa.replace(/\r\n/g, "\n"), desktop.replace(/\r\n/g, "\n"));
});

test("blank lines inside pasted code survive; prose spacing is compacted", async () => {
    const md = await htmlToMarkdown("<p><b>a</b></p><p><br></p><p><br></p><p>b</p><pre><code>x\n\n\n\ny</code></pre>");
    assert.match(md, /```\nx\n\n\n\ny\n```/);
    assert.doesNotMatch(md.split("```")[0], /\n{3,}/);
});
