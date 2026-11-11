import test from "node:test";
import assert from "node:assert/strict";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { currentFence } = await import("../src/core/db.js");
const { generateIdentity } = await import("../src/core/crypto.js");
const { DirectMessages } = await import("../src/core/direct.js");
const { disappearKey } = await import("../src/core/ratchet-consumer.js");
const { DISAPPEAR_OPTIONS, disappearingFromBody, durationLabel } = await import("../src/core/disappearing.js");

const session = () => Object.assign(new EventTarget(), { conn: { open: true, send() {} }, identity: generateIdentity(), uniqueID: "me", deviceID: "me-device", nickname: "Me" });
const record = (extra = {}) => ({ protocol: "noxa-olm-v1", scope: "direct", peer: "bob", sender: "bob", logicalID: "m1", key: "k", purpose: "chat",
    body: { text: "hi", sentAt: 1000 }, sentAt: 1000, status: "", transport: {}, ...extra });

// A fake consumer: records sends, projects records on demand, redacts on request.
function fakeConsumer() {
    const c = { closed: false, sends: [], redactions: [], projector: null, blocked: new Set() };
    c.project = fn => { c.projector = fn; };
    c.history = async () => [];
    c.send = async intent => { c.sends.push(intent); return { record: { body: intent.body }, receipt: { state: "accepted" } }; };
    c.redact = async targets => { c.redactions.push([...targets]); return { found: new Set([...targets].filter(k => !c.blocked.has(k))), blocked: new Set([...targets].filter(k => c.blocked.has(k))) }; };
    return c;
}

async function setup() {
    const dm = new DirectMessages(session(), {}, { fence: await currentFence() });
    await dm.ready;
    const consumer = fakeConsumer();
    await dm.attachRatchet(consumer);
    return { dm, consumer, project: (r, isNew = true) => consumer.projector(r, { isNew }) };
}

test("body fields and labels", () => {
    assert.deepEqual(disappearingFromBody({ ttl: 60 }), { ttl: 60, timer: null, expired: false });
    assert.deepEqual(disappearingFromBody({ ttl: 0.5 }), { ttl: 0, timer: null, expired: false });
    assert.deepEqual(disappearingFromBody({ disappearing: 0 }), { ttl: 0, timer: 0, expired: false });
    assert.deepEqual(disappearingFromBody({ expired: true }), { ttl: 0, timer: null, expired: true });
    assert.deepEqual([...DISAPPEAR_OPTIONS], [0, 30, 300, 3600, 86400, 604800]);
    assert.equal(durationLabel(90), "1m");
});

test("a setting change sets the timer; new messages carry it inside the encrypted body", async () => {
    const { dm, consumer, project } = await setup();
    await project(record({ logicalID: "timer:1", body: { text: "on", sentAt: 1000, disappearing: 3600 } }));
    assert.equal(dm.disappearing("bob"), 3600);
    const notice = dm.conversations.get("bob").messages.find(m => m.id === "timer:1");
    assert.equal(notice.timer, 3600);
    assert.equal(notice.ttl, undefined, "a setting change never expires itself");
    // An older change arriving later does not win.
    await project(record({ logicalID: "timer:0", sentAt: 900, body: { text: "off", sentAt: 900, disappearing: 0 } }));
    assert.equal(dm.disappearing("bob"), 3600);
    await dm.send("bob", "secret", "Bob", { id: "s1" });
    assert.deepEqual({ text: consumer.sends.at(-1).body.text, ttl: consumer.sends.at(-1).body.ttl }, { text: "secret", ttl: 3600 });
    // An explicit ttl (outbox retry of an older entry) keeps that content.
    await dm.send("bob", "queued before", "Bob", { id: "s2", ttl: 0 });
    assert.equal(consumer.sends.at(-1).body.ttl, undefined);
    await dm.setDisappearing("bob", 300, "fallback text");
    assert.deepEqual(consumer.sends.at(-1).body.disappearing, 300);
    assert.equal(consumer.sends.at(-1).body.text, "fallback text");
    await assert.rejects(dm.setDisappearing("bob", 7, ""), { code: "invalid" });
    dm.close();
});

test("received messages start on display, expire, and are redacted in the archive", async () => {
    const { dm, consumer, project } = await setup();
    await project(record({ body: { text: "secret", sentAt: 1000, ttl: 60 } }));
    await project(record({ logicalID: "own", sender: "me", status: "accepted", body: { text: "mine", sentAt: 1000, ttl: 30 } }));
    const messages = () => dm.conversations.get("bob").messages;
    const theirs = messages().find(m => m.id === "m1"), mine = messages().find(m => m.id === "own");
    assert.equal(theirs.ttl, 60);
    assert.equal(theirs.expiresAt, 0, "not shown yet");
    assert.equal(mine.expiresAt, 1030, "own copy counts from sending");
    // Nothing received expires before it was shown; the own copy does.
    let expired = await dm.sweepDisappearing(2_000_000);
    assert.deepEqual(expired, [{ peer: "bob", ids: [{ from: "me", id: "own" }] }]);
    assert.equal(theirs.body, "secret");
    const before = Date.now();
    assert.equal(await dm.startDisappearing("bob"), 1);
    assert.ok(theirs.expiresAt >= Math.floor(before / 1000) + 60);
    assert.equal(await dm.startDisappearing("bob"), 0, "a running timer is not restarted");
    expired = await dm.sweepDisappearing((theirs.expiresAt - 1) * 1000);
    assert.deepEqual(expired, []);
    expired = await dm.sweepDisappearing(theirs.expiresAt * 1000);
    assert.deepEqual(expired, [{ peer: "bob", ids: [{ from: "bob", id: "m1" }] }]);
    assert.equal(theirs.body, "");
    assert.equal(theirs.deleted, true);
    assert.equal(theirs.expired, true);
    assert.equal(theirs.redacted, true);
    assert.ok(consumer.redactions.flat().includes(disappearKey("bob", "m1")));
    // A history replay of the original record cannot bring the text back, and
    // a redacted archive record is never added as a new message.
    await project(record({ body: { text: "secret", sentAt: 1000, ttl: 60 } }), false);
    assert.equal(messages().find(m => m.id === "m1").body, "");
    await project(record({ logicalID: "gone", body: { expired: true } }), false);
    assert.equal(messages().some(m => m.id === "gone"), false);
    dm.close();
});

test("an own send without a terminal receipt is redacted later", async () => {
    const { dm, consumer, project } = await setup();
    await project(record({ peer: "carol", logicalID: "own", sender: "me", status: "accepted", body: { text: "mine", sentAt: 1000, ttl: 30 } }));
    consumer.blocked.add(disappearKey("me", "own"));
    await dm.sweepDisappearing(2_000_000);
    const own = dm.conversations.get("carol").messages.find(m => m.id === "own");
    assert.equal(own.body, "");
    assert.equal(own.redacted, false);
    consumer.blocked.clear();
    await dm.sweepDisappearing(2_000_000);
    assert.equal(own.redacted, true);
    dm.close();
});

test("a contact's change dated far ahead cannot block a later change", async () => {
    const { dm, project } = await setup();
    const now = Date.now() / 1000;
    await project(record({ peer: "dave", logicalID: "timer:f", sentAt: now + 365 * 86400, body: { text: "on", sentAt: now + 365 * 86400, disappearing: 604800 } }));
    assert.equal(dm.disappearing("dave"), 604800);
    await project(record({ peer: "dave", logicalID: "timer:me", sender: "me", status: "accepted", sentAt: now + 600, body: { text: "off", sentAt: now + 600, disappearing: 0 } }));
    assert.equal(dm.disappearing("dave"), 0);
    dm.close();
});

test("a queued own send is not marked redacted before its archive exists", async () => {
    const { dm, consumer } = await setup();
    const conversation = dm.conversation("erin");
    conversation.messages.push({ id: "q1", protocol: "noxa-olm-v1", from: "me", self: true, body: "queued", sentAt: 1000, ttl: 30, expiresAt: 1030, queued: true });
    for (let i = 0; i < 7; i++) await dm.sweepDisappearing(2_000_000);
    const queued = conversation.messages[0];
    assert.equal(queued.expired, true);
    assert.equal(queued.body, "");
    assert.equal(queued.redacted, false, "still waits for its archive");
    assert.equal(consumer.redactions.flat().includes(disappearKey("me", "q1")), false);
    dm.close();
});

test("a late edit of an expired message is redacted again (audit #11)", async () => {
    const { dm, consumer, project } = await setup();
    await project(record({ body: { text: "secret", sentAt: 1000, ttl: 60 } }));
    const find = () => dm.conversations.get("bob").messages.find(m => m.id === "m1");
    await dm.startDisappearing("bob");
    await dm.sweepDisappearing(find().expiresAt * 1000);
    assert.equal(find().redacted, true);
    const scans = consumer.redactions.length;
    // A delayed edit arrives from the spool after the message expired.
    await project(record({ logicalID: "e1", purpose: "edit", reference: "m1", body: { text: "edited secret" } }));
    assert.equal(find().body, "", "the expired message stays empty");
    assert.equal(find().redacted, false, "the edit's archive record still needs redacting");
    await dm.sweepDisappearing(find().expiresAt * 1000 + 1000);
    assert.ok(consumer.redactions.slice(scans).flat().includes(disappearKey("bob", "m1")));
    assert.equal(find().redacted, true);
    dm.close();
});
