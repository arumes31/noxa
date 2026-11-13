import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import vm from "node:vm";

// A hold released while the microphone prompt is open stops that recording
// once it starts, but must not stop every later "Record again".

const any = new Proxy(function () {}, {
    get: (_, key) => key === Symbol.toPrimitive ? () => "" : key === "then" ? undefined : any,
    apply: () => any, construct: () => any,
});

test("an early hold release stops only the first recording, not Record again", async () => {
    const source = await fs.readFile(new URL("../src/ui/voice-note.js", import.meta.url), "utf8");
    const buttons = []; // [label, onclick] of recorder buttons
    const h = (tag, props = {}, ...children) => {
        const listeners = {};
        const classes = new Set();
        if (tag === "button") buttons.push([children.flat()[0], props?.onclick]);
        const target = {
            addEventListener: (type, fn) => { listeners[type] = fn; },
            fire: (type, event) => listeners[type](event),
            classList: { add: c => classes.add(c), remove: c => classes.delete(c), contains: c => classes.has(c) },
        };
        return new Proxy(target, { get: (o, key) => key in o ? o[key] : any, set: (o, key, value) => { o[key] = value; return true; } });
    };
    const prompts = [];
    const recorders = [];
    class MediaRecorder {
        constructor() { this.state = "inactive"; this.mimeType = "audio/webm"; recorders.push(this); }
        start() { this.state = "recording"; }
        stop() {
            if (this.state === "inactive") return;
            this.state = "inactive";
            this.ondataavailable({ data: new Blob(["x"]) });
            this.onstop?.();
        }
    }
    const navigator = { mediaDevices: { getUserMedia: () => new Promise(resolve => prompts.push(resolve)) }, vibrate() {} };
    const overrides = {
        "./dom.js": { h, toast() {} },
        "../i18n.js": { t: key => key },
        "./conversation-tools.js": { panel: () => ({ close() {}, closed: new Promise(() => {}) }) },
        "../core/voice-notes.js": { pickVoiceMime: () => "audio/webm", formatDuration: () => "0:00", voiceNoteName: () => "v.webm", VOICE_MAX_MS: 300000, VOICE_MAX_BYTES: 1e9 },
    };
    const context = vm.createContext({ Blob, File, Promise, setTimeout: fn => { fn(); return 1; }, clearTimeout() {}, setInterval: () => 0, clearInterval() {},
        MediaRecorder, navigator, document: any, URL: any, Audio: any });
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
    const tick = () => new Promise(resolve => setImmediate(resolve));
    const grant = async () => { prompts.shift()({ getTracks: () => [] }); await tick(); };

    const mic = module.namespace.voiceNoteButton({});
    mic.fire("pointerdown", { button: 0, pointerId: 1, clientX: 0 });
    mic.fire("pointerup", { pointerId: 1 }); // released before the microphone answered
    await grant();
    assert.equal(recorders.length, 1);
    assert.equal(recorders[0].state, "inactive", "the early-released recording stops at once");

    const again = buttons.findLast(([label]) => label === "vnote.again");
    assert.ok(again, "the preview offers Record again");
    again[1]();
    await grant();
    assert.equal(recorders.length, 2);
    assert.equal(recorders[1].state, "recording", "Record again keeps recording");
});
