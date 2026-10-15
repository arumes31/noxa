import assert from "node:assert/strict";
import { test, afterEach } from "node:test";
import { saveStreamDiagnostics } from "../src/stream-diagnostic-export.js";
const old = globalThis.window;
afterEach(() => { if (old === undefined) delete globalThis.window; else globalThis.window = old; });
function setup(fn) { const messages = []; globalThis.window = { go: { main: { App: { SaveStreamDiagnostics: fn } } }, __noxa: { toast: (...args) => messages.push(args) } }; return messages; }
test("save preserves the exact snapshot and distinguishes cancellation", async () => {
 const raw = '{\n  "recording": {}\n}';
 for (const status of ["saved", "cancelled"]) { const messages = setup(async value => { assert.equal(value, raw); return status; }); assert.equal(await saveStreamDiagnostics(raw), status); assert.equal(messages.length, 1); }
});
test("pending native save admits no second dialog and stale feedback is suppressed", async () => {
 let finish, calls = 0, current = true;
 const messages = setup(() => { calls++; return new Promise(resolve => { finish = resolve; }); });
 const first = saveStreamDiagnostics("{}", { isCurrent: () => current });
 assert.equal(await saveStreamDiagnostics("{}"), "busy"); assert.equal(calls, 1);
 current = false; finish("saved"); assert.equal(await first, "saved"); assert.deepEqual(messages, []);
});
test("native errors and unsupported bridges expose no raw text or fallback", async () => {
 for (const fn of [undefined, async () => { throw new Error("secret path"); }, async () => "secret path"]) {
 const messages = setup(fn); assert.equal(await saveStreamDiagnostics("{}"), "error"); assert.equal(messages.length, 1); assert.doesNotMatch(JSON.stringify(messages), /secret/);
 }
});
