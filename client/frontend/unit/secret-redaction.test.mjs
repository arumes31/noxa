import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { REDACTED, containsPairingSecret, redactPairingSecrets, redactPairingSecretsDeep } from "../src/secret-redaction.js";

const link = "https://chat.example.test/link#id=QmFzZTY0VXJsSWRIZXJlQUE&k=c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmV0c2VjcmU";

describe("redactPairingSecrets", () => {
    it("masks the fragment of a device-link URL but keeps the origin", () => {
        const output = redactPairingSecrets(`open ${link} on your phone`);
        assert.equal(output, `open https://chat.example.test/link#${REDACTED} on your phone`);
        assert.ok(!output.includes("c2VjcmV0"));
    });

    it("masks secret-carrying query and fragment parameters anywhere", () => {
        assert.equal(redactPairingSecrets("https://x.test/app/?token=abc123&page=2"), `https://x.test/app/?token=${REDACTED}&page=2`);
        assert.equal(redactPairingSecrets("https://x.test/other#k=zzz&id=keepme"), `https://x.test/other#k=${REDACTED}&id=keepme`);
    });

    it("masks formatted pairing codes but not numeric ranges", () => {
        assert.equal(redactPairingSecrets("Code: 7KQ3-PX2M"), `Code: ${REDACTED}`);
        assert.equal(redactPairingSecrets("resolution 1080-1920 stays"), "resolution 1080-1920 stays");
        assert.equal(redactPairingSecrets("codec VP8-RTX1"), "codec VP8-RTX1");
    });

    it("masks JSON secret members and keeps the document valid", () => {
        const payload = JSON.stringify({ link_id: "abc", secret: "c2VjcmV0", code: "7KQ3-PX2M", qr: "data:image/png;base64," + "A".repeat(200), url: link, codec: "opus", code_point: 7, k: "x" });
        const output = redactPairingSecrets(payload);
        const parsed = JSON.parse(output);
        assert.equal(parsed.secret, REDACTED);
        assert.equal(parsed.code, REDACTED);
        assert.equal(parsed.k, REDACTED);
        assert.equal(parsed.qr, REDACTED);
        assert.equal(parsed.url, `https://chat.example.test/link#${REDACTED}`);
        assert.equal(parsed.codec, "opus");
        assert.equal(parsed.code_point, 7);
        assert.equal(parsed.link_id, "abc");
    });

    it("leaves ordinary diagnostics and error codes alone", () => {
        const text = JSON.stringify({ code: "NotAllowedError", sampled_at: 12, direction: "sent", frames: [1, 2, 3], url: "https://x.test/app/" });
        assert.equal(redactPairingSecrets(text), text);
        assert.equal(containsPairingSecret(text), false);
        assert.equal(containsPairingSecret(link), true);
    });

    it("masks long QR data URLs", () => {
        const output = redactPairingSecrets("img data:image/png;base64," + "Q".repeat(100) + " end");
        assert.equal(output, `img data:image/png;base64,${REDACTED} end`);
    });

    it("returns non-strings unchanged", () => {
        assert.equal(redactPairingSecrets(null), null);
        assert.equal(redactPairingSecrets(undefined), undefined);
        assert.equal(redactPairingSecrets(""), "");
        assert.equal(redactPairingSecrets(42), 42);
    });
});

describe("redactPairingSecretsDeep", () => {
    it("masks secret keys and nested strings while keeping the shape", () => {
        const input = { link_id: "id1", secret: "s", nested: [{ qr: "data:image/png;base64,AAAA" }, "see " + link], count: 3, code: "7KQ3-PX2M" };
        const output = redactPairingSecretsDeep(input);
        assert.deepEqual(output, { link_id: "id1", secret: REDACTED, nested: [{ qr: REDACTED }, `see https://chat.example.test/link#${REDACTED}`], count: 3, code: REDACTED });
        assert.notEqual(output, input);
        assert.equal(input.secret, "s");
    });
});
