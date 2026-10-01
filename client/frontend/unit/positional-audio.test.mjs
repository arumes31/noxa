import assert from "node:assert/strict";
import { test } from "node:test";
import { SpatialVoice } from "../src/positional-audio.js";

test("spatial voice preserves bypass, pans matching contexts and resets stale positions", () => {
    const scalar = () => ({ value: 0, setTargetAtTime(value) { this.value = value; } });
    const listener = Object.fromEntries(["positionX", "positionY", "positionZ", "forwardX", "forwardY", "forwardZ", "upX", "upY", "upZ"].map(key => [key, scalar()]));
    const nodes = [];
    const ctx = { currentTime: 0, listener, createPanner() {
        const p = { positionX: scalar(), positionY: scalar(), positionZ: scalar(), connect() {}, disconnect() {} }; nodes.push(p); return p;
    } };
    const destination = {};
    const source = { connections: [], connect(target) { this.connections.push(target); }, disconnect() { this.connections = []; } };
    const spatial = new SpatialVoice(ctx);
    spatial.attach("peer", source, destination);
    assert.equal(source.connections[0], destination);
    spatial.local({ x: 2, y: 0, z: 0, context: "map", forward: [0, 0, -1], up: [0, 1, 0] }, 1000);
    spatial.remote("peer", { x: 7, y: 0, z: 0, context: "map" }, 1000);
    spatial.update(true, 1000);
    assert.equal(source.connections[0], nodes[0]);
    assert.equal(nodes[0].positionX.value, 7);
    assert.equal(listener.positionX.value, 2);
    spatial.retainPeers([]);
    spatial.update(true, 1000);
    assert.equal(source.connections[0], destination);
    spatial.remote("peer", { x: 7, y: 0, z: 0, context: "map" }, 1000);
    spatial.update(true, 1000);
    spatial.reset();
    assert.equal(source.connections[0], destination);
    spatial.update(true, 1000);
    assert.equal(source.connections[0], destination);
    spatial.update(true, 5001);
    assert.equal(source.connections[0], destination);
    spatial.update(false, 1000);
    assert.equal(source.connections[0], destination);
    spatial.detach("peer");
    assert.equal(source.connections.length, 0);
});

test("different game contexts and unknown peers remain ordinary audio", () => {
    const ctx = { currentTime: 0, listener: {}, createPanner() { throw new Error("must not create a panner"); } };
    const destination = {};
    const source = { target: null, connect(target) { this.target = target; }, disconnect() {} };
    const spatial = new SpatialVoice(ctx);
    spatial.attach("peer", source, destination);
    spatial.local({ x: 0, y: 0, z: 0, context: "map-a", forward: [0, 0, -1], up: [0, 1, 0] }, 1000);
    spatial.remote("peer", { x: 10, y: 0, z: 0, context: "map-b" }, 1000);
    spatial.update(true, 1000);
    assert.equal(source.target, destination);
});
