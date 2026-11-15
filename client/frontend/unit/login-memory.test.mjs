import test from "node:test";
import assert from "node:assert/strict";

test("selecting an invite's server drops passwords typed for another server", async () => {
    const lookups = [];
    globalThis.window = { addEventListener() {}, go: { main: { App: {
        GetLoginPasswordStatus: async (addr, nick) => { lookups.push([addr, nick]); return { supported: true, available: true }; },
    } } } };
    const { createLoginMemory } = await import("../src/login-memory.js");
    const elements = new Map();
    const $ = id => {
        if (!elements.has(id)) elements.set(id, { value: "", placeholder: "", textContent: "", checked: false, disabled: false, addEventListener() {} });
        return elements.get(id);
    };
    const memory = createLoginMemory({ $, settings: () => null, clearBookmark() {} });
    Object.assign($("login-addr"), { value: "a.example" });
    Object.assign($("login-nick"), { value: "alice" });
    Object.assign($("login-accountpw"), { value: "secret-for-a" });
    Object.assign($("login-serverpw"), { value: "server-secret-for-a" });
    const invite = "noxa://b.example/invite/3#" + "A".repeat(43);
    await memory.select({ addr: invite, nickname: "alice", display_name: "" });
    assert.equal($("login-accountpw").value, "");
    assert.equal($("login-serverpw").value, "");
    assert.equal($("login-nick").value, "alice");
    const request = await memory.request();
    assert.equal(request.addr, invite);
    assert.equal(request.password, "");
    assert.equal(request.server_password, "");
    assert.equal(request.use_saved_account, false);
    assert.equal(request.use_saved_server, false);
    assert.deepEqual(lookups.at(-1), [invite, "alice"]);
});
