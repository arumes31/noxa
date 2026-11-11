import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { EMOJI, bindImagePlaceholders, escapeHTML, loadImagePlaceholder, renderMarkdown } from "../src/markdown.js";

describe("escapeHTML", () => {
    it("escapes every HTML-significant character and coerces values", () => {
        assert.equal(escapeHTML(`<&>"'`), "&lt;&amp;&gt;&quot;&#39;");
        assert.equal(escapeHTML(42), "42");
    });
});

describe("renderMarkdown", () => {
    it("never lets sender text reference internal stash fragments", () => {
        const fragment = "`" + "x".repeat(64) + "`";
        const forged = "0".repeat(50);
        const html = renderMarkdown(fragment + forged);
        assert.equal(html.match(/md-code/g).length, 1);
    });

    it("returns an empty string for empty input", () => {
        assert.equal(renderMarkdown(""), "");
        assert.equal(renderMarkdown(null), "");
    });

    it("renders supported inline markup and emoji", () => {
        const html = renderMarkdown("**bold** __under__ ~~gone~~ *soft* :SMILE: :unknown:");
        assert.equal(
            html,
            "<strong>bold</strong> <u>under</u> <del>gone</del> <em>soft</em> 😄 :unknown:",
        );
        assert.equal(EMOJI.thumbsup, "👍");
    });

    it("keeps inline code opaque and escapes its contents", () => {
        assert.equal(
            renderMarkdown("`<img src=x onerror=alert(1)> **not bold**`"),
            '<code class="md-code">&lt;img src=x onerror=alert(1)&gt; **not bold**</code>',
        );
    });

    it("escapes raw HTML and rejects non-http image URLs", () => {
        const html = renderMarkdown('<script>alert("x")</script> ![x](javascript:alert(1))');
        assert.match(html, /&lt;script&gt;/);
        assert.doesNotMatch(html, /<script|<img/);
        assert.match(html, /!\[x\]\(javascript:alert\(1\)\)/);
    });

    it("renders safe images and autolinks with escaped query separators", () => {
        const html = renderMarkdown("![diagram](https://example.test/a.png) https://example.test/?a=1&b=2");
        assert.doesNotMatch(html, /<img/);
        assert.match(html, /<button type="button" class="md-img-load" data-img="\d+" data-alt="diagram" title="https:\/\/example\.test\/a\.png"/);
        assert.match(html, /class="md-link"/);
        assert.match(html, /data-url="https:\/\/example\.test\/\?a=1&amp;b=2"/);
    });

    it("renders valid tables and leaves malformed tables as text", () => {
        const valid = renderMarkdown("A | B\n--- | :---:\none | two");
        assert.match(valid, /<div class="md-table-wrap"><table class="md-table">/);
        assert.match(valid, /<th>A<\/th>\s*<th>B<\/th>/);
        assert.match(valid, /<td>one<\/td>\s*<td>two<\/td>/);
        assert.doesNotMatch(valid, /<br>|style=/, "no line breaks or inline alignment styles");

        // A single dash line under text is a CommonMark heading, not a table.
        const malformed = renderMarkdown("A | B\n---\none | two");
        assert.doesNotMatch(malformed, /<table/);
        assert.match(malformed, /<h2>A \| B<\/h2>/);
    });

    it("stops a table when a row has the wrong number of cells", () => {
        // GFM pads a short row; a blank line ends the table.
        const html = renderMarkdown("A | B\n--- | ---\none | two\nonly-one\n\nafter");
        assert.match(html, /<td>one<\/td>\s*<td>two<\/td>/);
        assert.match(html, /<td>only-one<\/td>\s*<td><\/td>/);
        assert.match(html, /<\/table><\/div>\s*<p>after<\/p>$/);
    });

    it("detects common fenced-code languages and highlights code", () => {
        const cases = [
            ["package main\nfunc main() {}", "go"],
            ["const answer = () => 42", "javascript"],
            ["def answer():\n  return 42", "python"],
            ["SELECT * FROM users", "sql"],
            [".card { color: red; }", "css"],
            ["<section>hello</section>", "markup"],
            ["ordinary text", "plain"],
        ];
        for (const [code, language] of cases) {
            assert.match(renderMarkdown("```\n" + code + "\n```"), new RegExp(`language-${language}`));
        }

        const highlighted = renderMarkdown('```js\nconst value = "x"; // note\n```');
        assert.match(highlighted, /class="tk-kw">const<\/span>/);
        assert.match(highlighted, /class="tk-str">&quot;x&quot;<\/span>/);
        assert.match(highlighted, /class="tk-com">\/\/ note<\/span>/);
    });

    it("accepts an unterminated fenced block and trims its final newline", () => {
        assert.equal(
            renderMarkdown("```txt\nhello\n"),
            '<pre class="md-pre language-txt" data-lang="txt"><code>hello</code><button type="button" class="md-copy" title="Copy code" aria-label="Copy code">⧉</button></pre>',
        );
    });

    it("numbers multi-line blocks outside the code element", () => {
        const html = renderMarkdown("```go\na := 1\nb := 2\nc := 3\n```");
        assert.match(html, /<span class="md-gutter" aria-hidden="true">1\n2\n3<\/span><code>/);
        assert.match(html, /data-lang="go"/);
        assert.doesNotMatch(html.split("<code>")[1], /md-gutter/, "line numbers never enter the copied code");
        assert.doesNotMatch(renderMarkdown("```\none line\n```"), /md-gutter/);
    });

    it("never autolinks inside image placeholder attributes", () => {
        const html = renderMarkdown('![see https://evil.test/x](https://example.test/a.png)');
        assert.doesNotMatch(html, /md-link/);
        assert.match(html, /data-alt="see https:\/\/evil\.test\/x"/);
        assert.equal((html.match(/"/g) || []).length % 2, 0);
    });
});

function fakePlaceholder(attributes) {
    const created = [];
    const button = {
        replaced: null,
        getAttribute: (name) => attributes[name] ?? null,
        replaceWith(node) { this.replaced = node; },
        ownerDocument: { createElement: (tag) => { const node = { tag }; created.push(node); return node; } },
    };
    return { button, created };
}

describe("image placeholders", () => {
    it("loads image bytes through the server without contacting the remote URL", async () => {
        globalThis.window = { __noxa: { state: { activeTabID: "tab", serverGeneration: 1 } }, go: { main: { App: { ImagePreviewForTab: async (tab, url) => { assert.equal(tab, "tab"); assert.match(url, /^https:/); return { thumbnail: "data:image/jpeg;base64,/9j/2Q==" }; } } } } };
        const id = renderMarkdown("![diagram](https://example.test/a.png?x=1&y=2)").match(/data-img="(\d+)"/)[1];
        const { button, created } = fakePlaceholder({ "data-img": id, "data-alt": "diagram" });
        button.isConnected = true;
        const img = await loadImagePlaceholder(button);
        assert.equal(created.length, 1);
        assert.equal(button.replaced, img);
        assert.equal(img.tag, "img");
        assert.equal(img.className, "md-img");
        assert.equal(img.src, "data:image/jpeg;base64,/9j/2Q==");
        assert.equal(img.alt, "diagram");
        assert.equal(img.referrerPolicy, "no-referrer");

        const unsafe = fakePlaceholder({ "data-src": "javascript:alert(1)", "data-img": "999999" });
        assert.equal(await loadImagePlaceholder(unsafe.button), null);
        assert.equal(unsafe.created.length, 0);
        assert.equal(await loadImagePlaceholder(null), null);
    });

    it("binds one delegated click handler per root", async () => {
        const listeners = [];
        const root = { addEventListener: (type, fn, capture) => listeners.push({ type, fn, capture }) };
        bindImagePlaceholders(root);
        bindImagePlaceholders(root);
        bindImagePlaceholders(null);
        assert.equal(listeners.length, 1);
        assert.equal(listeners[0].type, "click");

        const bid = renderMarkdown("![](https://example.test/b.png)").match(/data-img="(\d+)"/)[1];
        const { button } = fakePlaceholder({ "data-img": bid });
        let prevented = 0;
        let stopped = 0;
        const click = (target) => listeners[0].fn({
            target: { closest: () => target },
            preventDefault: () => { prevented++; },
            stopPropagation: () => { stopped++; },
        });
        click(null);
        assert.equal(prevented, 0);
        button.isConnected = true;
        click(button);
        await new Promise(resolve => setImmediate(resolve));
        assert.equal(prevented, 1);
        assert.equal(stopped, 1);
        assert.equal(button.replaced.src, "data:image/jpeg;base64,/9j/2Q==");
        delete globalThis.window;
    });
});

describe("full markdown", () => {
    it("keeps a single paragraph unwrapped and single newlines as line breaks", () => {
        assert.equal(renderMarkdown("hello\nworld"), "hello<br>\nworld");
        assert.match(renderMarkdown("one\n\ntwo"), /^<p>one<\/p>\s*<p>two<\/p>$/);
    });

    it("renders headings, lists, task items, quotes and rules", () => {
        const html = renderMarkdown("# Title\n\n- a\n  - nested\n- [x] done\n- [ ] open\n\n1. first\n2. second\n\n> quoted **bold**\n\n---");
        assert.match(html, /<h1>Title<\/h1>/);
        assert.doesNotMatch(html, /\sid=/, "headings get no ids");
        assert.match(html, /<ul>\s*<li>a\s*<ul>\s*<li>nested<\/li>/);
        assert.match(html, /<li>☑ done<\/li>/);
        assert.match(html, /<li>☐ open<\/li>/);
        assert.doesNotMatch(html, /<input/, "task boxes are display-only text");
        assert.match(html, /<ol>\s*<li>first<\/li>\s*<li>second<\/li>\s*<\/ol>/);
        assert.match(html, /<blockquote>\s*<p>quoted <strong>bold<\/strong><\/p>\s*<\/blockquote>/);
        assert.match(html, /<hr>/);
    });

    it("renders named links through the delegated link markup", () => {
        const html = renderMarkdown('[the docs](https://example.test/a?b=1&c=2 "ignored title") and <https://example.test/x>');
        assert.match(html, /<a href="#" class="md-link" data-url="https:\/\/example\.test\/a\?b=1&amp;c=2" title="https:\/\/example\.test\/a\?b=1&amp;c=2">the docs<\/a>/);
        assert.doesNotMatch(html, /ignored title/);
        assert.match(html, /data-url="https:\/\/example\.test\/x"/);
    });

    it("never renders raw HTML, script URLs or attribute breakouts", () => {
        const corpus = [
            "[x](javascript:alert(1))",
            "[x](JaVaScRiPt:alert(1))",
            "[x](data:text/html;base64,PHNjcmlwdD4=)",
            "[x](vbscript:msgbox(1))",
            "[x](//evil.test/path)",
            "<img src=x onerror=alert(1)>",
            "<a href=\"javascript:alert(1)\">x</a>",
            '[x" onmouseover="alert(1)](https://example.test/)',
            '[x](https://example.test/" onmouseover="alert(1))',
            '![a" onerror="alert(1)](https://example.test/a.png)',
            "![x](javascript:alert(1))",
            "<javascript:alert(1)>",
            "**<b>**[*x*](https://example.test/)",
            "> - [x](javascript:alert(1))\n>   - <script>alert(1)</script>",
            "| a |\n| - |\n| <script>alert(1)</script> |",
        ];
        for (const source of corpus) {
            const html = renderMarkdown(source);
            assert.doesNotMatch(html, /<(script|img|iframe|svg|object|embed|input)\b/i, source);
            assert.doesNotMatch(html, /<[^>]*\son[a-z]+="/i, source); // a real attribute needs an unescaped quote
            assert.doesNotMatch(html, /href="(?!#")/i, source);
            assert.doesNotMatch(html, /data-url="(?!https?:\/\/)/i, source);
            assert.doesNotMatch(html, /title="(?!https?:\/\/|Copy code)/i, source);
        }
    });

    it("keeps underline, strikethrough, nested emphasis and backslash escapes", () => {
        assert.equal(renderMarkdown("__u__ ~~s~~ ***both*** \\*literal\\*"), "<u>u</u> <del>s</del> <em><strong>both</strong></em> *literal*");
    });

    it("leaves indented text alone and bare domains unlinked", () => {
        assert.doesNotMatch(renderMarkdown("intro\n\n    indented prose"), /<pre/);
        assert.doesNotMatch(renderMarkdown("see example.test or mail@example.test"), /md-link/);
    });
});
