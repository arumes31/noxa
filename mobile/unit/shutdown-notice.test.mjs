import assert from "node:assert/strict";
import { test } from "node:test";

const store = new Map();
globalThis.localStorage = { getItem: key => store.get(key) ?? null, setItem: (key, value) => store.set(key, value), removeItem: key => store.delete(key) };

const { setLanguage } = await import("../src/i18n.js");
const { shutdownNotice } = await import("../src/shutdown-notice.js");

test("scheduled shutdown notices name the remaining time", () => {
    setLanguage("en");
    assert.match(shutdownNotice("server_shutdown_scheduled", { seconds: 30 }), /shuts down in 30 s/);
    assert.match(shutdownNotice("server_shutdown_scheduled", { seconds: 300 }), /shuts down in 5 min/);
    assert.match(shutdownNotice("server_shutdown_scheduled", { seconds: 89 }), /in 89 s/);
    assert.match(shutdownNotice("server_shutdown_scheduled", { seconds: 90 }), /in 90 s/, "never rounds 90 s up to 2 min");
    assert.match(shutdownNotice("server_shutdown_scheduled", { seconds: 60 }), /in 1 min/);
    assert.match(shutdownNotice("server_shutdown_scheduled", {}), /in 1 s/, "malformed data still yields a notice");
});

test("an immediate shutdown makes no reconnect promise", () => {
    setLanguage("en");
    assert.equal(shutdownNotice("server_shutdown", {}), "The server is shutting down.");
});

test("German notices are translated", () => {
    setLanguage("de");
    assert.match(shutdownNotice("server_shutdown_scheduled", { seconds: 120 }), /in 2 min heruntergefahren/);
    assert.match(shutdownNotice("server_shutdown", {}), /wird heruntergefahren/);
    setLanguage("en");
});
