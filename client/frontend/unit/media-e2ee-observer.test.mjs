// The platform key observer (desktop native capture) learns the issuing
// instance's owner, and a closed instance never notifies it.
import assert from "node:assert/strict";
import { test } from "node:test";

const { MediaE2EE, observeMediaKeys } = await import("../src/media-e2ee.js");

class IdleWorker { postMessage() {} terminate() {} }
const scope = { Worker: IdleWorker, crypto: globalThis.crypto, RTCRtpScriptTransform: function () {} };

test("key operations reach the observer with their owner until close", () => {
    const seen = [];
    observeMediaKeys((pid, op, owner) => seen.push([pid, op.op, owner]));
    try {
        const media = new MediaE2EE({ scope, workerURL: "idle", owner: "tab-a" });
        media.ready.catch(() => {});
        media.activate("self|screen", 3).catch(() => {});
        media.close();
        media.forget("self|screen").catch(() => {});
        assert.deepEqual(seen, [["self|screen", "activate", "tab-a"]]);
    } finally { observeMediaKeys(null); }
});
