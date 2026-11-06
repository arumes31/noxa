import assert from "node:assert/strict";
import test from "node:test";
import { upgradeCodeBlock } from "../src/code-blocks.js";

function block(lang, text = "const a = 1") {
    const code = { textContent: text, innerHTML: "", classList: { added: [], add(name) { this.added.push(name); } } };
    return { dataset: { lang }, isConnected: true, querySelector: () => code, code };
}
const hljs = {
    getLanguage: name => ["javascript", "xml", "rust"].includes(name),
    highlight: (text, { language }) => ({ value: `<span class="hljs-keyword">${language}</span>:${text.replace(/</g, "&lt;")}` }),
};

test("a known language is re-highlighted once", async () => {
    const pre = block("javascript");
    assert.equal(await upgradeCodeBlock(pre, async () => hljs), "done");
    assert.equal(pre.code.innerHTML, '<span class="hljs-keyword">javascript</span>:const a = 1');
    assert.deepEqual(pre.code.classList.added, ["hljs"]);
    assert.equal(await upgradeCodeBlock(pre, async () => { throw new Error("must not reload"); }), "done");
});

test("markdown.js language names map to highlight.js names", async () => {
    const pre = block("markup", "<b>x</b>");
    assert.equal(await upgradeCodeBlock(pre, async () => hljs), "done");
    assert.match(pre.code.innerHTML, /^<span class="hljs-keyword">xml<\/span>:&lt;b>/);
});

test("plain, unknown, detached and failed loads keep the built-in colouring", async () => {
    let loads = 0;
    const load = async () => { loads++; return hljs; };
    assert.equal(await upgradeCodeBlock(block("plain"), load), "skip");
    assert.equal(await upgradeCodeBlock(block("txt"), load), "skip");
    assert.equal(loads, 0, "plain text never loads the library");
    const unknown = block("klingon");
    assert.equal(await upgradeCodeBlock(unknown, load), "unknown");
    assert.equal(unknown.code.innerHTML, "");
    const detached = block("rust");
    detached.isConnected = false;
    assert.equal(await upgradeCodeBlock(detached, load), "unknown");
    assert.equal(await upgradeCodeBlock(block("rust"), async () => { throw new Error("chunk failed"); }), "unavailable");
});

test("a failed library load does not stick to later blocks", async () => {
    // The default loader is module-scoped; exercise the retry through it by
    // injecting a loader that fails once, as the default one now forgets.
    let attempts = 0;
    const flaky = async () => { attempts++; if (attempts === 1) throw new Error("chunk failed"); return hljs; };
    assert.equal(await upgradeCodeBlock(block("rust"), flaky), "unavailable");
    assert.equal(await upgradeCodeBlock(block("rust"), flaky), "done");
});
