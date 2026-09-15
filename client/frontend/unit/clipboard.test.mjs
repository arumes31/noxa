import assert from "node:assert/strict";
import { afterEach, test } from "node:test";
import { copyToClipboard } from "../src/clipboard.js";

const originals = new Map(["window", "navigator"].map(key => [key, Object.getOwnPropertyDescriptor(globalThis, key)]));
afterEach(() => {
    for (const [key, descriptor] of originals) {
        if (descriptor) Object.defineProperty(globalThis, key, descriptor);
        else delete globalThis[key];
    }
});

function setup(native, browser) {
    const messages = [];
    Object.defineProperty(globalThis, "window", { configurable: true, value: {
        runtime: native ? { ClipboardSetText: native } : {},
        __voicx: { toast: (...args) => messages.push(args) },
    } });
    Object.defineProperty(globalThis, "navigator", { configurable: true, value: {
        clipboard: browser ? { writeText: browser } : undefined,
    } });
    return messages;
}

test("clipboard prefers native writes and reports success", async () => {
    const messages = setup(async value => { assert.equal(value, "a value"); return true; }, () => assert.fail("browser called"));
    assert.equal(await copyToClipboard("a value", { success: "ID copied" }), true);
    assert.deepEqual(messages, [["ID copied"]]);
});

test("clipboard uses the browser when the native bridge is absent", async () => {
    const messages = setup(null, async value => assert.equal(value, "a value"));
    assert.equal(await copyToClipboard("a value", { success: "Copied" }), true);
    assert.deepEqual(messages, [["Copied"]]);
});

test("clipboard failures do not leak values or raw errors", async () => {
    for (const native of [() => false, () => { throw new Error("secret"); }, async () => { throw new Error("secret"); }]) {
        const messages = setup(native, () => assert.fail("failed native writes must not be retried silently"));
        assert.equal(await copyToClipboard("secret"), false);
        assert.deepEqual(messages, [["Could not copy to the clipboard.", "warn"]]);
    }
    for (const browser of [undefined, async () => { throw new Error("secret"); }]) {
        const messages = setup(null, browser);
        assert.equal(await copyToClipboard("secret"), false);
        assert.deepEqual(messages, [["Could not copy to the clipboard.", "warn"]]);
    }
});

test("clipboard suppresses feedback after the initiating view changes", async () => {
    for (const succeeds of [true, false]) {
        let current = true;
        const messages = setup(async () => { current = false; return succeeds; });
        assert.equal(await copyToClipboard("secret", { isCurrent: () => current }), succeeds);
        assert.deepEqual(messages, []);
    }
});
