import test from "node:test";
import assert from "node:assert/strict";
import nacl from "tweetnacl";

import { Calls } from "../src/core/calls.js";
import { openDM, sealDM } from "../src/core/crypto.js";

// A desktop peer sharing its screen natively opens a second relay-only
// connection (sub "screen"): the phone answers it receive-only with sub
// "screen-reply" and shows its video as that peer's screen share. Main
// offers and answers advertise the feature that makes the desktop do so.

const CALL = "8f0a2c1e-4b8d-4d6e-9a51-3c2b1a0f9e77";
const me = nacl.box.keyPair();
const desk = nacl.box.keyPair();
const tick = () => new Promise(resolve => setTimeout(resolve, 0));

class FakePeer {
    static all = [];
    constructor(config) {
        this.config = config;
        this.transceivers = [];
        this.remoteDescription = null;
        this.closed = false;
        FakePeer.all.push(this);
    }
    getTransceivers() { return this.transceivers; }
    addTrack(track) { return this.addTransceiver(track.kind).sender; }
    addTransceiver(kind) { const t = { receiver: { track: { kind } }, sender: { track: null, replaceTrack: async track => { t.sender.track = track; } }, direction: "sendrecv" }; this.transceivers.push(t); return t; }
    createDataChannel() { return { send() {} }; }
    async setRemoteDescription(d) {
        this.remoteDescription = d;
        if (d.type === "offer" && !this.transceivers.length) { this.addTransceiver("video"); this.addTransceiver("audio"); }
    }
    async createAnswer() { return { type: "answer", sdp: `answer:${this.transceivers.map(t => t.direction).join(",")}` }; }
    async createOffer() { return { type: "offer", sdp: "offer" }; }
    async setLocalDescription(d) { this.localDescription = d; }
    async addIceCandidate() {}
    async getStats() { return new Map(); }
    close() { this.closed = true; }
}

function setup(t) {
    const saved = {};
    for (const name of ["RTCPeerConnection", "navigator", "document", "MediaStream"]) saved[name] = Object.getOwnPropertyDescriptor(globalThis, name);
    const audioTrack = { kind: "audio", enabled: true, stop() {}, addEventListener() {} };
    const define = (name, value) => Object.defineProperty(globalThis, name, { configurable: true, writable: true, value });
    define("RTCPeerConnection", FakePeer);
    define("navigator", { mediaDevices: { getUserMedia: async () => ({ getTracks: () => [audioTrack], getAudioTracks: () => [audioTrack], getVideoTracks: () => [] }) } });
    define("document", { createElement: () => ({ play: async () => {}, remove() {}, isConnected: true }), body: { append() {} } });
    define("MediaStream", class { constructor() { this.tracks = []; } addTrack(track) { this.tracks.push(track); } });
    t.after(() => { for (const [name, d] of Object.entries(saved)) { if (d) Object.defineProperty(globalThis, name, d); else delete globalThis[name]; } });
    FakePeer.all = [];
    const call = { id: CALL, caller: "desk", revision: 1, created_at: 1700000000, participants: [
        { unique_id: "desk", client_id: "c-desk", device_id: "d-desk", public_key: "k", state: "accepted" },
        { unique_id: "self", client_id: "c-self", device_id: "d-self", public_key: "k", state: "accepted" }] };
    const signals = [];
    const session = Object.assign(new EventTarget(), {
        uniqueID: "self", identity: { xSecret: me.secretKey },
        relayServers: async () => [{ urls: ["turn:relay.example:3478"], username: "u", credential: "c" }],
        onRelayRenewal: () => () => {},
        conn: { request: async (_type, request) => {
            if (request.action === "signal") signals.push(JSON.parse(openDM(request.signal, me.publicKey, desk.secretKey)));
            return { call: structuredClone(call) };
        } },
    });
    const mediaFactory = () => {
        const stub = { active: false, setRoster() {}, async start() { stub.active = true; }, receive: () => ({ ok: true }), peerConfig: c => c, protectPeer: pc => pc,
            syncPeer() {}, endpointID: () => "", releasePeer: async () => {}, stop() { stub.active = false; } };
        return stub;
    };
    const calls = new Calls(session, { callKey: async () => desk.publicKey }, { mediaFactory });
    calls.addEventListener("error", () => {});
    const event = (type, data) => { session.dispatchEvent(new CustomEvent("server-event", { detail: { type, data } })); };
    const signal = async description => {
        event("private_call_signal", { call_id: CALL, from: "desk", to: "self", body: sealDM(JSON.stringify({ call_id: CALL, from: "desk", to: "self", ...description }), me.publicKey, desk.secretKey) });
        for (let i = 0; i < 20; i++) await tick();
    };
    const wait = async predicate => { for (let i = 0; i < 200 && !predicate(); i++) await new Promise(resolve => setTimeout(resolve, 5)); };
    return { calls, event, signal, signals, wait };
}

test("main answers advertise the screen sub-connection; a native screen offer is answered receive-only", async t => {
    const { calls, event, signal, signals, wait } = setup(t);
    event("private_call", { id: CALL });
    await wait(() => calls.peers.has("desk"));
    FakePeer.all[0].ondatachannel({ channel: { send() {} } });
    await signal({ type: "offer", sdp: "main-offer" });
    await wait(() => signals.length >= 1);
    assert.equal(signals[0].type, "answer");
    assert.deepEqual(signals[0].features, ["screen-sub"]);
    assert.equal(signals[0].sub, undefined);

    await signal({ type: "offer", sub: "screen", sdp: "screen-offer" });
    await wait(() => signals.length >= 2);
    assert.equal(signals[1].sub, "screen-reply");
    assert.equal(signals[1].type, "answer");
    assert.equal(signals[1].features, undefined);
    assert.equal(signals[1].sdp, "answer:recvonly,recvonly", "nothing is sent back on the sub-connection");
    const sub = FakePeer.all.at(-1);
    assert.equal(sub.config.iceTransportPolicy, "relay");

    const peer = calls.peers.get("desk");
    const mainScreen = peer.video?.screen;
    const native = { kind: "video" };
    sub.ontrack({ track: native });
    assert.equal(peer.video.screen, native, "the sub-connection's video is the peer's screen");
    // The peer's main-connection state says the share ended: the sub-connection closes.
    peer.channel.onmessage({ data: JSON.stringify({ camera: false, screen: false, e2ee: (await import("../src/core/media-e2ee/call-media-e2ee.js")).CALL_E2EE_MARKER }) });
    assert.equal(sub.closed, true);
    assert.equal(peer.video.screen, mainScreen, "the main connection's screen slot is restored");
});

test("a malformed sub-connection signal closes only that sub-connection", async t => {
    const { calls, event, signal, wait } = setup(t);
    event("private_call", { id: CALL });
    await wait(() => calls.peers.has("desk"));
    await signal({ type: "offer", sub: "screen", sdp: "screen-offer" });
    await wait(() => FakePeer.all.length >= 2);
    const sub = FakePeer.all.at(-1);
    await signal({ type: "answer", sub: "screen", sdp: "bad" });
    await wait(() => sub.closed);
    assert.equal(sub.closed, true);
    assert.equal(calls.active, true, "the call continues");
    assert.equal(FakePeer.all[0].closed, false);
});
