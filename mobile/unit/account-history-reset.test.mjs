import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import vm from 'node:vm';

function deferred() { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; }
async function moduleWithStorage(file, { load = async () => [], save = async () => {}, remove = async () => {} } = {}) {
    const context = vm.createContext({ EventTarget, CustomEvent, console });
    const db = { Stores: { calls: 'calls', saved: 'saved' }, getSealed: load, putSealed: save, deleteKey: remove };
    const mod = new vm.SourceTextModule(await readFile(new URL(`../src/core/${file}`, import.meta.url), 'utf8'), { context });
    await mod.link(() => new vm.SyntheticModule(Object.keys(db), function () { for (const [key, value] of Object.entries(db)) this.setExport(key, value); }, { context }));
    await mod.evaluate();
    return mod.namespace;
}

test('call-log reset does not expose a previous account cache or late read', async () => {
    let stored = [{ id: 'old', peer: 'peer', startedAt: 1 }];
    const read = deferred(); let delayed = false;
    const log = await moduleWithStorage('call-log.js', { load: () => delayed ? read.promise : Promise.resolve(stored) });
    assert.equal((await log.callsWith('peer')).length, 1);
    await log.resetCallLog(); stored = [];
    assert.equal((await log.callsWith('peer')).length, 0);
    delayed = true;
    const pending = log.callsWith('another-peer');
    await log.resetCallLog();
    read.resolve([{ id: 'stale', peer: 'another-peer' }]);
    assert.equal((await pending).length, 0);
    delayed = false;
    assert.equal((await log.callsWith('another-peer')).length, 0);
});

test('call-log reset drains active writes and ignores events from old calls', async () => {
    const saving = deferred(), entered = deferred(); let writes = 0;
    const log = await moduleWithStorage('call-log.js', { save: () => { writes++; entered.resolve(); return saving.promise; } });
    const calls = new EventTarget(); log.watchCallLog(calls);
    const pending = log.recordCall({ id: 'old', peer: 'peer', startedAt: 1 });
    await entered.promise;
    let finished = false; const reset = log.resetCallLog().then(() => { finished = true; });
    await Promise.resolve(); assert.equal(finished, false);
    calls.dispatchEvent(new CustomEvent('record', { detail: { id: 'late', peer: 'peer' } }));
    saving.resolve(); await pending; await reset;
    assert.equal(writes, 1);
    assert.equal((await log.callsWith('peer')).length, 0);
});

test('saved-message clear invalidates a pending load and save', async () => {
    const read = deferred(); let writes = 0;
    const { SavedMessages } = await moduleWithStorage('saved.js', { load: () => read.promise, save: async () => { writes++; } });
    const saved = new SavedMessages();
    const pending = saved.save({ kind: 'dm', peer: 'private-peer', clientMsgID: 'late' });
    const clearing = saved.clear();
    read.resolve([{ kind: 'dm', peer: 'private-peer', clientMsgID: 'old' }]);
    await clearing; await pending;
    assert.equal(saved.list().length, 0);
    assert.equal(writes, 0);
});

test('saved-message clear waits for writes so account deletion happens last', async () => {
    const saving = deferred(), entered = deferred(); const writes = [];
    const { SavedMessages } = await moduleWithStorage('saved.js', { save: (_store, _key, rows) => { writes.push(structuredClone(rows)); entered.resolve(); return saving.promise; } });
    const saved = new SavedMessages();
    await saved.save({ kind: 'dm', peer: 'peer', clientMsgID: 'one' }, 'Private collection');
    await entered.promise;
    let cleared = false; const clearing = saved.clear().then(() => { cleared = true; });
    await Promise.resolve(); assert.equal(cleared, false);
    saving.resolve(); await clearing;
    assert.equal(saved.list().length, 0);
    assert.equal(writes[0][0].collection, 'Private collection');
});
