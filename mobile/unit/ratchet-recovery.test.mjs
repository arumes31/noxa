// 1c-iii-b item 2 (PWA): explicit recovery through the real RatchetConsumer.
// The fake server enforces store.ResetOlmAccount's rules; the fake store
// records the durable calls. Hostile own_state is refused before anything is
// signed or stored; a refused reset is abandoned; peers verify the chain.
import test from "node:test";
import assert from "node:assert/strict";
import { RatchetConsumer } from "../src/core/ratchet-consumer.js";
import { generateIdentity, toBase64 } from "../src/core/crypto.js";
import { signBundle, OLM_DIRECTORY_VERSION } from "../src/core/olm-directory.js";
import { signReset, verifyReset, verifyResetChain } from "../src/core/olm-reset.js";

const server = "tls-sha256:" + "c".repeat(64);
const raw = b => toBase64(b).replace(/=+$/, "");
const keys = () => { const i = generateIdentity(); return { identity: raw(i.xPublic), signing: raw(i.edPublic), otk: raw(generateIdentity().xPublic) }; };
const nowS = () => Math.floor(Date.now() / 1000);

function bundleAt(noxa, epoch, generation) {
    const k = keys();
    return signBundle({ version: OLM_DIRECTORY_VERSION, protocol: "noxa-olm-v1", server, principal: "alice", device_id: "a1", account_epoch: epoch, generation,
        issued_at: nowS() - 30 * 86400, expires_at: nowS() - 25 * 86400, identity: k.identity, signing: k.signing, one_time_keys: [{ id: "AAAAAAAAAAE", key: k.otk }] }, noxa.edSecret);
}

function harness({ ownState } = {}) {
    const noxa = generateIdentity();
    const srv = { state: bundleAt(noxa, 3, 5), resets: [], calls: 0 };
    const log = [];
    const authority = { accountID: "local", epoch: 1, blocked: false, nextAttempt: 0, fence: 1, retired: { through: 0, above: [] } };
    const transitions = new Map();
    const store = {
        async read() { return { records: {} }; },
        async accountAuthority() { return { ...authority }; },
        async checkAccountAuthority() {},
        async pendingRefs(kind) { return kind === "transition" ? [...transitions.keys()].map(id => ["transition", id]) : []; },
        async accountTransitionRequest(s, id) { return transitions.get(id); },
        coreKey() { return new Uint8Array(32); },
        async prepareRecoveryTransition(s, accountID, account, reset, bundle) {
            log.push("prepare"); authority.blocked = true;
            transitions.set(reset.transition_id, { account_id: accountID, reset, bundle, recovery: true, from_epoch: authority.epoch });
        },
        async abandonRecoveryTransition(s, id) { log.push("abandon"); transitions.get(id).abandoned = true; authority.blocked = false; },
        async promoteAccount(s, id, receipt) {
            const t = transitions.get(id);
            assert.equal(receipt.transition_id, id);
            log.push("promote"); t.receipt = receipt; Object.assign(authority, { accountID: t.account_id, epoch: t.reset.new_epoch, blocked: false });
        },
    };
    let n = 0;
    const core = { async apply(r) { assert.equal(r.op, "new_account"); const k = keys(); return { account: "acct-" + ++n, bundle: { identity: k.identity, signing: k.signing, one_time_keys: [{ id: "AAAAAAAAAAE", key: k.otk }] } }; } };
    const directory = async r => {
        assert.equal(r.action, "own_state");
        if (srv.ownFail > 0) { srv.ownFail--; throw new Error("connection lost"); }
        return { request_id: r.request_id, action: r.action, bundle: ownState ? ownState(noxa, srv) : srv.state };
    };
    const apply = async r => {
        assert.equal(r.action, "reset");
        srv.calls++;
        const cur = srv.state;
        try {
            if (r.account_epoch !== cur.account_epoch || r.reset.generation !== cur.generation + 1) throw new Error();
            verifyReset(r.reset, { server, principal: "alice", device: "a1", epoch: cur.account_epoch, identity: cur.identity, signing: cur.signing, noxaSigning: noxa.edPublic });
        } catch { throw Object.assign(new Error("authority-required"), { code: "authority-required" }); }
        srv.state = r.bundle;
        srv.resets.push(r.reset);
        return { server, device: "a1", transition_id: r.reset.transition_id, account_epoch: r.reset.new_epoch, digest: "d", publication_digest: r.reset.publication_digest };
    };
    // srv.override(r, apply) replaces the server's answer (lost responses, refusals).
    const admission = r => srv.override ? srv.override(r, apply) : apply(r);
    const c = new RatchetConsumer({ store, core, namespace: { server, principal: "alice", device: "a1" }, admission, directory,
        deviceKey: async () => noxa.edPublic, signBundle: async b => signBundle(b, noxa.edSecret), assertCurrent: () => {},
        signReset: async r => signReset(r, noxa.edSecret), ownKey: noxa.edPublic });
    c.sticky = Object.assign(new Error("recovery_required"), { code: "recovery_required" });
    return { c, srv, noxa, log, authority };
}

test("recovery gives a restored store a fresh signed epoch that a peer with the old pin verifies", async () => {
    const { c, srv, noxa, log, authority } = harness();
    const old = srv.state;
    assert.equal(await c.recover("restored"), true);
    assert.deepEqual(log, ["prepare", "promote"]);
    assert.equal(authority.epoch, 4);
    assert.equal(c.sticky, null, "recovery_required is lifted");
    const peer = await verifyResetChain({ epoch: 3, identity: old.identity, signing: old.signing }, { epoch: 4, identity: srv.state.identity, signing: srv.state.signing },
        { server, principal: "alice", device: "a1", noxaSigning: noxa.edPublic, fetch: async () => srv.resets });
    assert.equal(peer.pin.epoch, 4);
    assert.equal(srv.resets[0].generation, 6);
});

test("hostile own_state: forged or regressed is refused before anything is signed; stale is refused by the server and abandoned", async () => {
    const forged = harness({ ownState: () => bundleAt(generateIdentity(), 3, 5) });
    await assert.rejects(forged.c.recover("restored"), e => e.code === "invalid_evidence");
    assert.deepEqual(forged.log, []);
    assert.equal(forged.srv.calls, 0);

    const regressed = harness();
    regressed.authority.epoch = 5;
    await assert.rejects(regressed.c.recover("restored"), e => e.code === "invalid_evidence");
    assert.deepEqual(regressed.log, []);

    const stale = harness({ ownState: noxa => bundleAt(noxa, 2, 3) });
    await assert.rejects(stale.c.recover("restored"), e => e.code === "authority-required");
    assert.deepEqual(stale.log, ["prepare", "abandon"]);
    assert.equal(stale.authority.blocked, false, "sending authority restored");
    assert.equal(stale.srv.resets.length, 0);
});

const code = c => e => e.code === c;
const refuse = c => async () => { throw Object.assign(new Error(c), { code: c }); };

test("review P2-1: applied reset, response lost, resume refused -> promoted, never abandoned", async () => {
    const { c, srv, log, authority } = harness();
    let n = 0;
    srv.override = async (r, apply) => {
        if (++n === 1) { await apply(r); srv.ownFail = 1; throw new Error("connection lost"); }
        return refuse("authority-required")();
    };
    await assert.rejects(c.recover("restored"), code("recovery_pending"));
    assert.equal(authority.blocked, true);
    assert.equal(await c.recover("restored"), true);
    assert.deepEqual(log, ["prepare", "promote"]);
    assert.equal(authority.epoch, 4);
    assert.equal(srv.resets.length, 1);
    assert.equal(c.sticky, null);
});

test("evidence decides: applied with lost response completes; not applied with a transient refusal stays pending", async () => {
    const applied = harness();
    applied.srv.override = async (r, apply) => { await apply(r); throw new Error("connection lost"); };
    assert.equal(await applied.c.recover("restored"), true);
    assert.equal(applied.authority.epoch, 4);

    const pending = harness();
    pending.srv.override = refuse("unavailable");
    await assert.rejects(pending.c.recover("restored"), code("recovery_pending"));
    assert.deepEqual(pending.log, ["prepare"]);
    assert.equal(pending.authority.blocked, true);
});

test("review P3-2: explicit cancel abandons only on not-applied evidence, completes an applied intent", async () => {
    const none = harness();
    await assert.rejects(none.c.cancelRecovery(), code("not_needed"));

    const pending = () => { const h = harness(); h.srv.override = refuse("unavailable"); return h; };
    const unknown = pending();
    await assert.rejects(unknown.c.recover("restored"), code("recovery_pending"));
    unknown.srv.ownFail = 1;
    await assert.rejects(unknown.c.cancelRecovery(), code("recovery_unknown"));
    assert.equal(unknown.authority.blocked, true);

    const notApplied = pending();
    await assert.rejects(notApplied.c.recover("restored"), code("recovery_pending"));
    assert.equal(await notApplied.c.cancelRecovery(), "cancelled");
    assert.deepEqual(notApplied.log, ["prepare", "abandon"]);
    assert.equal(notApplied.authority.blocked, false);

    const applied = pending();
    await assert.rejects(applied.c.recover("restored"), code("recovery_pending"));
    // The server applied it after all (e.g. a delayed request).
    const [ref] = await applied.c.store.pendingRefs("transition");
    const intent = await applied.c.store.accountTransitionRequest(null, ref[1]);
    applied.srv.state = intent.bundle;
    assert.equal(await applied.c.cancelRecovery(), "completed");
    assert.equal(applied.authority.epoch, 4);
    assert.equal(applied.authority.blocked, false);
});
