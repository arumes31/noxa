import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import vm from "node:vm";

// Any stubbed value: callable, constructible, every property is another stub,
// iteration ends at once and string conversion is empty.
const any = new Proxy(function () {}, {
    get: (_, key) => key === Symbol.toPrimitive ? () => "" : key === "then" ? undefined : key === "done" ? true : any,
    apply: () => any, construct: () => any,
});
// A stub that remembers the properties written to it (DOM nodes).
const node = () => { const props = {}; return new Proxy(props, { get: (_, key) => key in props ? props[key] : any, set: (_, key, value) => { props[key] = value; return true; } }); };

test("a channel attachment that fails retryably is queued in the outbox with its caption", async () => {
    const source = await fs.readFile(new URL("../src/ui/chat.js", import.meta.url), "utf8");
    let upload, input;
    const queued = [];
    const session = new Proxy({
        channels: () => [], messages: new Map(), pins: new Map(), conn: {},
        send: async () => { throw Object.assign(new Error("offline"), { retryable: true }); },
    }, { get: (target, key) => key in target ? target[key] : any });
    const overrides = {
        "../app.js": { app: { session }, queueMessage: async (...args) => { queued.push(args); } },
        "../core/settings.js": { get: () => 0, onChange: () => () => {} },
        "../core/files.js": { uploadAttachment: async () => "[file:token]" },
        "./dom.js": { h: tag => { const element = node(); if (tag === "textarea") input = element; return element; } },
        "./composer-extras.js": { attachControls: callback => { upload = callback; return { button: any, picker: any }; } },
    };
    const context = vm.createContext({ crypto: globalThis.crypto, setTimeout, clearTimeout, setInterval: () => 0, clearInterval() {},
        document: any, window: any, matchMedia: any, requestAnimationFrame: any, IntersectionObserver: any, ResizeObserver: any,
        MutationObserver: any, Event: class {}, CustomEvent: class {}, navigator: any, localStorage: any, performance });
    const module = new vm.SourceTextModule(source, { context });
    await module.link(specifier => {
        const names = [...source.matchAll(/^import\s*(?:\{([^}]*)\}|\*\s+as\s+\w+)?\s*(?:from\s*)?"([^"]+)"/gm)]
            .filter(match => match[2] === specifier)
            .flatMap(match => (match[1] || "").split(",").map(name => name.trim().split(/\s+as\s+/)[0]).filter(Boolean));
        const exports = { ...Object.fromEntries(names.map(name => [name, any])), ...overrides[specifier] };
        return new vm.SyntheticModule(Object.keys(exports), function () {
            for (const [key, value] of Object.entries(exports)) this.setExport(key, value);
        }, { context });
    });
    await module.evaluate();
    module.namespace.renderChat(node(), 4);
    input.value = "look";
    await upload({ name: "a.png" });
    assert.equal(queued.length, 1, "a retryable failure goes to the outbox instead of sticking");
    assert.equal(JSON.stringify(queued[0].slice(0, 2)), JSON.stringify([{ kind: "channel", scope: 4 }, "look [file:token]"]));
    assert.equal(queued[0][2].replyTo, 0);
    assert.equal(input.value, "");
});
