// Video and screen-audio receivers keyed by the receiver track object and the
// publisher/slot of the CURRENT remote description, never by browser track.id.
// Chromium fixes a receiver's track.id from the a=msid present when its
// transceiver was created and keeps it when the server reuses that MID for
// another publisher, so after a leave/rejoin two live receivers share one id.
import { test, mock } from "node:test";
import assert from "node:assert/strict";
import { createReceiverRegistry, attributeReceiver, releaseReceiver } from "../src/remote-receivers.js";
import { startRemoteMedia, reconcileRemoteMedia } from "../src/remote-media.js";

// ---------------------------------------------------------------------------
// Minimal browser surface for stream-controls.js (catalog cards + watch).
// ---------------------------------------------------------------------------
function fakeElement(tag = "div") {
    const attributes = new Map(), children = new Map();
    const el = {
        tag, className: "", id: "", hidden: false, disabled: false, textContent: "", title: "", value: "", dataset: {},
        parentElement: null, isConnected: true, innerHTML: "",
        setAttribute: (name, value) => attributes.set(name, String(value)),
        getAttribute: name => attributes.get(name) ?? null,
        removeAttribute: name => attributes.delete(name),
        append(...nodes) { for (const node of nodes) node.parentElement = el; },
        before(...nodes) { for (const node of nodes) node.parentElement = el.parentElement; },
        remove() { el.parentElement = null; el.isConnected = false; },
        focus() {},
        querySelector(selector) {
            if (!children.has(selector)) {
                const child = fakeElement();
                child.parentElement = el;
                children.set(selector, child);
            }
            return children.get(selector);
        },
    };
    return el;
}

const grid = fakeElement();
globalThis.document = { createElement: fakeElement, getElementById: id => id === "video-grid" ? grid : null, activeElement: null, body: fakeElement() };
globalThis.window = Object.assign(new EventTarget(), {
    __noxa: { state: { pc: null, clients: [{ client_id: "pa", nickname: "A" }, { client_id: "pb", nickname: "B" }] }, renderTree() {} },
    go: { main: { App: {} } },
});
globalThis.MediaStream = class { constructor(tracks) { this.tracks = [...tracks]; } getVideoTracks() { return this.tracks.filter(track => track.kind === "video"); } };

const catalog = { streams: [], session: "s1" };
mock.module(new URL("../src/media-controls.js", import.meta.url).href, { namedExports: {
    captureMediaScope: () => ({ tabID: "tab", channelID: 1, clientID: "me" }), mediaScopeIsCurrent: () => true,
} });
mock.module(new URL("../src/stream-publication.js", import.meta.url).href, { namedExports: {
    streamRequest: async (_scope, body) => body.action === "list" ? structuredClone(catalog) : { ...body },
    stopPublications() {}, publicationSnapshot: () => [], reconcilePublications() {}, reconcilePublicationUploads() {},
    isCurrentPublication: () => false,
} });
mock.module(new URL("../src/share-status.js", import.meta.url).href, { namedExports: { updateShareViewers() {} } });
mock.module(new URL("../src/i18n.js", import.meta.url).href, { namedExports: { t: key => key } });
mock.module(new URL("../src/video-hevc.js", import.meta.url).href, { namedExports: {
    receiverHEVCSupport: async () => null, hevcReadmissionDelay: () => null,
} });
mock.module(new URL("../src/video-codecs.js", import.meta.url).href, { namedExports: { receiverVideoCodecs: () => ["vp8"] } });

const controls = await import("../src/stream-controls.js");

// ---------------------------------------------------------------------------
// Peer connection fixture: transceivers + SDP; the remote description is the
// only source of publisher identity.
// ---------------------------------------------------------------------------
class FakeTrack extends EventTarget {
    constructor(id, kind) { super(); Object.assign(this, { id, kind, readyState: "live", enabled: true, muted: false }); }
    end() { this.readyState = "ended"; this.dispatchEvent(new Event("ended")); }
}

const section = ({ mid, kind = "video", id = "", direction = "sendonly" }) =>
    `m=${kind} 9 UDP/TLS/RTP/SAVPF 96\r\na=mid:${mid}\r\na=${direction}\r\n${id ? `a=msid:noxa-${id} ${id}\r\n` : ""}`;

function peer() {
    const transceivers = [];
    const pc = { remoteDescription: { sdp: "" }, getTransceivers: () => transceivers };
    return {
        pc, transceivers,
        // Creating a transceiver fixes its track.id from the MSID at that time.
        add(mid, kind, createdAs) {
            const transceiver = { mid, receiver: { track: new FakeTrack(createdAs, kind) } };
            transceivers.push(transceiver);
            return transceiver.receiver.track;
        },
        negotiate(sections) {
            pc.remoteDescription = { sdp: `v=0\r\n${sections.map(section).join("")}` };
            reconcileRemoteMedia(pc);
        },
    };
}

// The video grid contract (video-grid.js): one tile per negotiated key, with a
// <video> bound to exactly the receiver stream stream-controls hands over.
function harness() {
    const tiles = new Map(), removed = [];
    const users = new Map(), owners = createReceiverRegistry();
    const p = peer();
    window.__noxa.state.pc = p.pc;
    controls.startStreamSession(p.pc, (key, stream) => {
        const tile = tiles.get(key) || { key, el: fakeElement(), video: {} };
        tile.video.srcObject = stream;
        tile.track = stream.getVideoTracks()[0];
        tiles.set(key, tile);
        return tile.el;
    }, key => {
        if (!tiles.has(key)) return;
        tiles.get(key).video.srcObject = null;
        tiles.delete(key);
        removed.push(key);
    });
    // main.js startVoice wiring for video and screen audio.
    const stop = startRemoteMedia(p.pc, () => true, (track, trackID) => {
        const [clientID, slot] = trackID.split("|");
        const publisher = window.__noxa.state.clients.find(client => client.client_id === clientID) || null;
        attributeReceiver(users, owners, track, trackID, clientID, publisher);
        if (track.kind === "video") controls.receiveStreamTrack(track, publisher, trackID);
        else if (slot === "screenaudio") controls.receiveShareAudio(track, clientID);
    }, (track, trackID) => {
        releaseReceiver(users, owners, track, trackID);
        if (track.kind === "video") controls.removeStreamTrack(track, trackID);
        else if (trackID.endsWith("|screenaudio")) controls.removeShareAudio(track, trackID.split("|")[0]);
    });
    return { ...p, tiles, removed, users, owners, stop };
}

const settle = async () => { for (let i = 0; i < 4; i++) await new Promise(resolve => setImmediate(resolve)); };

// stream-controls creates one <article> card per catalog entry; its
// ".stream-watch" button starts watching that publisher slot.
const watchCards = [];
document.createElement = tag => {
    const el = fakeElement(tag);
    if (tag === "article") watchCards.push(el);
    return el;
};

// watched starts a session whose catalog lists pa and pb for slot, then
// watches the given publishers before any receiver exists.
async function watched(slot, publishers = ["pa", "pb"]) {
    watchCards.length = 0;
    catalog.streams = ["pa", "pb"].map((publisher_id, i) => ({ publisher_id, slot, generation: String(i + 1), preview_at: 0, watch_revision: "0" }));
    const h = harness();
    await settle();
    await Promise.all(watchCards.filter(card => publishers.includes(card.dataset.publisher))
        .map(card => card.querySelector(".stream-watch").onclick()));
    await settle();
    return h;
}

for (const slot of ["cam", "screen"]) {
    test(`${slot}: a reused MID re-binds to its new publisher and a rejoin with a duplicate track.id keeps both tiles`, async () => {
        const h = await watched(slot);

        // pa publishes on MID 1.
        const x = h.add("1", "video", `pa|${slot}`);
        h.negotiate([{ mid: "1", id: `pa|${slot}` }]);
        assert.equal(h.tiles.get(`pa|${slot}`)?.track, x);
        assert.equal(x.enabled, true, "watched receiver plays");

        // pa leaves: its section goes idle and its tile is torn down.
        h.negotiate([{ mid: "1", direction: "inactive" }]);
        assert.equal(h.tiles.has(`pa|${slot}`), false);
        assert.equal(h.users.size, 0);

        // pb reuses pa's MID; the receiver keeps track.id "pa|<slot>".
        h.negotiate([{ mid: "1", id: `pb|${slot}` }]);
        assert.equal(x.id, `pa|${slot}`);
        assert.equal(h.tiles.get(`pb|${slot}`)?.track, x, "reused receiver renders pb");
        assert.equal(h.tiles.has(`pa|${slot}`), false, "nobody renders pb as pa");
        assert.equal(h.users.get(`pb|${slot}`)?.client_id, "pb");

        // pa rejoins on a new MID; Chromium gives that receiver the same id.
        const y = h.add("2", "video", `pa|${slot}`);
        h.negotiate([{ mid: "1", id: `pb|${slot}` }, { mid: "2", id: `pa|${slot}` }]);
        assert.equal(x.id, y.id, "two live receivers share one browser track id");
        assert.equal(h.tiles.get(`pa|${slot}`)?.track, y);
        assert.equal(h.tiles.get(`pb|${slot}`)?.track, x);
        assert.notEqual(h.tiles.get(`pa|${slot}`).video.srcObject, h.tiles.get(`pb|${slot}`).video.srcObject);
        assert.deepEqual([...h.users].map(([key, user]) => [key, user.client_id]).sort(), [[`pa|${slot}`, "pa"], [`pb|${slot}`, "pb"]]);

        // A stale release by the duplicate-id receiver never removes pa's tile.
        controls.removeStreamTrack(x, `pa|${slot}`);
        releaseReceiver(h.users, h.owners, x, `pa|${slot}`);
        assert.equal(h.tiles.get(`pa|${slot}`)?.track, y);
        assert.equal(h.users.get(`pa|${slot}`)?.client_id, "pa");

        // pb's receiver ends: only pb's tile and attribution go away.
        x.end();
        assert.equal(h.tiles.has(`pb|${slot}`), false);
        assert.equal(h.tiles.get(`pa|${slot}`)?.track, y, "pa keeps playing");
        assert.equal(h.users.has(`pb|${slot}`), false);
        assert.equal(h.users.get(`pa|${slot}`)?.client_id, "pa");

        // pa's section goes inactive: its tile is cleaned up.
        h.negotiate([{ mid: "1", direction: "inactive" }, { mid: "2", direction: "inactive" }]);
        assert.equal(h.tiles.size, 0);
        assert.equal(h.users.size, 0);
        h.stop();
        controls.stopStreamSession();
    });

    test(`${slot}: a MID handed directly from pa to pb releases pa's tile before pb binds`, async () => {
        const h = await watched(slot);
        const x = h.add("1", "video", `pa|${slot}`);
        h.negotiate([{ mid: "1", id: `pa|${slot}` }]);
        h.negotiate([{ mid: "1", id: `pb|${slot}` }]);
        assert.deepEqual([...h.tiles.keys()], [`pb|${slot}`]);
        assert.equal(h.tiles.get(`pb|${slot}`).track, x);
        assert.ok(h.removed.includes(`pa|${slot}`));
        h.stop();
        controls.stopStreamSession();
    });

    test(`${slot}: a receiver re-bound without a removal still releases its previous publisher`, async () => {
        const h = await watched(slot);
        const x = new FakeTrack(`pa|${slot}`, "video");
        controls.receiveStreamTrack(x, { client_id: "pa" }, `pa|${slot}`);
        assert.equal(h.tiles.get(`pa|${slot}`)?.track, x);
        controls.receiveStreamTrack(x, { client_id: "pb" }, `pb|${slot}`);
        assert.deepEqual([...h.tiles.keys()], [`pb|${slot}`]);
        // The old binding's late removal is a no-op for pb.
        controls.removeStreamTrack(x, `pa|${slot}`);
        assert.equal(h.tiles.get(`pb|${slot}`)?.track, x);
        h.stop();
        controls.stopStreamSession();
    });
}

test("receivers are never selected by browser track id, even without a track id", async () => {
    const h = await watched("cam");
    const x = new FakeTrack("pa|cam", "video");
    controls.receiveStreamTrack(x, { client_id: "pa" });
    assert.equal(h.tiles.size, 0, "a receiver without negotiated identity is not attributed by its browser id");
    assert.equal(x.enabled, false);
    h.stop();
    controls.stopStreamSession();
});

test("screen audio: pb reusing pa's MID and pa rejoining keep each share's audio with its own screen", async () => {
    // Watch only pb's screen.
    const h = await watched("screen", ["pb"]);

    const x = h.add("3", "audio", "pa|screenaudio");
    h.negotiate([{ mid: "3", kind: "audio", id: "pa|screenaudio" }]);
    assert.equal(x.enabled, false, "pa's share audio is not watched");
    h.negotiate([{ mid: "3", kind: "audio", direction: "inactive" }]);
    h.negotiate([{ mid: "3", kind: "audio", id: "pb|screenaudio" }]);
    assert.equal(x.enabled, true, "the reused receiver plays pb's watched share audio");
    const y = h.add("5", "audio", "pa|screenaudio");
    h.negotiate([{ mid: "3", kind: "audio", id: "pb|screenaudio" }, { mid: "5", kind: "audio", id: "pa|screenaudio" }]);
    assert.equal(x.id, y.id);
    assert.equal(x.enabled, true, "pb's share audio stays audible");
    assert.equal(y.enabled, false, "pa's unwatched share audio stays muted");
    assert.equal(h.users.get("pb|screenaudio")?.client_id, "pb");
    assert.equal(h.users.get("pa|screenaudio")?.client_id, "pa");

    // A stale removal for pa by the pb receiver changes nothing.
    controls.removeShareAudio(x, "pa");
    assert.equal(x.enabled, true);
    y.end();
    assert.equal(x.enabled, true, "pa's receiver ending leaves pb's audio alone");
    assert.equal(h.users.has("pa|screenaudio"), false);
    assert.equal(h.users.get("pb|screenaudio")?.client_id, "pb");
    h.negotiate([{ mid: "3", kind: "audio", direction: "inactive" }, { mid: "5", kind: "audio", direction: "inactive" }]);
    assert.equal(x.enabled, false);
    assert.equal(h.users.size, 0);
    h.stop();
    controls.stopStreamSession();
});

test("receiver registry binds track objects to negotiated keys and guards release", () => {
    const registry = createReceiverRegistry();
    const x = new FakeTrack("pa|cam", "video"), y = new FakeTrack("pa|cam", "video");
    assert.deepEqual(registry.bind("pa|cam", x, "x"), []);
    assert.deepEqual(registry.bind("pb|cam", x, "x2"), ["pa|cam"], "re-binding a track releases its previous key");
    assert.equal(registry.has("pa|cam"), false);
    assert.deepEqual(registry.bind("pa|cam", y, "y"), []);
    assert.equal(registry.keyOf(x), "pb|cam");
    assert.equal(registry.keyOf(y), "pa|cam");
    assert.equal(registry.owns("pa|cam", x), false);
    assert.equal(registry.release("pa|cam", x), false, "duplicate id cannot release another receiver");
    assert.equal(registry.get("pa|cam"), "y");
    assert.equal(registry.track("pa|cam"), y);
    assert.equal(registry.release("pa|cam", y), true);
    assert.equal(registry.release("pa|cam", y), false);
    assert.equal(registry.release("pb|cam", null), false);
    assert.equal(registry.size, 1);
    assert.deepEqual([...registry.keys()], ["pb|cam"]);
    registry.clear();
    assert.equal(registry.size, 0);
    assert.equal(registry.keyOf(x), null);
});
