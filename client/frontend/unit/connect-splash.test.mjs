import test from "node:test";
import assert from "node:assert/strict";
import { CONNECT_QUIP_COUNT, connectEnglish, connectGerman, quipKey, shuffledQuips } from "../src/connect-messages.js";
import { choreographies, logoGeometry } from "../src/logo-motion.js";

test("at least fifty playful lines in English and German, same keys", () => {
    assert.ok(CONNECT_QUIP_COUNT >= 50);
    for (let i = 0; i < CONNECT_QUIP_COUNT; i++) {
        const key = quipKey(i);
        assert.ok(connectEnglish[key]?.length > 3, `English ${key}`);
        assert.ok(connectGerman[key]?.length > 3, `German ${key}`);
    }
    assert.equal(new Set(Object.values(connectEnglish)).size, Object.keys(connectEnglish).length, "no duplicate English lines");
    assert.equal(new Set(Object.values(connectGerman)).size, Object.keys(connectGerman).length, "no duplicate German lines");
    assert.match(connectEnglish["connect.title"], /\{host\}/);
    assert.match(connectGerman["connect.title"], /\{host\}/);
    assert.deepEqual(Object.keys(connectEnglish).sort(), Object.keys(connectGerman).sort());
});

test("lines come in a random order with no repeats inside a round or across rounds", () => {
    let seed = 7;
    const random = () => { seed = (seed * 16807) % 2147483647; return seed / 2147483647; };
    const first = shuffledQuips(CONNECT_QUIP_COUNT, random);
    assert.equal(new Set(first).size, CONNECT_QUIP_COUNT, "every line once per round");
    assert.notDeepEqual(first, [...first].sort((x, y) => x - y), "not in catalog order");
    // A random source that leaves the order untouched would start the next
    // round with index 0; when 0 was the line just shown it must move.
    const next = shuffledQuips(CONNECT_QUIP_COUNT, () => 0.999999, 0);
    assert.notEqual(next[0], 0, "a new round never starts with the line just shown");
    assert.equal(new Set(next).size, CONNECT_QUIP_COUNT);
});

test("trace draws the ring first, then the X, then the voice bars, and settles home", () => {
    const packets = logoGeometry();
    const trace = choreographies.trace;
    const visibleAt = (part, time) => packets.filter(p => p.part === part && trace.place(p, time).alpha > 0).length;
    assert.ok(visibleAt("ring", 0.5) > 0);
    assert.equal(visibleAt("x", 0.5), 0, "the X waits for the ring");
    assert.equal(visibleAt("bar", 1.2), 0, "the bars wait for the X");
    for (const p of packets) {
        const home = trace.place(p, trace.settle + 1);
        assert.equal(home.alpha, 1);
        assert.ok(Math.abs(home.dx) < 1e-9 && Math.abs(home.dy) < 1e-9);
    }
});

test("converge brings every packet home from outside the frame", () => {
    const packets = logoGeometry();
    const converge = choreographies.converge;
    const far = packets.filter(p => { const s = converge.place(p, 0); return Math.hypot(s.dx, s.dy) > 250; });
    assert.ok(far.length > packets.length * 0.9);
    for (const p of packets) {
        const home = converge.place(p, converge.settle + 0.5);
        assert.equal(home.alpha, 1);
        assert.ok(Math.abs(home.dx) < 1e-6 && Math.abs(home.dy) < 1e-6);
    }
});
