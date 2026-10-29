import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import {
    bundleTranscript,
    verifyOlmClaim,
    serverBinding,
} from "../src/core/olm-directory.js";
import { trustedDevices } from "../src/core/dm-trust.js";
const fixture = JSON.parse(
    readFileSync(
        new URL(
            "../../internal/e2ee/olmdir/testdata/directory-v1.json",
            import.meta.url,
        ),
    ),
);
const bytes = (s) => new Uint8Array(Buffer.from(s, "base64"));
const expected = {
    server: fixture.server,
    principal: fixture.principal,
    deviceID: fixture.device_id,
    pinnedOlmIdentity: fixture.bundle.identity,
    keyID: fixture.key_id,
    rootSigning: bytes(fixture.root_signing),
    rootEncryption: bytes(fixture.root_encryption),
    devices: fixture.devices,
    now: fixture.now,
};
test("shared UTF-8 transcript and signed noXa device bundle", () => {
    assert.equal(
        Buffer.from(bundleTranscript(fixture.bundle)).toString("base64"),
        fixture.transcript,
    );
    assert.equal(verifyOlmClaim(fixture.bundle, expected).id, fixture.key_id);
    assert.throws(() =>
        verifyOlmClaim(fixture.bundle, {
            ...expected,
            pinnedOlmIdentity: "changed",
        }),
    );
    assert.throws(() => serverBinding(""));
    assert.throws(() => serverBinding("display.example"));
});
for (const c of fixture.chain_cases)
    test(`shared device chain: ${c.name}`, () =>
        assert.deepEqual(
            trustedDevices(
                fixture.principal,
                c.devices,
                expected.rootSigning,
                expected.rootEncryption,
            ).map((d) => d.deviceID),
            c.ids,
        ));
for (const c of fixture.bundle_cases)
    test(`shared bundle rejects: ${c.name}`, () =>
        assert.throws(() => verifyOlmClaim(c.bundle, expected)));
