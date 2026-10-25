import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import vm from 'node:vm';

async function fixture(stored = [], registration = async () => null) {
    const none = () => {};
    const sources = {
        './core/chat-session.js': { ChatSession: class {} }, './core/direct.js': { DirectMessages: class {} },
        './core/calls.js': { Calls: class {} }, './core/groups.js': { Groups: class {} }, './core/voice.js': { Voice: class {} },
        './core/connection.js': { Connection: class {} }, './core/crypto.js': { generateIdentity: none }, './core/device.js': { signIn: none },
        './core/identity-store.js': { clearIdentity: none, loadIdentity: none, saveIdentity: none, updateMeta: none },
        './core/outbox.js': { enqueue: none, flush: none, showPending: none, waitingCount: none },
        './core/push.js': { announceRead: none, closeNotifications: tag => closed.push(tag), reportVisibility: none, setBadge: none, syncPush: none },
        './core/settings.js': { get: () => [] }, './core/servers.js': { addServer: none },
        './notifier.js': { notifier: { install: none, decide: () => ({ count: true }), read: none } },
        './core/conversation-prefs.js': { activityShared: () => false, isMuted: () => false, syncMuted: none, watchCallLog: none, resetCallLog: none },
        './core/saved.js': { SavedMessages: class {} },
    };
    const storage = new Map([['noxa.callReminders', JSON.stringify(stored)]]);
    const timers = new Map(), closed = [], vibrations = [];
    let next = 0;
    const context = vm.createContext({ EventTarget, CustomEvent, console,
        window: new EventTarget(), document: new EventTarget(),
        navigator: { vibrate: value => vibrations.push(value), serviceWorker: { getRegistration: registration } },
        localStorage: { getItem: key => storage.get(key), setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key) },
        setTimeout: fn => { timers.set(++next, fn); return next; }, clearTimeout: id => timers.delete(id), clearInterval: none,
    });
    const mod = new vm.SourceTextModule(await readFile(new URL('../src/app.js', import.meta.url), 'utf8'), { context });
    await mod.link(spec => new vm.SyntheticModule(Object.keys(sources[spec]), function () {
        for (const [key, value] of Object.entries(sources[spec])) this.setExport(key, value);
    }, { context }));
    await mod.evaluate();
    return { api: mod.namespace, timers, storage, closed, vibrations };
}
const reminder = { uid: 'private-peer', name: 'Private contact', body: 'Call back', at: 1 };

test('sign-out clears stored callback reminders, timers and displayed notifications', async () => {
    const f = await fixture([reminder]);
    assert.equal(f.timers.size, 1);
    await f.api.forgetDevice();
    assert.equal(f.timers.size, 0);
    assert.equal(f.storage.has('noxa.callReminders'), false);
    assert.equal(f.api.reminders().length, 0);
    assert.deepEqual(f.closed, ['remind:private-peer']);
});

test('sign-out during service worker lookup cannot notify or restore old reminders', async () => {
    let resolve;
    const ready = new Promise(r => { resolve = r; });
    let notifications = 0;
    const f = await fixture([reminder], () => ready);
    const pending = [...f.timers.values()][0]();
    await f.api.forgetDevice();
    resolve({ showNotification: async () => { notifications++; } });
    await pending;
    assert.equal(notifications, 0);
    assert.equal(f.vibrations.length, 0);
    assert.equal(f.storage.has('noxa.callReminders'), false);
    assert.equal(f.timers.size, 0);
});

test('sign-out during notification delivery closes it after delivery finishes', async () => {
    let resolve, entered;
    const delivery = new Promise(r => { resolve = r; });
    const started = new Promise(r => { entered = r; });
    const f = await fixture([reminder], async () => ({ showNotification: () => { entered(); return delivery; } }));
    const pending = [...f.timers.values()][0]();
    await started;
    await f.api.forgetDevice();
    resolve();
    await pending;
    assert.equal(f.closed.at(-1), 'remind:private-peer');
    assert.equal(f.closed.length, 2);
    assert.equal(f.storage.has('noxa.callReminders'), false);
});

test('malformed reminder storage cannot break application startup', async () => {
    for (const value of [null, {}, [null, {}, { at: 'invalid' }]]) {
        const f = await fixture(value);
        assert.equal(f.api.reminders().length, 0);
        assert.equal(f.timers.size, 0);
    }
});
