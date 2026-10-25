import { describe, it } from "node:test";
import assert from "node:assert/strict";
import {
    aggregateCallConnections, callConnectionAnnouncement, callConnectionPhase, describeCallConnection,
    initialCallConnection, normalizeTransportState, reduceCallConnection, trackIsFlowing,
} from "../src/call-connection-state.js";

const run = (...events) => events.reduce((state, event) => reduceCallConnection(state, event), initialCallConnection());

describe("call connection state", () => {
    it("starts connecting until the transport is up", () => {
        assert.equal(callConnectionPhase(initialCallConnection()), "connecting");
        assert.equal(callConnectionPhase(run({ type: "transport", state: "connecting" })), "connecting");
        assert.equal(callConnectionPhase(run({ type: "transport", state: "checking" })), "connecting");
        assert.equal(callConnectionPhase(run({ type: "transport", state: "connected" })), "connected");
        assert.equal(callConnectionPhase(run({ type: "transport", state: "completed" })), "connected");
    });

    it("reaches media only when audio flows over a connected transport", () => {
        const sendOnly = run({ type: "transport", state: "connected" }, { type: "media", direction: "send", active: true });
        assert.equal(callConnectionPhase(sendOnly), "media");
        const receiveOnly = run({ type: "transport", state: "connected" }, { type: "media", direction: "receive", active: true });
        assert.equal(callConnectionPhase(receiveOnly), "media");
        // Flow flags recorded before the transport is up never promote the phase.
        const early = run({ type: "media", direction: "receive", active: true });
        assert.equal(callConnectionPhase(early), "connecting");
        assert.equal(callConnectionPhase(reduceCallConnection(sendOnly, { type: "media", direction: "send", active: false })), "connected");
    });

    it("drops media flags when the transport goes away and resets cleanly", () => {
        const flowing = run({ type: "transport", state: "connected" }, { type: "media", direction: "send", active: true }, { type: "media", direction: "receive", active: true });
        const dropped = reduceCallConnection(flowing, { type: "transport", state: "disconnected" });
        assert.deepEqual(dropped, { transport: "connecting", sending: false, receiving: false });
        assert.deepEqual(reduceCallConnection(flowing, { type: "transport", state: "failed" }), { transport: "failed", sending: false, receiving: false });
        assert.deepEqual(reduceCallConnection(flowing, { type: "reset" }), initialCallConnection());
    });

    it("returns the same object for no-op events", () => {
        const state = run({ type: "transport", state: "connected" });
        assert.equal(reduceCallConnection(state, { type: "transport", state: "connected" }), state);
        assert.equal(reduceCallConnection(state, { type: "media", direction: "send", active: false }), state);
        assert.equal(reduceCallConnection(state, { type: "media", direction: "sideways", active: true }), state);
        assert.equal(reduceCallConnection(state, { type: "unknown" }), state);
        assert.equal(reduceCallConnection(state), state);
    });

    it("normalizes peer connection and ICE states", () => {
        assert.equal(normalizeTransportState("completed"), "connected");
        assert.equal(normalizeTransportState("disconnected"), "connecting");
        assert.equal(normalizeTransportState("checking"), "connecting");
        assert.equal(normalizeTransportState("closed"), "closed");
        assert.equal(normalizeTransportState("new"), "new");
        assert.equal(normalizeTransportState(undefined), "connecting");
    });
});

describe("group call aggregation", () => {
    it("is connecting while any peer is still coming up", () => {
        const up = run({ type: "transport", state: "connected" }, { type: "media", direction: "receive", active: true });
        const pending = run({ type: "transport", state: "connecting" });
        assert.equal(callConnectionPhase(aggregateCallConnections([up, pending])), "connecting");
        assert.equal(callConnectionPhase(aggregateCallConnections([])), "connecting");
        assert.equal(callConnectionPhase(aggregateCallConnections(new Map([["a", up]]).values())), "media");
    });

    it("merges media flags across connected peers", () => {
        const sending = run({ type: "transport", state: "connected" }, { type: "media", direction: "send", active: true });
        const quiet = run({ type: "transport", state: "connected" });
        const merged = aggregateCallConnections([sending, quiet]);
        assert.deepEqual(merged, { transport: "connected", sending: true, receiving: false });
        assert.equal(callConnectionPhase(aggregateCallConnections([quiet, quiet])), "connected");
        assert.equal(aggregateCallConnections([run({ type: "transport", state: "failed" })]).transport, "failed");
    });
});

describe("call connection presentation", () => {
    it("pairs every phase with text keys and a distinct icon", () => {
        const connecting = describeCallConnection(initialCallConnection());
        assert.deepEqual([connecting.phase, connecting.key, connecting.icon, connecting.detailKey], ["connecting", "call.link.connecting", "refresh", null]);
        const connected = describeCallConnection(run({ type: "transport", state: "connected" }));
        assert.deepEqual([connected.phase, connected.key, connected.icon, connected.detailKey], ["connected", "call.link.connected", "phone", null]);
        const both = describeCallConnection(run({ type: "transport", state: "connected" }, { type: "media", direction: "send", active: true }, { type: "media", direction: "receive", active: true }));
        assert.deepEqual([both.phase, both.key, both.icon, both.detailKey], ["media", "call.link.media", "activity", "call.link.media.both"]);
        assert.equal(describeCallConnection(run({ type: "transport", state: "connected" }, { type: "media", direction: "send", active: true })).detailKey, "call.link.media.sending");
        assert.equal(describeCallConnection(run({ type: "transport", state: "connected" }, { type: "media", direction: "receive", active: true })).detailKey, "call.link.media.receiving");
        assert.equal(describeCallConnection(null).phase, "connecting");
        assert.equal(new Set([connecting.icon, connected.icon, both.icon]).size, 3);
    });

    it("announces phase changes once", () => {
        assert.equal(callConnectionAnnouncement(null, "connecting"), "call.link.announce.connecting");
        assert.equal(callConnectionAnnouncement("connecting", "connected"), "call.link.announce.connected");
        assert.equal(callConnectionAnnouncement("connected", "media"), "call.link.announce.media");
        assert.equal(callConnectionAnnouncement("media", "media"), null);
        assert.equal(callConnectionAnnouncement("media", null), null);
    });

    it("treats a live unmuted track as flowing", () => {
        assert.equal(trackIsFlowing({ readyState: "live", muted: false }), true);
        assert.equal(trackIsFlowing({ readyState: "live" }), true);
        assert.equal(trackIsFlowing({ readyState: "live", muted: true }), false);
        assert.equal(trackIsFlowing({ readyState: "ended", muted: false }), false);
        assert.equal(trackIsFlowing(null), false);
    });
});
