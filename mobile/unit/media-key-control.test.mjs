// The media-key control body built by the shared MediaKeyEngine is a valid
// Task 1c control (same rules as the Go admission, shared vectors) and its
// archive form keeps no key material.
import test from "node:test";
import assert from "node:assert/strict";
import { buildMediaKeyBody, parseMediaKeyControl } from "../src/core/media-e2ee/media-keys.js";
import { controlArchiveBody, judgeControl, validateControlSend } from "../src/core/olm-admission.js";

const keys = Object.fromEntries(["mic", "cam", "screen", "screenaudio"].map((slot, i) => [slot, new Uint8Array(32).fill(i + 7)]));
const now = 1_800_000_000;

test("an engine media-key body passes the control send and receive rules", () => {
    const body = buildMediaKeyBody({ channelID: 42, epoch: 1_800_000_000_123, nowSeconds: now, client: "c-abcdef0123456789", session: "s".repeat(22), kid: 3, members: "m".repeat(43), count: 160, keys });
    const sent = validateControlSend(body, now);
    const raw = JSON.stringify(body);
    const judged = judgeControl(raw, now + 5);
    assert.equal(judged.status, "delivered");
    const parsed = parseMediaKeyControl({ ...judged.body }, 42);
    assert.equal(parsed.ok, true);
    assert.deepEqual([...parsed.value.keys.screen], [...keys.screen]);
    assert.ok(raw.length < 1024, "far below the 4 KiB data bound");
    const archived = JSON.stringify(controlArchiveBody(sent));
    assert.ok(!archived.includes("keys") && !archived.includes(Buffer.from(keys.mic).toString("base64")), "no key material at rest");
    assert.equal(judgeControl(raw, now + 121).status, "expired", "an expired retry is never handed out");
});
