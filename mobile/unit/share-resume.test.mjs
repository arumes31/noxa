// Retrying a share that failed halfway resumes after the files already sent.
import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import vm from "node:vm";

const any = new Proxy(function () {}, {
    get: (_, key) => key === Symbol.toPrimitive ? () => "" : key === "then" ? undefined : any,
    apply: () => any, construct: () => any,
});

test("a retried multi-file share sends only what is left, the caption once", async () => {
    const source = await fs.readFile(new URL("../src/ui/share.js", import.meta.url), "utf8");
    const sent = [];
    let failOn = "b";
    const session = { conn: {}, send: async (scope, body) => {
        if (failOn && body.includes(`[${failOn}]`)) { failOn = ""; throw new Error("offline"); }
        sent.push(body);
    } };
    const stored = [];
    const overrides = {
        "../core/files.js": { uploadAttachment: async (conn, location, file) => `[${file.name}]` },
        "../core/share-store.js": { putShare: async s => { stored.push(s.files.map(f => f.name).join()); } },
    };
    const context = vm.createContext({});
    const module = new vm.SourceTextModule(source, { context });
    await module.link(specifier => {
        const names = [...source.matchAll(/^import\s*(?:\{([^}]*)\})?\s*(?:from\s*)?"([^"]+)"/gm)]
            .filter(match => match[2] === specifier)
            .flatMap(match => (match[1] || "").split(",").map(name => name.trim()).filter(Boolean));
        const exports = { ...Object.fromEntries(names.map(name => [name, any])), ...overrides[specifier] };
        return new vm.SyntheticModule(Object.keys(exports), function () {
            for (const [key, value] of Object.entries(exports)) this.setExport(key, value);
        }, { context });
    });
    await module.evaluate();
    const { deliver } = module.namespace;
    const share = { text: "look", files: [{ name: "a" }, { name: "b" }, { name: "c" }] };
    const target = { kind: "channel", scope: 4 };
    await assert.rejects(deliver(session, target, share), /offline/);
    assert.deepEqual(sent, ["look [a]"]);
    assert.equal(share.text, "");
    assert.deepEqual(share.files.map(f => f.name), ["b", "c"]);
    assert.deepEqual(stored, ["b,c"], "progress is stored for a remount after reconnect");
    await deliver(session, target, share);
    assert.deepEqual(sent, ["look [a]", "[b]", "[c]"]);
    assert.equal(share.files.length, 0);
});
