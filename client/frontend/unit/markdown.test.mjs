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
        assert.match(valid, /<table class="md-table">/);
        assert.match(valid, /<th>A<\/th><th>B<\/th>/);
        assert.match(valid, /<td>one<\/td><td>two<\/td>/);
        assert.doesNotMatch(valid, /<br>/);

        const malformed = renderMarkdown("A | B\n---\none | two");
        assert.doesNotMatch(malformed, /<table/);
        assert.match(malformed, /A \| B<br>---<br>one \| two/);
    });

    it("stops a table when a row has the wrong number of cells", () => {
        const html = renderMarkdown("A | B\n--- | ---\none | two\nonly-one");
        assert.match(html, /<td>one<\/td><td>two<\/td>/);
        assert.match(html, /<\/div>only-one$/);
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
            '<pre class="md-pre language-txt"><code>hello</code></pre>',
        );
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
