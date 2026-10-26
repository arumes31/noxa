// Deterministic PUBLIC TEST KEYS only. Run from repository root after npm --prefix mobile ci.
import fs from "node:fs";
import nacl from "../mobile/node_modules/tweetnacl/nacl-fast.js";
import { bundleTranscript } from "../mobile/src/core/olm-directory.js";
import { approvalTranscript } from "../mobile/src/core/dm-trust.js";
const b64 = (b) => Buffer.from(b).toString("base64");
const raw = (b) => b64(b).replace(/=+$/, "");
const ed = (n) => nacl.sign.keyPair.fromSeed(new Uint8Array(32).fill(n));
const x = (n) =>
    nacl.box.keyPair.fromSecretKey(new Uint8Array(32).fill(n)).publicKey;
const root = ed(1),
    child = ed(2),
    olm = ed(3);
const pem = (k) =>
    `-----BEGIN PUBLIC KEY-----\n${b64(k.publicKey)}\n-----END PUBLIC KEY-----\n`;
const principal = "fixture-üser",
    server = "tls-sha256:" + "a".repeat(64),
    now = 1800000000;
const devices = [
    {
        device_id: "root",
        kind: "desktop",
        first_device: true,
        ed25519_public_key: pem(root),
        x25519_public_key: b64(x(1)),
    },
    {
        device_id: "phone",
        kind: "web",
        ed25519_public_key: pem(child),
        x25519_public_key: b64(x(2)),
        approved_by: "root",
    },
];
devices[1].approval_signature = b64(
    nacl.sign.detached(
        approvalTranscript(principal, "root", pem(child), b64(x(2))),
        root.secretKey,
    ),
);
const bundle = {
    version: "noxa-olm-directory-v1",
    protocol: "noxa-olm-v1",
    server,
    principal,
    device_id: "phone",
    generation: 1,
    issued_at: now - 10,
    expires_at: now + 3600,
    identity: raw(x(3)),
    signing: raw(olm.publicKey),
    one_time_keys: [
        { id: "AAAAAAAAAAE", key: raw(x(4)) },
        { id: "AAAAAAAAAAI", key: raw(x(5)) },
    ],
};
bundle.signature = b64(
    nacl.sign.detached(bundleTranscript(bundle), child.secretKey),
);
const base = {
    server,
    principal,
    device_id: "phone",
    root_signing: b64(root.publicKey),
    root_encryption: b64(x(1)),
    devices,
    bundle,
    key_id: bundle.one_time_keys[0].id,
    now,
    transcript: b64(bundleTranscript(bundle)),
};
const chainCases = [];
function chain(name, mutate, ids) {
    let ds = structuredClone(devices);
    mutate(ds);
    chainCases.push({ name, devices: ds, ids });
}
chain("valid", () => {}, ["root", "phone"]);
chain("duplicate", (ds) => ds.push(ds[1]), []);
chain("two-roots", (ds) => (ds[1].first_device = true), []);
chain("changed-root-x", (ds) => (ds[0].x25519_public_key = b64(x(9))), []);
chain("revoked-root", (ds) => (ds[0].revoked_at = now), []);
chain("revoked-child", (ds) => (ds[1].revoked_at = now), ["root"]);
chain("dangling", (ds) => (ds[1].approved_by = "unknown"), ["root"]);
chain("cycle", (ds) => (ds[1].approved_by = "phone"), ["root"]);
chain("empty-id", (ds) => (ds[1].device_id = ""), []);
chain(
    "bad-child-key",
    (ds) => (ds[1].x25519_public_key = b64(new Uint8Array(32))),
    ["root"],
);
chain("untrusted-root", (ds) => (ds[0].ed25519_public_key = pem(olm)), []);
chain("missing-root", (ds) => (ds[0].first_device = false), []);
chain(
    "oversized-list",
    (ds) => {
        while (ds.length < 65)
            ds.push({ ...ds[1], device_id: "extra-" + ds.length });
    },
    [],
);
const bundleCases = [];
function bad(name, mutate) {
    const b = structuredClone(bundle);
    mutate(b);
    bundleCases.push({ name, bundle: b });
}
bad("wrong-server", (b) => (b.server = "tls-sha256:" + "b".repeat(64)));
bad("wrong-principal", (b) => (b.principal = "other"));
bad("wrong-device", (b) => (b.device_id = "root"));
bad("tampered-key", (b) => (b.one_time_keys[0].key = raw(x(6))));
bad("duplicate-id", (b) => (b.one_time_keys[1].id = b.one_time_keys[0].id));
bad("unsafe-generation", (b) => (b.generation = 9007199254740992));
bad("noncanonical-base64", (b) => (b.identity += "="));
bad("zero-curve", (b) => (b.identity = raw(new Uint8Array(32))));
bad("small-order-signing", (b) => (b.signing = raw(new Uint8Array(32))));
bad("expired", (b) => (b.expires_at = now - 1));
bad(
    "self-signed-olm",
    (b) =>
        (b.signature = b64(
            nacl.sign.detached(bundleTranscript(b), olm.secretKey),
        )),
);
fs.mkdirSync("internal/e2ee/olmdir/testdata", { recursive: true });
fs.writeFileSync(
    "internal/e2ee/olmdir/testdata/directory-v1.json",
    JSON.stringify(
        { ...base, chain_cases: chainCases, bundle_cases: bundleCases },
        null,
        2,
    ) + "\n",
);
