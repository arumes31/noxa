import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import vm from "node:vm";

// Loads ui/attachments.js with stubbed imports and a counting object-URL API.
async function load(shown) {
    const revoked = [];
    let urls = 0;
    const sources = {
        "../app.js": { app: { addEventListener() {} } },
        "../core/files.js": { fetchAttachment: (_conn, _channel, ref) => ref.plain ? Promise.resolve(ref.plain) : new Promise(() => {}) },
        "../core/crypto.js": { toBase64: String },
        "../core/message-format.js": { formatSize: String, mediaKind: () => "", mimeOf: () => "" },
        "../core/settings.js": { get: () => false },
        "../i18n.js": { t: key => key },
        "./dom.js": { h() {}, toast() {} },
        "../core/voice-notes.js": { audioMime: () => "" },
        "./voice-note.js": { voiceNotePlayer() {} },
        "./rich-strings.js": {},
        "./rich-text.js": { messageBody() {} },
    };
    const context = vm.createContext({
        Blob: class { constructor([plain]) { this.size = plain.byteLength; } },
        URL: { createObjectURL: () => `blob:${++urls}`, revokeObjectURL: url => revoked.push(url) },
        document: { querySelectorAll: () => shown.map(src => ({ src, dataset: {} })) },
    });
    const module = new vm.SourceTextModule(await fs.readFile(new URL("../src/ui/attachments.js", import.meta.url), "utf8"), { context });
    await module.link(name => new vm.SyntheticModule(Object.keys(sources[name]), function () {
        for (const [key, value] of Object.entries(sources[name])) this.setExport(key, value);
    }, { context }));
    await module.evaluate();
    return { attachmentURL: module.namespace.attachmentURL, revoked };
}

test("the decrypted attachment cache keeps recent, loading and visible media within its caps", async () => {
    const shown = [];
    const { attachmentURL, revoked } = await load(shown);
    const get = (storage, bytes = 1) => attachmentURL({ storage, master: "k", plain: bytes && { byteLength: bytes } }, { channelID: 1 });
    get("loading", 0); // never finishes decrypting
    const first = await get("first");
    shown.push(first.url); // still on screen
    const second = await get("second");
    const fill = [];
    for (let i = 0; i < 77; i++) fill.push(await get(`fill${i}`));
    assert.equal(await get("second"), second, "a hit returns the cached entry");
    await get("third");
    assert.deepEqual(revoked, [fill[0].url], "the least recently used, finished, hidden entry goes first");
    await get("big", 150 * 1024 * 1024);
    assert.ok(revoked.includes(second.url), "the byte cap evicts older entries too");
    assert.ok(!revoked.includes(first.url), "a URL still on screen is never revoked");
    assert.equal(await get("first"), first);
});
