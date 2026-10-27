// Fix4 (review N1/N2/N5/N6, F2/F5/F6/F7): receive classification, drain
// robustness against malformed deliveries, readiness refresh backoff and
// configuration bounds of the PWA RatchetConsumer.
import test from "node:test";
import assert from "node:assert/strict";
import { RatchetConsumer, deterministic, receiveDisposition } from "../src/core/ratchet-consumer.js";

const coded = code => Object.assign(new Error(String(code)), { code });
const relabel = async error => deterministic("invalid_evidence", () => { throw error; }).catch(e => e.code);

test("N2/F2: only allow-listed string codes pass through deterministic()", async () => {
    assert.equal(await relabel(new Error("core rejected")), "invalid_evidence", "uncoded core/verification rejection");
    let dom;
    try { atob("!!!"); } catch (e) { dom = e; }
    assert.equal(typeof dom.code, "number", "atob throws a DOMException with a numeric code");
    assert.equal(await relabel(dom), "invalid_evidence", "DOMException numeric code is relabelled");
    assert.equal(await relabel(coded(42)), "invalid_evidence");
    assert.equal(await relabel(coded("something_else")), "invalid_evidence", "unknown string code is relabelled");
    assert.equal(await relabel(new TypeError("x is undefined")), "invalid_evidence");
    for (const code of ["core_unavailable", "fenced", "rate-limit", "unavailable", "conflict", "aborted", "local_conflict"])
        assert.equal(await relabel(coded(code)), code, `transient ${code} passes through`);
    for (const code of ["lost_key", "unauthenticated", "digest_mismatch", "invalid"])
        assert.equal(await relabel(coded(code)), code, `deterministic ${code} passes through`);
});

test("N1/F5: only same-delivery races skip quarantine", () => {
    assert.equal(receiveDisposition("core_unavailable", false), "retry");
    assert.equal(receiveDisposition("rate-limit", true), "retry");
    assert.equal(receiveDisposition("lost_key", true), "skip", "another tab consumed the key for this delivery");
    assert.equal(receiveDisposition("unauthenticated", true), "skip", "another tab advanced the session for this delivery");
    assert.equal(receiveDisposition("lost_key", false), "quarantine");
    assert.equal(receiveDisposition("digest_mismatch", true), "quarantine", "a genuine mismatch is never hidden");
    assert.equal(receiveDisposition("invalid", true), "quarantine");
});

function fakeStore({ authority } = {}) {
    const calls = { authority: 0 };
    return {
        calls,
        async read() { return { records: {} }; },
        async accountAuthority() { calls.authority++; return authority ? authority() : { accountID: "local", epoch: 1, blocked: false, nextAttempt: 0, fence: 1, retired: { through: 0, above: [] } }; },
        async checkAccountAuthority() {},
        async pendingRefs() { return []; },
    };
}
const consumer = (store, admission = async () => ({ deliveries: [], has_more: false }), extra = {}) => new RatchetConsumer({
    store, core: {}, namespace: { server: "tls-sha256:" + "a".repeat(64), principal: "alice", device: "a" }, admission, directory: async () => ({}),
    deviceKey: async () => new Uint8Array(32), signBundle: async b => b, assertCurrent: () => {}, ...extra,
});
const ready = c => { c.readyValue = { identity: "id", epoch: 1, expiresAt: Math.floor(Date.now() / 1000) + 86400 }; c.readiness = Promise.resolve(c.readyValue); c.readyAt = Date.now(); };

test("N1/F6: malformed deliveries become coded invalid placeholders and never abort the drain", async () => {
    let pages = 0;
    const page = { deliveries: [{ manifest: {} }, { id: "bad\u0000id", manifest: null }, null, { id: "x".repeat(600), copy: {} }], after: "", has_more: false };
    const c = consumer(fakeStore(), async r => { if (r.action === "deliveries") { pages++; return page; } return {}; });
    ready(c);
    const first = await c.drain();
    assert.equal(first.quarantined.length, 4);
    assert.ok(first.quarantined.every(q => q.code === "invalid" && typeof q.id === "string" && q.id.startsWith("malformed:")));
    const second = await c.drain();
    assert.equal(second.quarantined.length, 4, "quarantined once, not re-processed or duplicated");
    assert.equal(pages, 2);
});

test("CodeRabbit: a page without a deliveries array is rejected, not treated as empty", async () => {
    for (const bad of [{ after: "", has_more: false }, { deliveries: null, after: "", has_more: false }, { deliveries: {}, has_more: false }, null]) {
        const c = consumer(fakeStore(), async r => (r.action === "deliveries" ? bad : {}));
        ready(c);
        await assert.rejects(c.drain(), e => e.code === "invalid");
    }
    const ok = consumer(fakeStore(), async r => (r.action === "deliveries" ? { deliveries: [], after: "", has_more: false } : {}));
    ready(ok);
    assert.deepEqual((await ok.drain()).quarantined, [], "a valid empty page still completes");
});

test("F7: a failed refresh keeps the valid readiness and backs off; recovery_required is sticky", async () => {
    let failing = false;
    const store = fakeStore({ authority: () => { if (failing) throw coded("unavailable"); return { accountID: "local", epoch: 1, blocked: false, nextAttempt: 0, fence: 1, retired: { through: 0, above: [] } }; } });
    const c = consumer(store, undefined, { refreshMS: 1000 });
    ready(c);
    c.readyAt = Date.now() - 2000; // refresh due
    failing = true;
    const before = store.calls.authority;
    assert.equal((await c.ready()).identity, "id", "previous readiness is served");
    await c.refreshing?.catch(() => {});
    await new Promise(r => setTimeout(r, 0));
    assert.ok(store.calls.authority > before, "a refresh was attempted");
    const afterFailure = store.calls.authority;
    assert.equal((await c.ready()).identity, "id");
    assert.equal(store.calls.authority, afterFailure, "no immediate re-refresh after a failure (backoff)");
    assert.ok(Date.now() - c.readyAt < 1000, "next refresh scheduled for later");

    const blocked = fakeStore({ authority: () => ({ accountID: "local", epoch: 1, blocked: true, nextAttempt: 0, fence: 1 }) });
    const r = consumer(blocked);
    await assert.rejects(r.ready(), e => e.code === "recovery_required");
    const n = blocked.calls.authority;
    await assert.rejects(r.ready(), e => e.code === "recovery_required");
    await assert.rejects(r.send({ logicalID: "x", scope: "direct", peer: "bob", body: {} }), e => e.code === "recovery_required");
    assert.equal(blocked.calls.authority, n, "sticky: no repeated work");
});

test("N6: attemptTTL must exceed the abort margin and let a fresh claim cover a resend", () => {
    assert.throws(() => consumer(fakeStore(), undefined, { attemptTTL: 30 }), e => e.code === "invalid_config");
    assert.throws(() => consumer(fakeStore(), undefined, { attemptTTL: 3600 }), e => e.code === "invalid_config");
    assert.throws(() => consumer(fakeStore(), undefined, { attemptTTL: 120.5 }), e => e.code === "invalid_config");
    assert.equal(consumer(fakeStore(), undefined, { attemptTTL: 300 }).attemptTTL, 300);
});
