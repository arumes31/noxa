import assert from "node:assert/strict";
import { test } from "node:test";

import { _resetUploads, cancelUpload, dismissUpload, enqueueUpload, onUploadsChanged, pendingUploads, retryUpload, setUploadConnection, uploads } from "../src/core/upload-queue.js";

const file = (name, size = 3) => new File([new Uint8Array(size)], name);
globalThis.location ??= { protocol: "http:", host: "127.0.0.1" };
const settle = () => new Promise(resolve => setTimeout(resolve, 0));

test("without a connection an upload fails, can be retried and dismissed", async () => {
    _resetUploads();
    setUploadConnection(() => null);
    const changes = [];
    const stop = onUploadsChanged(item => changes.push(item.status));
    const item = enqueueUpload(file("a.txt"), { channelID: 3, folder: "docs" });
    assert.deepEqual(item.target, { channelID: 3, folder: "docs", sealed: false, name: "a.txt" });
    await settle();
    assert.equal(item.status, "error");
    assert.match(item.error, /not connected/);
    assert.ok(changes.includes("active"));
    retryUpload(item.id);
    await settle();
    assert.equal(item.status, "error");
    let rejected = false;
    item.done.catch(() => { rejected = true; });
    dismissUpload(item.id);
    await settle();
    assert.equal(rejected, true);
    assert.equal(uploads().includes(item), false);
    stop();
});

test("queued uploads cancel without starting", async () => {
    _resetUploads();
    // A connection that never answers keeps the first upload active.
    let release;
    setUploadConnection(() => ({ open: true, request: () => new Promise(resolve => { release = resolve; }) }));
    const first = enqueueUpload(file("one"), { channelID: 1 });
    const second = enqueueUpload(file("two"), { channelID: 1 });
    await settle();
    assert.equal(first.status, "active", first.error);
    assert.equal(second.status, "queued");
    assert.equal(pendingUploads().length, 2);
    let canceled = false;
    second.done.catch(error => { canceled = error.name === "AbortError"; });
    cancelUpload(second.id);
    await settle();
    assert.equal(second.status, "canceled");
    assert.equal(canceled, true);
    cancelUpload(first.id);
    release?.({});
    await settle();
    assert.equal(first.status, "canceled");
    _resetUploads();
});
