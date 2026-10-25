import { test } from "node:test";
import assert from "node:assert/strict";
import { loginFailure } from "../src/login-errors.js";
import * as loginErrorsModule from "../src/login-errors.js";

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
    for (const reason of ["dial tcp 127.0.0.1:12333: connectex: No connection could be made because the target machine actively refused it.", "read tcp: i/o timeout"]) {
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

test("minimum client version preserves installed and required versions without blaming credentials", () => {
    assert.deepEqual(loginFailure("client version rejected: installed 0.5.59; required >=0.5.94; update noXa to connect"), { kind: "minimumVersion", installed: "0.5.59", required: "0.5.94" });
    assert.deepEqual(loginFailure("Error: client version rejected: installed unknown; required >=0.5.94-RC.1+Build; update noXa to connect"), { kind: "minimumVersion", installed: "unknown", required: "0.5.94-RC.1+Build" });
    assert.equal(loginFailure("client version rejected: installed old; required >=<script>; update noXa to connect").kind, "unknown");
});

test("TLS dial wrapper preserves network versus certificate recovery", () => {
    assert.deepEqual(loginFailure("TLS connection failed: dial tcp 127.0.0.1:12333: connectex: actively refused"), { kind: "network", field: "login-addr" });
    assert.deepEqual(loginFailure("TLS connection failed: context deadline exceeded"), { kind: "network", field: "login-addr" });
    assert.equal(loginFailure("TLS connection failed: tls: failed to verify certificate: x509: certificate expired").kind, "certificate");
    assert.equal(loginFailure("TLS connection failed: tls fingerprint mismatch").kind, "certificate");
});

test("(313) DNS, TLS, authentication and authorization failures are distinct categories with their own next step", () => {
    const { connectFailureCategory, connectFailureIsDefinitive } = loginErrorsModule;
    assert.deepEqual(loginFailure("dial tcp: lookup voice.example: no such host"), { kind: "dns", field: "login-addr", invalid: true });
    assert.equal(connectFailureCategory(loginFailure("lookup voice.example on 10.0.0.1:53: server misbehaving")), "dns");
    assert.equal(connectFailureCategory(loginFailure("dial tcp 10.0.0.5:12333: i/o timeout")), "network");
    assert.equal(connectFailureCategory(loginFailure("tls: failed to verify certificate: x509: certificate signed by unknown authority")), "tls");
    assert.equal(connectFailureCategory(loginFailure("remote error: tls: handshake failure")), "tls");
    assert.equal(connectFailureCategory(loginFailure("invalid credentials")), "authentication");
    assert.equal(connectFailureCategory(loginFailure("banned")), "authentication");
    assert.deepEqual(loginFailure("permission denied"), { kind: "permission" });
    assert.equal(connectFailureCategory(loginFailure("forbidden: registration required")), "permission");
    assert.equal(connectFailureCategory(loginFailure("unsupported server authorization model; upgrade noXa")), "version");
    assert.equal(connectFailureCategory(loginFailure("server address is required")), "input");
    assert.equal(connectFailureCategory(loginFailure("something else entirely")), "other");
    assert.equal(connectFailureCategory(null), "other");
});

test("(314) only server decisions and trust failures are definitive; the path to the server is retryable", () => {
    const { connectFailureIsDefinitive } = loginErrorsModule;
    for (const reason of ["invalid credentials", "invalid server password", "banned", "authentication failed", "permission denied", "unsupported server authorization model; upgrade noXa", "x509: certificate has expired"]) {
        assert.equal(connectFailureIsDefinitive(loginFailure(reason)), true, reason);
    }
    for (const reason of ["dial tcp 10.0.0.5:12333: connection refused", "dial tcp: lookup voice.example: no such host", "too many failed logins, try again later", "context deadline exceeded", "unexpected upstream rejection"]) {
        assert.equal(connectFailureIsDefinitive(loginFailure(reason)), false, reason);
    }
});
