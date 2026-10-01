import assert from "node:assert/strict";
import { beforeEach, afterEach, test } from "node:test";
import { MediaCryptor } from "../src/media-cryptor.js";

const saved = new Map();
const sessions = [];
class Worker {
    messages = [];
    postMessage(message) {
        this.messages.push(message);
        if (message.kind === "init" || message.kind === "enable") queueMicrotask(() => this.onmessage?.({ data: {
            kind: message.kind === "init" ? "initAck" : "enabled", data: { uuid: message.data.uuid },
        } }));
    }
    terminate() {}
}
beforeEach(() => {
    for (const [name, value] of Object.entries({ Worker, RTCRtpSender: class { createEncodedStreams() {} }, RTCRtpReceiver: class { createEncodedStreams() {} } })) {
        saved.set(name, Object.getOwnPropertyDescriptor(globalThis, name));
        Object.defineProperty(globalThis, name, { configurable: true, writable: true, value });
    }
});
afterEach(() => {
    for (const session of sessions.splice(0)) session.close();
    for (const [name, descriptor] of saved) {
        if (descriptor) Object.defineProperty(globalThis, name, descriptor); else delete globalThis[name];
    }
    saved.clear();
});
function session() { const cryptor = new MediaCryptor(); sessions.push(cryptor); return cryptor; }
function endpoint() { return { createEncodedStreams: () => ({ readable: {}, writable: {} }) }; }

test("media key installation snapshots caller bytes before any asynchronous work", async () => {
    const cryptor = session(); await cryptor.ready;
    const bytes = new Uint8Array(32).fill(1);
    const installing = cryptor.setKey("alice/audio/epoch-1", bytes);
    bytes.fill(2);
    await installing;
    await assert.doesNotReject(cryptor.setKey("alice/audio/epoch-1", new Uint8Array(32).fill(1)));
    await assert.rejects(cryptor.setKey("alice/audio/epoch-1", bytes), /new epoch/);
});

test("an attached transform cannot change its replay or encryption identity", async () => {
    const cryptor = session(); await cryptor.ready;
    const receiver = endpoint();
    cryptor.attachReceiver(receiver, "alice/audio/epoch-1", "voice", "opus");
    const count = cryptor.worker.messages.length;
    assert.throws(() => cryptor.attachReceiver(receiver, "alice/audio/epoch-2", "voice", "opus"), /new.*(endpoint|transform)/i);
    assert.equal(cryptor.worker.messages.length, count, "rebind must not be posted to the worker");
    assert.doesNotThrow(() => cryptor.attachReceiver(receiver, "alice/audio/epoch-1", "voice", "opus"));
});

test("keyless media transforms and preview requests share a bounded identity registry", async () => {
    const cryptor = session(); await cryptor.ready;
    for (let n = 0; n < 512; n++) cryptor.attachReceiver(endpoint(), `peer-${n}/audio/epoch-1`, "voice", "opus");
    const count = cryptor.worker.messages.length;
    assert.throws(() => cryptor.attachReceiver(endpoint(), "extra/audio/epoch-1", "voice", "opus"), /capacity/);
    await assert.rejects(cryptor.decryptData("extra/preview/epoch-1", { payload: new Uint8Array(16), iv: new Uint8Array(12), keyIndex: 0 }), /capacity/);
    assert.equal(cryptor.worker.messages.length, count);
});

test("transforms are bounded even when they reuse one identity", async () => {
    const cryptor = session(); await cryptor.ready;
    for (let n = 0; n < 512; n++) cryptor.attachReceiver(endpoint(), "alice/audio/epoch-1", "voice", "opus");
    assert.throws(() => cryptor.attachReceiver(endpoint(), "alice/audio/epoch-1", "voice", "opus"), /capacity/);
});

test("worker requests and malformed identities cannot grow the worker without bound", async () => {
    const cryptor = session(); await cryptor.ready;
    for (const identity of [undefined, {}, "", "x".repeat(257), "peer\nforged"]) {
        await assert.rejects(cryptor.setKey(identity, new Uint8Array(32)), /Invalid/);
        assert.throws(() => cryptor.attachSender(endpoint(), identity, "voice", "opus"), /Invalid/);
    }
    const waiting = Array.from({ length: 64 }, () => cryptor.encryptData("alice/preview/epoch-1", new Uint8Array(1)));
    const settled = Promise.allSettled(waiting);
    await assert.rejects(cryptor.encryptData("alice/preview/epoch-1", new Uint8Array(1)), /capacity/);
    const posted = cryptor.worker.messages.length;
    await assert.rejects(cryptor.setKey("alice/audio/epoch-1", new Uint8Array(32)), /capacity/);
    assert.equal(cryptor.worker.messages.length, posted, "key derivation cannot bypass the request reservation");
    cryptor.close(); await settled;
});
