import { test } from "node:test";
import assert from "node:assert/strict";
import { loginFailure } from "../src/login-errors.js";

test("account rejection never claims which credential was wrong", () => {
    assert.deepEqual(loginFailure("invalid credentials"), { kind: "account", field: "login-accountpw", invalid: true });
    assert.equal(loginFailure("authentication failed").kind, "authentication");
});

test("server password and blocked admission are distinct from account credentials", () => {
    assert.equal(loginFailure("invalid server password").field, "login-serverpw");
    assert.equal(loginFailure("banned").field, undefined);
    assert.equal(loginFailure("too many failed logins, try again later").kind, "rateLimit");
});

test("network errors offer address recovery without marking valid input invalid", () => {
    for (const reason of ["dial tcp: lookup voice.example: no such host", "dial tcp 127.0.0.1:12333: connectex: No connection could be made because the target machine actively refused it.", "read tcp: i/o timeout"]) {
        assert.deepEqual(loginFailure(reason), { kind: "network", field: "login-addr" });
    }
});

test("certificate failures never fall through to network retry advice", () => {
    assert.equal(loginFailure("tls: failed to verify certificate: x509: certificate has expired").kind, "certificate");
    assert.equal(loginFailure("tls fingerprint mismatch: timeout").kind, "certificate");
});

test("incompatible authorization versions offer the update path", () => {
    assert.deepEqual(loginFailure("unsupported server authorization model; upgrade noXa"), { kind: "version" });
});

test("unknown server responses remain unknown and cannot direct arbitrary field focus", () => {
    for (const reason of ["unexpected upstream rejection", "server says password invalid but unrelated", "<script>alert(1)</script>"]) {
        assert.deepEqual(loginFailure(reason), { kind: "unknown" });
    }
});

test("name errors focus the name actually submitted, and missing saved secrets retain scope", () => {
    assert.equal(loginFailure("display name must contain 1–64 characters", { displayName: "" }).field, "login-nick");
    assert.equal(loginFailure("display name must contain 1–64 characters", { displayName: "Daniel" }).field, "login-display-name");
    assert.equal(loginFailure("saved password is no longer available; enter it again", { use_saved_server: true }).field, "login-serverpw");
    assert.equal(loginFailure("saved password is no longer available; enter it again", { use_saved_account: true, use_saved_server: true }).field, "login-accountpw");
    assert.equal(loginFailure("saved passwords cannot be unlocked by this OS account; enter them again").kind, "savedPassword");
});
