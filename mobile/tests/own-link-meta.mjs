// own-link-meta.mjs — Node-side helper for the browser product harnesses
// (ratchet-product-browser.mjs, ratchet-interop-browser.mjs).
//
// The PWA trusts its own account root only from its verified desktop link
// statement (Task 4 review P1-1; direct.js #ownRoot). These harnesses boot
// the product DirectMessages directly from a fixture device instead of
// linking by QR, so this builds the statement the approving root device would
// have signed for that PWA device: approver = the account's desktop (a
// fixture device with private_key) or, for a PWA-only account, its first PWA
// device (by name). Synthetic test material only.
import nacl from "tweetnacl";

const b64 = bytes => Buffer.from(bytes).toString("base64");
const raw = text => new Uint8Array(Buffer.from(String(text).replace(/-----(BEGIN|END) [A-Z ]+-----/g, "").replace(/\s+/g, ""), "base64"));
const publicKeyPEM = key => `-----BEGIN PUBLIC KEY-----\n${b64(key)}\n-----END PUBLIC KEY-----\n`;
// keyFingerprint (core/crypto.js): first 16 hex of sha256(trimmed PEM) in 4 groups.
async function keyFingerprint(pem) {
    const { createHash } = await import("node:crypto");
    const h = createHash("sha256").update(pem.trim()).digest("hex").slice(0, 16);
    return `${h.slice(0, 4)} ${h.slice(4, 8)} ${h.slice(8, 12)} ${h.slice(12, 16)}`;
}

/**
 * {linkBinding, bindingAnchor, xPublic} for fixture device `device` among
 * `devices` (the fixture's name -> device map). xPublic (base64) is the
 * device's X25519 public key, derived from its secret when the fixture has
 * one, else a fixed synthetic key the statement is signed over.
 */
export async function ownLinkMeta(devices, device) {
    const same = Object.entries(devices).filter(([, d]) => d.principal === device.principal).sort(([a], [b]) => a.localeCompare(b)).map(([, d]) => d);
    const root = same.find(d => d.private_key) || same.find(d => d.edSecret) || device;
    const secret = raw(root.private_key || root.edSecret);
    const approverKey = secret.length === 64 ? secret.slice(32) : nacl.sign.keyPair.fromSeed(secret).publicKey;
    const signing = secret.length === 64 ? secret : nacl.sign.keyPair.fromSeed(secret).secretKey;
    const own = raw(device.edSecret).slice(32);
    const xPublic = device.x25519_private ? nacl.scalarMult.base(raw(device.x25519_private)) : new Uint8Array(32).fill(7);
    const binding = "tls-sha256:" + "00".repeat(32);
    const transcript = new TextEncoder().encode(`noxa-device-link-binding-v1\n${device.principal}\n${root.device}\n${publicKeyPEM(own)}\n${b64(xPublic)}\n${binding}`);
    const linkBinding = { binding, signature: b64(nacl.sign.detached(transcript, signing)), approverDeviceID: root.device, approverKey: publicKeyPEM(approverKey), principal: device.principal };
    return { linkBinding, bindingAnchor: await keyFingerprint(publicKeyPEM(approverKey)), xPublic: b64(xPublic) };
}
