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
  } else if (m.purpose === "control") {
    // One explicitly targeted device, never a fanout (admission/control.go).
    if (m.reference || m.scope !== "direct" || m.targets.length !== 2) throw fail();
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
  if (m.purpose === "control" && !controlTarget(m)) throw fail();
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
  return openPayloadRaw(m, target, plaintext).body;
}

/** As openPayload, plus the exact body text (raw), like Go's json.RawMessage. */
export function openPayloadRaw(m, target, plaintext) {
  if (typeof plaintext !== "string" || enc.encode(plaintext).length > 65536) throw fail();
  const p = JSON.parse(plaintext);
  checkPayloadFields(plaintext);
  if (
    !equalJSON(p.context, payloadContext(m, target)) ||
    !Object.hasOwn(p, "body")
  )
    throw fail();
  const raw = rawMembers(plaintext).find(member => member.key === "body")?.raw;
  return { body: p.body, raw };
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
    purpose: m.purpose === "chat" ? "message" : m.purpose === "control" ? "media-key" : m.purpose,
  };
}

// --- Media controls (mirror of internal/e2ee/admission/control.go) ---------
export const MAX_CONTROL_TTL = 300;
export const MAX_CONTROL_DATA = 4096;
export const MAX_PENDING_CONTROLS = 64; // per sender device and target device
export const CONTROL_CLOCK_SKEW = 300;
const controlWord = (s) => typeof s === "string" && /^[a-z0-9._-]{1,64}$/.test(s);

/** The single non-local target of a control manifest, or null. */
export function controlTarget(m) {
  if (m?.purpose !== "control" || !Array.isArray(m.targets) || m.targets.length !== 2) return null;
  const t = m.targets.find((x) => !x.local);
  return t && t.principal === m.peer ? t : null;
}

// Raw-text control parsing with the Go semantics (internal/e2ee/admission
// control.go): the receiver validates the exact authenticated bytes, so a
// duplicate member, a non-integer number literal or escaped bytes are judged
// the same on both clients. Shared vectors: internal/e2ee/admission/testdata.
const WS = " \t\n\r";
function skipWS(s, i) { while (i < s.length && WS.includes(s[i])) i++; return i; }
function scanString(s, i) {
  if (s[i] !== '"') throw fail();
  for (i++; i < s.length; i++) {
    if (s[i] === "\\") { i++; continue; }
    if (s[i] === '"') return i + 1;
  }
  throw fail();
}
function scanValue(s, i) {
  i = skipWS(s, i);
  const c = s[i];
  if (c === '"') return scanString(s, i);
  if (c === "{" || c === "[") {
    const close = c === "{" ? "}" : "]";
    i = skipWS(s, i + 1);
    if (s[i] === close) return i + 1;
    for (;;) {
      if (c === "{") {
        i = skipWS(s, scanString(s, skipWS(s, i)));
        if (s[i] !== ":") throw fail();
        i++;
      }
      i = skipWS(s, scanValue(s, i));
      if (s[i] === ",") { i++; continue; }
      if (s[i] === close) return i + 1;
      throw fail();
    }
  }
  let j = i;
  while (j < s.length && !",}]".includes(s[j]) && !WS.includes(s[j])) j++;
  if (j === i) throw fail();
  return j;
}
/** The members of a JSON object text in order: [{key, raw}] (raw value text). */
function rawMembers(s) {
  let i = skipWS(s, 0);
  if (s[i] !== "{") throw fail();
  const out = [];
  i = skipWS(s, i + 1);
  if (s[i] === "}") i++;
  else for (;;) {
    const ks = skipWS(s, i);
    i = scanString(s, ks);
    const key = JSON.parse(s.slice(ks, i));
    i = skipWS(s, i);
    if (s[i] !== ":") throw fail();
    const vs = skipWS(s, i + 1);
    i = scanValue(s, vs);
    out.push({ key, raw: s.slice(vs, i) });
    i = skipWS(s, i);
    if (s[i] === ",") { i++; continue; }
    if (s[i] === "}") { i++; break; }
    throw fail();
  }
  if (skipWS(s, i) !== s.length) throw fail();
  return out;
}
/** Exactly the allowed (case-sensitive) member names, each at most once. */
function exactMembers(s, allowed) {
  const members = rawMembers(s), seen = new Set();
  for (const m of members) {
    if (!allowed.includes(m.key) || seen.has(m.key)) throw fail();
    seen.add(m.key);
  }
  return Object.fromEntries(members.map(m => [m.key, m.raw]));
}
/** UTF-8 size of a JSON text without insignificant whitespace (Go json.Compact). */
function compactSize(raw) {
  let n = 0, inString = false, escaped = false;
  for (const ch of raw) {
    if (inString) {
      if (escaped) escaped = false;
      else if (ch === "\\") escaped = true;
      else if (ch === '"') inString = false;
    } else if (WS.includes(ch)) continue;
    else if (ch === '"') inString = true;
    n += enc.encode(ch).length;
  }
  return n;
}
const INTEGER = /^-?(0|[1-9][0-9]*)$/;
function int64Literal(raw) {
  if (typeof raw !== "string" || !INTEGER.test(raw) || raw.replace("-", "").length > 19) throw fail();
  return Number(raw);
}

/**
 * Validates a control body's shape (not its expiry); returns the parsed body.
 * text: the exact JSON text (receive), or an object (send; serialized first).
 */
export function parseControlBody(text) {
  if (typeof text !== "string") text = JSON.stringify(text ?? null);
  if (enc.encode(text).length === 0 || enc.encode(text).length > MAX_CONTROL_DATA + 1024) throw fail();
  const top = exactMembers(text, ["kind", "scope", "epoch", "expires_at", "data"]);
  if (top.scope === undefined || top.kind === undefined || top.epoch === undefined || top.expires_at === undefined) throw fail();
  const scopeRaw = exactMembers(top.scope, ["type", "id"]);
  const kind = JSON.parse(top.kind), type = scopeRaw.type === undefined ? undefined : JSON.parse(scopeRaw.type), id = scopeRaw.id === undefined ? undefined : JSON.parse(scopeRaw.id);
  const epoch = int64Literal(top.epoch), expiresAt = int64Literal(top.expires_at);
  if (!controlWord(kind) || !controlWord(type) || !validText(id) || epoch < 0 || epoch > Number.MAX_SAFE_INTEGER || !safe(expiresAt)) throw fail();
  const body = { kind, scope: { type, id }, epoch: epoch === 0 ? 0 : epoch, expires_at: expiresAt };
  if (top.data !== undefined) {
    if (compactSize(top.data) > MAX_CONTROL_DATA) throw fail();
    body.data = JSON.parse(top.data);
  }
  return body;
}

/** A control about to be sealed: valid, unexpired, expiring within MAX_CONTROL_TTL. */
export function validateControlSend(body, now) {
  const b = parseControlBody(body);
  if (b.expires_at <= now || b.expires_at > now + MAX_CONTROL_TTL) throw fail();
  return b;
}

/**
 * Judges a received control ONCE, on its exact authenticated bytes (Go
 * parity): {status: "delivered"|"expired"|"invalid", body}. body is the parsed
 * control (null when invalid); it is handed to the handlers and the archive as
 * is, never re-serialized and re-checked (JSON.stringify can lengthen numbers,
 * so a re-check could reject a body the raw bound accepted). An expiry further
 * ahead than a sender may set, plus clock skew, is invalid.
 */
export function judgeControl(raw, now) {
  let b;
  try { b = parseControlBody(raw); } catch { return { status: "invalid", body: null }; }
  if (b.expires_at > now + MAX_CONTROL_TTL + CONTROL_CLOCK_SKEW) return { status: "invalid", body: null };
  return { status: b.expires_at <= now ? "expired" : "delivered", body: b };
}

/** "delivered", "expired" or "invalid" for a received control body. */
export function controlDisposition(body, now) {
  return judgeControl(body, now).status;
}

/** What is kept at rest for an already parsed control (judgeControl /
 * validateControlSend): never its data (future media keys). */
export function controlArchiveBody(b) {
  if (!b || typeof b !== "object" || !b.scope || typeof b.scope !== "object") return null;
  return { kind: b.kind, scope: { type: b.scope.type, id: b.scope.id }, epoch: b.epoch, expires_at: b.expires_at };
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
