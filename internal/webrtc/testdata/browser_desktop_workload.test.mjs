import test from 'node:test';
import assert from 'node:assert/strict';
import { desktopPhase, drawDesktopScene } from './browser_desktop_workload.mjs';

test('desktop scene cycles through three complete deterministic 20-second phases', () => {
    for (const [time, name, cycle] of [[0, 'static-text', 0], [19999, 'static-text', 0], [20000, 'scrolling-text', 0],
        [39999, 'scrolling-text', 0], [40000, 'motion', 0], [59999, 'motion', 0], [60000, 'static-text', 1], [80000, 'scrolling-text', 1]]) {
        const phase = desktopPhase(time);
        assert.equal(phase.name, name); assert.equal(phase.cycle, cycle);
        assert.ok(phase.phase_elapsed_ms >= 0 && phase.phase_elapsed_ms < 20000);
    }
    for (const time of [-1, NaN, Infinity, '0']) assert.throws(() => desktopPhase(time));
});

test('drawing is deterministic, covers full capture geometry and separates static/scroll/motion content', () => {
    const draw = time => {
        const calls = [];
        const context = new Proxy({}, { get: (_target, key) => (...args) => calls.push([key, ...args]),
            set: (_target, key, value) => { calls.push([key, value]); return true; } });
        drawDesktopScene(context, time, 1920, 1080);
        return calls;
    };
    const first = draw(1000);
    assert.deepEqual(first, draw(1000));
    assert.ok(first.some(row => row[0] === 'fillRect' && row.slice(1).join() === '0,0,1920,1080'));
    assert.equal(first.filter(row => row[0] === 'save').length, first.filter(row => row[0] === 'restore').length);
    const text = calls => calls.filter(row => row[0] === 'fillText' && row[1].startsWith('Readable'));
    const arcs = calls => calls.filter(row => row[0] === 'arc');
    assert.deepEqual(text(first), text(draw(2000)), 'static text does not scroll');
    assert.deepEqual(arcs(first), arcs(draw(2000)), 'static panel does not move');
    assert.notDeepEqual(text(draw(20100)), text(draw(20200)), 'scroll phase moves readable text');
    assert.deepEqual(text(draw(40100)), text(draw(40200)), 'motion phase leaves readable text steady');
    assert.notDeepEqual(arcs(draw(40100)), arcs(draw(40200)), 'motion phase moves the panel');
});
