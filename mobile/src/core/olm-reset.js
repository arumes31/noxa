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
