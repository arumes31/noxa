import { test } from "node:test";
import assert from "node:assert/strict";
import { bookmarkExportPayload, exportContainsCredentials, isCredentialKey, serializeBookmarkExport, stripBookmarkCredentials } from "../src/bookmark-export.js";

const bookmarks = [
    { name: "Home", addr: "voice.example:12333", nickname: "dan", folder: "Friends", color: "#ff0", password: "hunter2", server_password: "srv", token: "abc", pw: "p", spw: "s", nested: { api_key: "k", avatar_override_b64: "AAAA" } },
    { name: "Guest", addr: "guest.example", auto_connect: true },
    null,
];

test("(318) credential keys are recognised in every spelling that appears in connect code", () => {
    for (const key of ["password", "server_password", "Password", "accountPassword", "pw", "spw", "token", "access_token", "secret", "client_secret", "api_key", "apiKey", "private_key", "credentials", "passphrase"]) {
        assert.equal(isCredentialKey(key), true, key);
    }
    for (const key of ["name", "addr", "nickname", "folder", "color", "order", "auto_connect", "profile", "nickname_override", "avatar_override_b64"]) {
        assert.equal(isCredentialKey(key), false, key);
    }
});

test("(318) the default export strips credentials recursively and leaves the source untouched", () => {
    const payload = bookmarkExportPayload(bookmarks, { now: () => new Date("2026-10-25T10:00:00Z") });
    assert.equal(payload.include_credentials, false);
    assert.equal(payload.exported_at, "2026-10-25T10:00:00.000Z");
    assert.equal(payload.bookmarks.length, 2, "null entries are dropped");
    assert.deepEqual(payload.bookmarks[0], { name: "Home", addr: "voice.example:12333", nickname: "dan", folder: "Friends", color: "#ff0", nested: { avatar_override_b64: "AAAA" } });
    assert.deepEqual(payload.bookmarks[1], { name: "Guest", addr: "guest.example", auto_connect: true });
    assert.equal(bookmarks[0].password, "hunter2", "the stored bookmark keeps its fields");
    const text = serializeBookmarkExport(payload);
    assert.equal(exportContainsCredentials(text), false);
    assert.doesNotMatch(text, /hunter2|srv|abc/);
});

test("(318) the opt-in export keeps credential fields verbatim and marks the file", () => {
    const payload = bookmarkExportPayload(bookmarks, { includeCredentials: true });
    assert.equal(payload.include_credentials, true);
    assert.equal(payload.bookmarks[0].password, "hunter2");
    assert.equal(payload.bookmarks[0].nested.api_key, "k");
    assert.equal(exportContainsCredentials(serializeBookmarkExport(payload)), true);
    assert.notEqual(payload.bookmarks[0], bookmarks[0], "entries are copies");
});

test("(318) stripping tolerates arrays, primitives and empty input", () => {
    assert.deepEqual(stripBookmarkCredentials([{ token: 1, name: "a" }]), [{ name: "a" }]);
    assert.equal(stripBookmarkCredentials("x"), "x");
    assert.equal(stripBookmarkCredentials(null), null);
    assert.deepEqual(bookmarkExportPayload(undefined).bookmarks, []);
    assert.equal(exportContainsCredentials("not json"), false);
});
