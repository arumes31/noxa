// Final inactive admission contract. This module never opens a socket.
const validText = (s) =>
  typeof s === "string" &&
  s.length > 0 &&
  new TextEncoder().encode(s).length <= 128 &&
  !/[\p{Cc}]/u.test(s);
export const ADMISSION_VERSION = "noxa-olm-admission-v1";
const enc = new TextEncoder(),
  dec = new TextDecoder("utf-8", { fatal: true });
const fail = () =>
  Object.assign(new Error("invalid Olm admission"), { code: "invalid" });
export const canonicalJSON = (value) =>
  JSON.stringify(value).replace(
    /[<>&\u2028\u2029]/g,
    (c) => "\\u" + c.charCodeAt(0).toString(16).padStart(4, "0"),
  );
export const equalJSON = (a, b) => {
  const sort = (v) =>
    Array.isArray(v)
      ? v.map(sort)
      : v && typeof v === "object"
        ? Object.fromEntries(
            Object.keys(v)
              .sort()
              .map((k) => [k, sort(v[k])]),
          )
        : v;
  return canonicalJSON(sort(a)) === canonicalJSON(sort(b));
};
export const jsonBytes = (value) => enc.encode(canonicalJSON(value));
export async function digestBytes(data) {
  return Array.from(
    new Uint8Array(await crypto.subtle.digest("SHA-256", data)),
    (b) => b.toString(16).padStart(2, "0"),
  ).join("");
}
export function attemptSequence(id) {
  const n = Number(id);
  if (!Number.isSafeInteger(n) || n < 1 || String(n) !== id) throw fail();
  return n;
}
const safe = (n) => Number.isSafeInteger(n) && n > 0;
const hash = (s) => typeof s === "string" && /^[a-f0-9]{64}$/.test(s);
export function canonicalManifest(m) {
  const v = {
    version: m.version,
    server: m.server,
    sender: m.sender,
    device: m.device,
    account_epoch: m.account_epoch,
    attempt_id: m.attempt_id,
    logical_id: m.logical_id,
    purpose: m.purpose,
  };
  if (m.reference) v.reference = m.reference;
  v.scope = m.scope;
  if (m.peer) v.peer = m.peer;
  if (m.conversation) v.conversation = m.conversation;
  if (m.revision) v.revision = m.revision;
  if (m.epoch) v.epoch = m.epoch;
  v.deadline = m.deadline;
  v.rosters = m.rosters.map((r) => ({
    principal: r.principal,
    version: r.version,
  }));
  v.targets = m.targets.map((t) => ({
    ...{
      principal: t.principal,
      device: t.device,
      account_epoch: t.account_epoch,
      identity: t.identity,
    },
    ...(t.local ? { local: true } : {}),
  }));
  v.parts = m.parts.map((p) => ({ digest: p.digest, length: p.length }));
  return v;
}
export function validateManifest(m) {
  if (
    !m ||
    m.version !== ADMISSION_VERSION ||
    !/^tls-sha256:[a-f0-9]{64}$/.test(m.server) ||
    ![m.sender, m.device, m.attempt_id, m.logical_id].every(validText) ||
    !safe(m.account_epoch) ||
    !safe(m.deadline) ||
    !Array.isArray(m.rosters) ||
    m.rosters.length < 1 ||
    m.rosters.length > 16 ||
    !Array.isArray(m.targets) ||
    m.targets.length < 1 ||
    m.targets.length > 160 ||
    !Array.isArray(m.parts) ||
    m.parts.length < 1 ||
    m.parts.length > 8
  )
    throw fail();
  attemptSequence(m.attempt_id);
  if (m.purpose === "chat") {
    if (m.reference) throw fail();
  } else if (!["edit", "delete"].includes(m.purpose) || !validText(m.reference))
    throw fail();
  if (m.scope === "direct") {
    if (
      !validText(m.peer) ||
      m.conversation ||
      m.revision ||
      m.epoch ||
      m.rosters.length > 2
    )
      throw fail();
  } else if (
    m.scope !== "group" ||
    !validText(m.conversation) ||
    m.peer ||
    !safe(m.revision) ||
    !safe(m.epoch)
  )
    throw fail();
  const rs = new Set();
  for (const r of m.rosters) {
    if (!validText(r.principal) || !safe(r.version) || rs.has(r.principal))
      throw fail();
    rs.add(r.principal);
  }
  if (!rs.has(m.sender) || (m.scope === "direct" && !rs.has(m.peer)))
    throw fail();
  const seen = new Set();
  let local = 0;
  for (const t of m.targets) {
    if (
      !rs.has(t.principal) ||
      !validText(t.device) ||
      !validText(t.identity) ||
      !safe(t.account_epoch) ||
      seen.has(t.device)
    )
      throw fail();
    seen.add(t.device);
    if (t.local) {
      local++;
      if (
        t.principal !== m.sender ||
        t.device !== m.device ||
        t.account_epoch !== m.account_epoch
      )
        throw fail();
    }
  }
  if (local !== 1) throw fail();
  let total = 0;
  for (const p of m.parts) {
    if (
      !hash(p.digest) ||
      !Number.isSafeInteger(p.length) ||
      p.length < 2 ||
      p.length > 524288
    )
      throw fail();
    total += p.length;
  }
  const b = jsonBytes(canonicalManifest(m));
  if (b.length > 131072 || b.length + total > 4194304) throw fail();
  return b;
}
export async function freezeAttempt(manifest, copies) {
  if (!Array.isArray(copies) || copies.length > 159) throw fail();
  const parts = [];
  let batch = [];
  for (const c of copies) {
    if (jsonBytes(c.envelope).length > 24576) throw fail();
    const next = [...batch, c],
      b = jsonBytes(next);
    if (b.length > 524288) {
      parts.push(jsonBytes(batch));
      batch = [c];
    } else batch = next;
  }
  parts.push(jsonBytes(batch));
  const m = structuredClone(manifest);
  m.parts = [];
  for (const p of parts)
    m.parts.push({ digest: await digestBytes(p), length: p.length });
  await openAttempt(m, parts);
  return { manifest: canonicalManifest(m), parts };
}
export async function openAttempt(m, parts) {
  validateManifest(m);
  if (parts.length !== m.parts.length) throw fail();
  const targets = new Map(
      m.targets.filter((t) => !t.local).map((t) => [t.device, t]),
    ),
    copies = [];
  for (let i = 0; i < parts.length; i++) {
    const p = parts[i];
    if (
      p.length !== m.parts[i].length ||
      (await digestBytes(p)) !== m.parts[i].digest
    )
      throw fail();
    const batch = JSON.parse(dec.decode(p));
    if (!Array.isArray(batch)) throw fail();
    for (const c of batch) {
      const t = targets.get(c.device);
      if (
        !t ||
        t.principal !== c.principal ||
        t.account_epoch !== c.account_epoch ||
        !validText(c.session_id) ||
        (c.claim_id && !validText(c.claim_id)) ||
        !c.envelope ||
        jsonBytes(c.envelope).length > 24576
      )
        throw fail();
      targets.delete(c.device);
      copies.push(c);
    }
  }
  if (targets.size) throw fail();
  return copies;
}
export function payloadContext(m, t) {
  return {
    version: ADMISSION_VERSION,
    server: m.server,
    sender: m.sender,
    device: m.device,
    account_epoch: m.account_epoch,
    attempt_id: m.attempt_id,
    logical_id: m.logical_id,
    purpose: m.purpose,
    reference: m.reference || "",
    scope: m.scope,
    peer: m.peer || "",
    conversation: m.conversation || "",
    revision: m.revision || 0,
    epoch: m.epoch || 0,
    target_principal: t.principal,
    target_device: t.device,
    target_epoch: t.account_epoch,
  };
}
export function sealPayload(m, target, body) {
  const plaintext = canonicalJSON({ context: payloadContext(m, target), body });
  if (body === undefined || enc.encode(plaintext).length > 65536) throw fail();
  return plaintext;
}
// JSON.parse validates syntax; this token walk preserves duplicate-field evidence
// at the outer/context boundary without constraining opaque application JSON.
function checkPayloadFields(plaintext) {
  const tokens = plaintext.match(/"(?:\\.|[^"\\])*"|[{}\[\],:]|[^\s{}\[\],:]+/g);
  let i = 0;
  const walk = (strict, outer = false) => {
    const token = tokens[i++];
    if (!strict) {
      if (token === "{" || token === "[") {
        let depth = 1;
        while (depth > 0) {
          const next = tokens[i++];
          if (next === "{" || next === "[") depth++;
          if (next === "}" || next === "]") depth--;
        }
      }
      return;
    }
    if (token === "{") {
      const seen = new Set();
      while (tokens[i] !== "}") {
        const key = JSON.parse(tokens[i++]); i++; // colon
        if (strict && (seen.has(key) || (outer && key !== "context" && key !== "body"))) throw fail();
        seen.add(key);
        walk(outer ? key === "context" : strict);
        if (tokens[i] !== ",") break;
        i++;
      }
      i++;
    } else if (token === "[") {
      while (tokens[i] !== "]") {
        walk(strict);
        if (tokens[i] !== ",") break;
        i++;
      }
      i++;
    }
  };
  walk(true, true);
}
export function openPayload(m, target, plaintext) {
  if (typeof plaintext !== "string" || enc.encode(plaintext).length > 65536) throw fail();
  const p = JSON.parse(plaintext);
  checkPayloadFields(plaintext);
  if (
    !equalJSON(p.context, payloadContext(m, target)) ||
    !Object.hasOwn(p, "body")
  )
    throw fail();
  return p.body;
}

export function expectedBinding(m, t) {
  return {
    server: m.server,
    conversation:
      m.scope === "direct"
        ? "direct:" + canonicalJSON([m.sender, m.peer].sort((a, b) => {
            const x = enc.encode(a), y = enc.encode(b);
            for (let i = 0; i < Math.min(x.length, y.length); i++) {
              if (x[i] !== y[i]) return x[i] - y[i];
            }
            return x.length - y.length;
          }))
        : "group:" + m.conversation,
    sender: m.sender,
    sender_device: m.device,
    recipient: t.principal,
    recipient_device: t.device,
    message_id: m.logical_id,
    purpose: m.purpose === "chat" ? "message" : m.purpose,
  };
}
export function checkEnvelope(m, c) {
  const e = c.envelope;
  if (
    e.version !== "noxa-olm-v1" ||
    e.session_id !== c.session_id ||
    !equalJSON(e.binding, expectedBinding(m, c)) ||
    !e.message
  )
    throw fail();
}

// Reset receipt digest (store.ResetOlmAccount / durable PromoteAccount).
const canonicalReset = (r) => ({
  version: r.version,
  server: r.server,
  principal: r.principal,
  device: r.device,
  transition_id: r.transition_id,
  old_epoch: r.old_epoch,
  old_identity: r.old_identity,
  old_signing: r.old_signing,
  new_epoch: r.new_epoch,
  new_identity: r.new_identity,
  new_signing: r.new_signing,
  generation: r.generation,
  publication_digest: r.publication_digest,
  reason: r.reason,
  signature: r.signature,
});
export const resetDigest = (r) => digestBytes(jsonBytes(canonicalReset(r)));

/**
 * The receipt the server returns for reset r. Every field derives from the
 * recorded reset (no server secret), so the server holding r's exact
 * publication at the new epoch proves the same as the receipt.
 */
export async function recoveryReceipt(r) {
  return {
    server: r.server,
    device: r.device,
    transition_id: r.transition_id,
    account_epoch: r.new_epoch,
    digest: await resetDigest(r),
    publication_digest: r.publication_digest,
  };
}
