import { utf8, sign, toBase64, fromBase64 } from "./crypto.js";
import { bundleTranscript } from "./olm-directory.js";
import {
  validText,
  validCurve,
  validSigning,
  verifySignature,
} from "./olm-keys.js";
import { digestBytes } from "./olm-admission.js";
const fail = () => new Error("invalid Olm reset"),
  safe = (n) => Number.isSafeInteger(n) && n > 0,
  hash = (s) => /^[a-f0-9]{64}$/.test(s);
export async function publicationDigest(bundle) {
  const transcript = bundleTranscript(bundle),
    sig = fromBase64(bundle.signature);
  if (sig.length !== 64) throw fail();
  const b = new Uint8Array(transcript.length + sig.length);
  b.set(transcript);
  b.set(sig, transcript.length);
  return digestBytes(b);
}
export function resetTranscript(r) {
  if (
    !r ||
    r.version !== "noxa-olm-reset-v1" ||
    !/^tls-sha256:[a-f0-9]{64}$/.test(r.server) ||
    ![r.principal, r.device, r.transition_id].every(validText) ||
    !safe(r.old_epoch) ||
    r.new_epoch !== r.old_epoch + 1 ||
    !safe(r.new_epoch) ||
    !safe(r.generation) ||
    r.generation < 2 ||
    !validCurve(r.old_identity) ||
    !validCurve(r.new_identity) ||
    r.old_identity === r.new_identity ||
    !validSigning(r.old_signing) ||
    !validSigning(r.new_signing) ||
    !hash(r.publication_digest) ||
    !["lost", "restored"].includes(r.reason)
  )
    throw fail();
  const fields = [
    r.version,
    r.server,
    r.principal,
    r.device,
    r.transition_id,
    String(r.old_epoch),
    r.old_identity,
    r.old_signing,
    String(r.new_epoch),
    r.new_identity,
    r.new_signing,
    String(r.generation),
    r.publication_digest,
    r.reason,
  ];
  return utf8(fields.map((f) => utf8(f).length + ":" + f).join(""));
}
export function signReset(r, secret) {
  return { ...r, signature: toBase64(sign(resetTranscript(r), secret)) };
}
export function verifyReset(
  r,
  { server, principal, device, epoch, identity, signing, noxaSigning },
) {
  const sig = fromBase64(r.signature);
  if (
    r.server !== server ||
    r.principal !== principal ||
    r.device !== device ||
    r.old_epoch !== epoch ||
    r.old_identity !== identity ||
    r.old_signing !== signing ||
    !verifySignature(sig, resetTranscript(r), noxaSigning)
  )
    throw fail();
  return r;
}

export const MAX_RESET_CHAIN_PAGES = 64;

/**
 * Advances a cached peer pin {epoch, identity, signing} to target {epoch,
 * identity[, signing]} only through noxa-olm-reset-v1 transitions verified
 * (verifyReset) from the pin reached so far under the independently trusted
 * noXa key. fetch(after) returns one reset_chain page (old_epoch >= after,
 * any order). A gap, a foreign/altered transition or a chain ending elsewhere
 * throws: a server can never promote a pin (olmdir.VerifyResetChain).
 * Returns {pin, passed} (passed: the pins left behind, oldest first).
 */
export async function verifyResetChain(from, to, { server, principal, device, noxaSigning, fetch }) {
  if (!(to.epoch > from.epoch)) throw fail();
  let cur = { epoch: from.epoch, identity: from.identity, signing: from.signing };
  const passed = [];
  for (let pages = 0; cur.epoch < to.epoch; pages++) {
    if (pages >= MAX_RESET_CHAIN_PAGES) throw fail();
    const page = await fetch(cur.epoch);
    if (!Array.isArray(page)) throw fail();
    const byOld = new Map();
    for (const r of page) if (r && Number.isSafeInteger(r.old_epoch)) byOld.set(r.old_epoch, r);
    const start = cur.epoch;
    for (let r; cur.epoch < to.epoch && (r = byOld.get(cur.epoch)); ) {
      verifyReset(r, { server, principal, device, epoch: cur.epoch, identity: cur.identity, signing: cur.signing, noxaSigning });
      passed.push(cur);
      cur = { epoch: r.new_epoch, identity: r.new_identity, signing: r.new_signing };
    }
    if (cur.epoch === start) throw fail(); // gap
  }
  if (cur.epoch !== to.epoch || cur.identity !== to.identity || (to.signing && cur.signing !== to.signing)) throw fail();
  return { pin: cur, passed };
}
