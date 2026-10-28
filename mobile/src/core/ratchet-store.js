import {
  openAttempt,
  validateManifest,
  digestBytes,
  attemptSequence,
  canonicalJSON,
  jsonBytes,
  equalJSON,
  checkEnvelope,
  expectedBinding,
} from "./olm-admission.js";
// Candidate storage only: no wire migration or transport lives in this module.
import {
  Stores,
  openDB,
  identityFence,
  fencedWrite,
  seal,
  unseal,
} from "./db.js";
// Mirror of durable.DevicePairScope: one Olm session per local account/device
// pair, reused by every direct and group conversation. The core authenticates
// each message's conversation binding. Per-conversation session records from
// before this rule fail validation/context equality and are never mixed.
export const DEVICE_PAIR_SCOPE = "device-pair";
/** Mirror of durable.SessionRef: an attempt packet's session record is "olm:" + core session ID. */
export const sessionRef = (coreSessionID) => "olm:" + coreSessionID;
export const Limits = Object.freeze({
  fanout: 160,
  reads: 512,
  record: 2 << 20,
  batch: 32 << 20,
  bytes: 128 << 20,
  records: 100000,
  otks: 100,
});
const enc = new TextEncoder(),
  dec = new TextDecoder("utf-8", { fatal: true });
const kinds = new Set([
  "account",
  "session",
  "trust",
  "outbox",
  "inbox",
  "publication",
  "claim",
  "obligation",
  "logical",
  "transition",
]);
const ctl = ["control", "identity"];
const key = (r) => JSON.stringify(r);
const base64 = (data) => {
  let s = "";
  for (let i = 0; i < data.length; i += 8192)
    s += String.fromCharCode(...data.subarray(i, i + 8192));
  return btoa(s);
};
const bytes = (x) => enc.encode(JSON.stringify(x));
const json = (x) => JSON.parse(dec.decode(x));
const validID = (x) =>
  typeof x === "string" &&
  x.length > 0 &&
  enc.encode(x).length <= 512 &&
  !/[\p{Cc}\p{Cf}]/u.test(x);
const validRef = (r) =>
  Array.isArray(r) && r.length === 2 && kinds.has(r[0]) && validID(r[1]);
const fail = (code) =>
  Object.assign(new Error(`ratchet store: ${code}`), { code });
const equal = (a, b) => a === b;
function validateRefs(refs) {
  if (refs.length > Limits.reads) throw fail("exhausted");
  if (!refs.every(validRef)) throw fail("invalid");
}
function version(n) {
  if (!Number.isSafeInteger(n) || n < 1) throw fail("corrupt");
  return n;
}
async function withDB(fn) {
  const db = await openDB();
  try {
    return await fn(db);
  } finally {
    db.close();
  }
}

export async function openRatchetStore(namespace) {
  if (
    !namespace ||
    !["server", "principal", "device"].every((k) => validID(namespace[k]))
  )
    throw fail("invalid");
  const store = new RatchetStore(
    JSON.stringify([namespace.server, namespace.principal, namespace.device]),
  );
  await store.initialize();
  return store;
}
class RatchetStore {
  constructor(namespace) {
    this.namespace = namespace;
  }
  dbkey(ref) {
    return [this.namespace, ...ref];
  }
  aad(ref, revision) {
    return bytes(["noxa-local-ratchet-v1", this.namespace, ...ref, revision]);
  }
  async decode(db, ref, raw) {
    if (!raw) return { revision: 0, data: new Uint8Array() };
    version(raw.revision);
    try {
      return {
        revision: raw.revision,
        data: await unseal(db, raw, this.aad(ref, raw.revision)),
      };
    } catch {
      throw fail("corrupt");
    }
  }
  async encode(db, ref, revision, data) {
    version(revision);
    return { ...(await seal(db, data, this.aad(ref, revision))), revision };
  }
  async raw(db, refs) {
    return new Promise((resolve, reject) => {
      const tx = db.transaction([Stores.device, Stores.ratchet], "readonly");
      const records = {};
      let fence, initialized;
      const f = tx.objectStore(Stores.device).get("identity-fence");
      f.onsuccess = () => {
        fence = f.result || { generation: 1, active: true };
      };
      const marker = tx
        .objectStore(Stores.device)
        .get("ratchet-initialized:" + this.namespace);
      marker.onsuccess = () => {
        initialized = marker.result;
      };
      for (const ref of refs) {
        const r = tx.objectStore(Stores.ratchet).get(this.dbkey(ref));
        r.onsuccess = () => {
          records[key(ref)] = r.result;
        };
      }
      tx.oncomplete = () => resolve({ fence, records, initialized });
      tx.onabort = tx.onerror = () => reject(tx.error || fail("aborted"));
    });
  }
  async initialize() {
    return withDB(async (db) => {
      const before = await this.raw(db, [ctl]);
      if (!before.fence.active) throw fail("fenced");
      if (before.records[key(ctl)]) {
        const c = json(
          (await this.decode(db, ctl, before.records[key(ctl)])).data,
        );
        if (
          c.coreKey?.length !== 32 ||
          !Number.isSafeInteger(c.count) ||
          c.count < 0 ||
          !Number.isSafeInteger(c.bytes) ||
          c.bytes < 0
        )
          throw fail("corrupt");
        return;
      }
      if (before.initialized === before.fence.generation) throw fail("corrupt");
      const count = await new Promise((resolve, reject) => {
        const tx = db.transaction(Stores.ratchet, "readonly"),
          r = tx
            .objectStore(Stores.ratchet)
            .count(IDBKeyRange.bound([this.namespace], [this.namespace, []]));
        tx.oncomplete = () => resolve(r.result);
        tx.onabort = tx.onerror = () => reject(tx.error);
      });
      if (count) {
        // Another tab may have won initialization while the count was queued.
        const latest = await this.raw(db, [ctl]);
        if (!latest.records[key(ctl)]) throw fail("corrupt");
        await this.decode(db, ctl, latest.records[key(ctl)]);
        return;
      }
      const data = bytes({
        coreKey: Array.from(crypto.getRandomValues(new Uint8Array(32))),
        count: 0,
        bytes: 0,
        obligations: 0,
        accountID: "local",
        accountEpoch: 1,
        sendingBlocked: false,
        nextAttempt: 0,
      });
      const candidate = await this.encode(db, ctl, 1, data);
      await fencedWrite(db, [Stores.ratchet], before.fence, (tx) => {
        const st = tx.objectStore(Stores.ratchet),
          r = st.get(this.dbkey(ctl));
        r.onsuccess = () => {
          if (!r.result) {
            st.add(candidate, this.dbkey(ctl));
            tx.objectStore(Stores.device).put(
              before.fence.generation,
              "ratchet-initialized:" + this.namespace,
            );
          }
        };
      });
    });
  }
  async read(refs = []) {
    validateRefs(refs);
    return withDB(async (db) => {
      const all = [ctl, ...refs],
        raw = await this.raw(db, all);
      if (!raw.fence.active) throw fail("fenced");
      const records = {};
      let total = 0;
      for (const ref of all) {
        const r = await this.decode(db, ref, raw.records[key(ref)]);
        records[key(ref)] = r;
        total += r.data.length;
        if (total > Limits.batch) throw fail("exhausted");
      }
      if (!records[key(ctl)].revision) throw fail("corrupt");
      return { namespace: this.namespace, fence: raw.fence, records };
    });
  }
  coreKey(snapshot) {
    this.validate(snapshot);
    const c = json(snapshot.records[key(ctl)].data);
    if (c.coreKey?.length !== 32) throw fail("corrupt");
    return new Uint8Array(c.coreKey);
  }
  validate(s) {
    if (!s || s.namespace !== this.namespace || !s.records?.[key(ctl)])
      throw fail("invalid");
    if (Object.keys(s.records).length > Limits.reads + 1)
      throw fail("exhausted");
  }
  require(s, r) {
    const v = s.records[key(r)];
    if (!v) throw fail("invalid");
    return v;
  }
  trust(s, id) {
    const r = this.require(s, ["trust", id]);
    if (!r.revision) throw fail("invalid");
    const t = json(r.data);
    if (t.revoked) throw fail("revoked");
    return r.revision;
  }
  async #commit(s, changes, authorityUpdate = null) {
    this.validate(s);
    return withDB(async (db) => {
      const c = json(s.records[key(ctl)].data);
      if (authorityUpdate) Object.assign(c, authorityUpdate);
      let total = 0;
      for (const [k, data] of Object.entries(changes)) {
        const ref = JSON.parse(k);
        if (!validRef(ref) || !(data instanceof Uint8Array) || !data.length)
          throw fail("invalid");
        if (data.length > (ref[0] === "outbox" ? 24 << 20 : Limits.record))
          throw fail("exhausted");
        total += data.length;
        const old = s.records[k];
        if (!old) throw fail("invalid");
        if (!old.revision) c.count++;
        c.bytes += data.length - old.data.length;
        if (ref[0] === "obligation") {
          const before = old.revision ? json(old.data) : null,
            after = json(data);
          if (!before && !after.consumed && !after.closed) c.obligations++;
          else if (
            before &&
            !before.consumed &&
            !before.closed &&
            (after.consumed || after.closed)
          )
            c.obligations--;
        }
      }
      if (
        total > Limits.batch ||
        c.count > Limits.records ||
        c.bytes > Limits.bytes ||
        c.obligations > Limits.otks
      )
        throw fail("exhausted");
      const prepared = {};
      for (const [k, data] of Object.entries(
        Object.keys(changes).length || authorityUpdate
          ? { ...changes, [key(ctl)]: bytes(c) }
          : {},
      )) {
        prepared[k] = await this.encode(
          db,
          JSON.parse(k),
          s.records[k].revision + 1,
          data,
        );
      }
      // No promises, crypto or external callbacks execute inside this tx.
      await new Promise((resolve, reject) => {
        const tx = db.transaction(
          [Stores.device, Stores.ratchet],
          "readwrite",
          { durability: "strict" },
        );
        let failure;
        const st = tx.objectStore(Stores.ratchet);
        let remaining = Object.keys(s.records).length + 1;
        const abort = (code) => {
          failure = fail(code);
          tx.abort();
        };
        const ready = () => {
          if (--remaining === 0)
            for (const [k, record] of Object.entries(prepared))
              st.put(record, this.dbkey(JSON.parse(k)));
        };
        const fr = tx.objectStore(Stores.device).get("identity-fence");
        fr.onsuccess = () => {
          const f = fr.result || { generation: 1, active: true };
          if (!f.active || f.generation !== s.fence.generation) {
            abort("fenced");
            return;
          }
          ready();
        };
        for (const [k, record] of Object.entries(s.records)) {
          const r = st.get(this.dbkey(JSON.parse(k)));
          r.onsuccess = () => {
            if (!equal(r.result?.revision || 0, record.revision)) {
              abort("conflict");
              return;
            }
            ready();
          };
        }
        tx.oncomplete = () => resolve();
        tx.onabort = tx.onerror = () =>
          reject(failure || tx.error || fail("aborted"));
      });
    });
  }
  async extended(s, refs) {
    this.validate(s);
    const unique = [
      ...new Map(
        [
          ...Object.keys(s.records)
            .map(JSON.parse)
            .filter((r) => r[0] !== "control"),
          ...refs,
        ].map((r) => [key(r), r]),
      ).values(),
    ];
    const fresh = await this.read(unique);
    if (fresh.fence.generation !== s.fence.generation) throw fail("fenced");
    return fresh;
  }
  // A read-only validation: re-read and validate (empty commit) as one
  // consistent snapshot. Writes elsewhere on the namespace (another tab or
  // consumer) only move revisions, so a lost validation is re-read and
  // re-validated here instead of failing the caller with "conflict".
  async validated(s, refs) {
    for (let attempt = 0; ; attempt++) {
      const fresh = await this.extended(s, refs);
      try {
        await this.#commit(fresh, {});
        return fresh;
      } catch (e) {
        if (e.code !== "conflict" || attempt >= 16) throw e;
      }
    }
  }
  compare(s, fresh) {
    for (const [k, v] of Object.entries(s.records))
      if (fresh.records[k]?.revision !== v.revision) throw fail("conflict");
  }
  // Mirror of Go SessionState.Pending: a first-contact outbound session keeps
  // {claim_id, claim_until} of its reserved claim until an attempt using it is
  // accepted (or the session receives). Every send on a pending session must
  // carry exactly that claim; an established session can never acquire one.
  sessionValue(s, ref, state, context, receiving = false, claim = null) {
    if (
      (claim &&
        (receiving ||
          !validID(claim.claim_id) ||
          !Number.isSafeInteger(claim.claim_until) ||
          claim.claim_until <= 0)) ||
      !state?.length ||
      !context ||
      !["peer_principal", "peer_device", "peer_identity"].every((k) =>
        validID(context[k]),
      ) ||
      context.scope !== DEVICE_PAIR_SCOPE ||
      Object.hasOwn(context, "conversation") ||
      context.protocol !== "noxa-olm-v1" ||
      !validID(context.local_account_id) ||
      !Number.isSafeInteger(context.local_epoch) ||
      context.local_epoch < 1 ||
      !Number.isSafeInteger(context.peer_epoch) ||
      context.peer_epoch < 1
    )
      throw fail("invalid");
    const old = this.require(s, ref),
      previous = old.revision ? json(old.data) : null;
    if (previous?.retired && !receiving) throw fail("conflict");
    if (
      previous &&
      JSON.stringify(previous.context) !== JSON.stringify(context)
    )
      throw fail("conflict");
    let pending = claim
      ? { claim_id: claim.claim_id, claim_until: claim.claim_until }
      : null;
    if (previous) {
      if (!receiving && previous.pending && !claim) throw fail("conflict");
      if (claim && (!previous.pending || !equalJSON(previous.pending, pending)))
        throw fail("conflict");
      pending = receiving ? null : previous.pending || null;
    }
    return bytes({
      snapshot: Array.from(state),
      context,
      created_at: previous?.created_at || Math.floor(Date.now() / 1000),
      retired: previous?.retired || false,
      ...(pending ? { pending } : {}),
    });
  }
  async setTrust(s, id, value, revoked = false) {
    const r = ["trust", id];
    const old = this.require(s, r);
    if (old.revision && json(old.data).revoked && !revoked)
      throw fail("revoked");
    await this.#commit(s, { [key(r)]: bytes({ value, revoked }) });
  }
  async commitSend(s, id, packets, archive, attempt = null) {
    if (attempt) await this.#validateAttempt(attempt, packets);
    if (!validID(id) || (!packets.length && !attempt) || !archive?.length)
      throw fail("invalid");
    if (packets.length > Limits.fanout) throw fail("exhausted");
    if (
      archive.length > Limits.record ||
      packets.some(
        (p) =>
          p.envelope?.length > Limits.record || p.state?.length > Limits.record,
      )
    )
      throw fail("exhausted");
    const r = ["outbox", id],
      fresh = await this.extended(s, [
        r,
        ...(attempt ? [["logical", attempt.manifest.logical_id]] : []),
      ]);
    const old = fresh.records[key(r)];
    if (old.revision) {
      const b = json(old.data);
      // An attempt ID is reused only for the SAME frozen attempt. A concurrent
      // writer that chose the same sequence for different content conflicts;
      // it must never inherit another operation's archive or receipt.
      if (
        !!attempt !== !!b.attempt ||
        (attempt &&
          (!equalJSON(b.attempt.manifest, attempt.manifest) ||
            !equalJSON(b.attempt.parts, attempt.parts)))
      )
        throw fail("conflict");
      this.sendAuthority(fresh, b.packets, b.attempt);
      for (const p of b.packets)
        if (this.trust(fresh, p.trustID) !== p.trustRevision)
          throw fail("conflict");
      await this.#commit(fresh, {});
      return { data: old.data, reused: true };
    }
    this.compare(s, fresh);
    this.sendAuthority(fresh, packets, attempt);
    const changes = {},
      saved = {
        status: "pending",
        packets: [],
        archive: Array.from(archive),
        ...(attempt ? { attempt } : {}),
      };
    let authorityUpdate = null;
    if (attempt) {
      const m = attempt.manifest,
        c = json(fresh.records[key(ctl)].data),
        seq = attemptSequence(m.attempt_id);
      if (seq !== c.nextAttempt + 1) throw fail("conflict");
      const lr = ["logical", m.logical_id],
        old = this.require(fresh, lr);
      if (
        old.revision &&
        !["rejected", "aborted"].includes(json(old.data).state)
      )
        throw fail("conflict");
      changes[key(lr)] = bytes({ attemptID: id, state: "pending" });
      authorityUpdate = { nextAttempt: seq };
    }
    for (const p of packets) {
      const ref = ["session", p.sessionID],
        k = key(ref);
      this.require(s, ref);
      if (changes[k] || !p.envelope?.length) throw fail("invalid");
      // A new outbound session in an attempt is always a first contact: it
      // must carry its reserved claim (becomes the pending claim).
      if (attempt && !s.records[k].revision && !p.claim) throw fail("invalid");
      const trustRevision = this.trust(s, p.trustID);
      changes[k] = this.sessionValue(s, ref, p.state, p.context, false, p.claim || null);
      if (p.replaces) {
        const oldRef = ["session", p.replaces],
          oldKey = key(oldRef),
          old = this.require(s, oldRef);
        if (
          !old.revision ||
          s.records[k].revision ||
          p.replaces === p.sessionID ||
          changes[oldKey]
        )
          throw fail("invalid");
        const previous = json(old.data);
        if (
          previous.retired ||
          JSON.stringify(previous.context) !== JSON.stringify(p.context)
        )
          throw fail("conflict");
        changes[oldKey] = bytes({ ...previous, retired: true });
      }
      saved.packets.push({
        sessionID: p.sessionID,
        trustID: p.trustID,
        trustRevision,
        envelope: base64(p.envelope),
        context: p.context,
      });
    }
    const result = bytes(saved);
    changes[key(r)] = result;
    await this.#commit(fresh, changes, authorityUpdate);
    return { data: result, reused: false };
  }
  async ack(s, id) {
    const r = ["outbox", id],
      old = this.require(s, r);
    if (!old.revision) throw fail("invalid");
    const b = json(old.data);
    if (b.attempt) throw fail("invalid");
    if (b.status === "sent") {
      await this.#commit(s, {});
      return;
    }
    b.status = "sent";
    await this.#commit(s, { [key(r)]: bytes(b) });
  }
  async commitReceive(s, id, p) {
    if (!!p.account?.length !== !!p.consumedOTK) throw fail("invalid");
    if (p.envelope?.length > Limits.record || p.archive?.length > Limits.record)
      throw fail("exhausted");
    if (!validID(id) || !p.envelope?.length || !p.archive?.length)
      throw fail("invalid");
    const r = ["inbox", id];
    const digest = Array.from(
      new Uint8Array(await crypto.subtle.digest("SHA-256", p.envelope)),
    )
      .map((x) => x.toString(16).padStart(2, "0"))
      .join("");
    const fresh = await this.extended(s, [r]);
    const old = fresh.records[key(r)];
    if (old.revision) {
      const rec = json(old.data);
      if (rec.digest !== digest) throw fail("conflict");
      await this.#commit(fresh, {});
      return new Uint8Array(rec.archive);
    }
    this.compare(s, fresh);
    this.trust(s, p.trustID);
    const sr = ["session", p.sessionID];
    this.require(s, sr);
    const changes = {
      [key(sr)]: this.sessionValue(s, sr, p.session, p.context, true),
      [key(r)]: bytes({ digest, archive: Array.from(p.archive) }),
    };
    if (p.account) {
      this.require(s, ["account", p.accountID || "local"]);
      changes[key(["account", p.accountID || "local"])] = p.account;
    }
    if (p.consumedOTK) {
      const ref = ["obligation", p.consumedOTK],
        v = this.require(s, ref);
      if (!v.revision || !p.account) throw fail("invalid");
      const obligation = json(v.data);
      if (
        obligation.account_id !== (p.accountID || "local") ||
        obligation.closed
      )
        throw fail("conflict");
      changes[key(ref)] = bytes({ ...obligation, consumed: true });
    }
    await this.#commit(fresh, changes);
    return p.archive.slice();
  }
  async commitPublication(
    s,
    id,
    account,
    signed,
    otks,
    { accountID = "local" } = {},
  ) {
    if (
      !validID(accountID) ||
      !validID(id) ||
      !account?.length ||
      !signed?.length
    )
      throw fail("invalid");
    const entries = Object.entries(otks);
    if (!entries.length || entries.length > Limits.otks)
      throw fail("exhausted");
    const r = ["publication", id],
      refs = [r, ...entries.map(([id]) => ["obligation", id])],
      fresh = await this.extended(s, refs);
    const authority = json(fresh.records[key(ctl)].data);
    if (authority.sendingBlocked || authority.accountID !== accountID)
      throw fail("fenced");
    const old = fresh.records[key(r)];
    if (old.revision) {
      await this.#commit(fresh, {});
      const saved = json(old.data);
      if (saved.account_id !== accountID) throw fail("conflict");
      return new Uint8Array(saved.payload);
    }
    this.compare(s, fresh);
    this.require(s, ["account", accountID]);
    const changes = {
      [key(["account", accountID])]: account,
      [key(r)]: bytes({ account_id: accountID, payload: Array.from(signed) }),
    };
    for (const [oid, o] of entries) {
      const ref = ["obligation", oid];
      if (
        fresh.records[key(ref)].revision ||
        o.publication_id !== id ||
        o.consumed ||
        o.closed
      )
        throw fail("conflict");
      if (o.account_id && o.account_id !== accountID) throw fail("invalid");
      changes[key(ref)] = bytes({
        ...o,
        account_id: accountID,
        account_epoch: authority.accountEpoch,
        key_id: o.key_id || oid,
      });
    }
    await this.#commit(fresh, changes);
    return signed.slice();
  }
  async reserveClaim(s, id, trustID, request) {
    if (!validID(id) || !validID(trustID) || !request?.length)
      throw fail("invalid");
    if (request.length > Limits.record) throw fail("exhausted");
    const r = ["claim", id],
      fresh = await this.extended(s, [r]);
    const authority = json(fresh.records[key(ctl)].data);
    if (authority.sendingBlocked) throw fail("fenced");
    const revision = this.trust(fresh, trustID),
      old = fresh.records[key(r)];
    if (old.revision) {
      const saved = json(old.data);
      if (
        saved.accountID !== authority.accountID ||
        saved.accountEpoch !== authority.accountEpoch
      )
        throw fail("fenced");
      if (saved.trustID !== trustID || saved.trustRevision !== revision)
        throw fail("conflict");
      await this.#commit(fresh, {});
      return new Uint8Array(saved.request);
    }
    this.compare(s, fresh);
    await this.#commit(fresh, {
      [key(r)]: bytes({
        accountID: authority.accountID,
        accountEpoch: authority.accountEpoch,
        trustID,
        trustRevision: revision,
        request: Array.from(request),
      }),
    });
    return request.slice();
  }
  // Re-validates a committed send for a retry. Only a real change (trust
  // revision moved, attempt no longer pending, authority fenced) fails with
  // "conflict"; a pure revision move by another tab or consumer re-reads and
  // re-validates (bounded, like validated()), so a healthy committed attempt
  // is never aborted because of an unrelated concurrent write. Exhausting the
  // bound is "local_conflict" (transient), never a trust mismatch.
  async retrySend(s, id) {
    for (let attempt = 0; ; attempt++) {
      const fresh = await this.extended(s, [["outbox", id]]);
      const old = fresh.records[key(["outbox", id])];
      if (!old.revision) throw fail("invalid");
      const saved = json(old.data);
      const checked = await this.extended(
        fresh,
        saved.packets.map((p) => ["trust", p.trustID]),
      );
      const moved = Object.entries(fresh.records).some(
        ([k, v]) => checked.records[k]?.revision !== v.revision,
      );
      if (!moved) {
        this.sendAuthority(checked, saved.packets, saved.attempt);
        if (saved.attempt && saved.status !== "pending")
          throw fail("conflict");
        for (const p of saved.packets)
          if (this.trust(checked, p.trustID) !== p.trustRevision)
            throw fail("conflict");
        try {
          await this.#commit(checked, {});
          return { data: old.data, reused: true };
        } catch (e) {
          if (e.code !== "conflict") throw e;
        }
      }
      if (attempt >= 16) throw fail("local_conflict");
      s = checked;
    }
  }

  sendAuthority(s, packets, attempt) {
    const c = json(this.require(s, ctl).data);
    if (
      c.sendingBlocked ||
      !c.accountID ||
      !Number.isSafeInteger(c.accountEpoch) ||
      c.accountEpoch < 1 ||
      (attempt && attempt.manifest.account_epoch !== c.accountEpoch)
    )
      throw fail("fenced");
    for (const p of packets)
      if (
        p.context?.local_account_id !== c.accountID ||
        p.context?.local_epoch !== c.accountEpoch
      )
        throw fail("fenced");
    return c;
  }
  async #validateAttempt(attempt, packets) {
    const m = attempt.manifest,
      [server, principal, device] = JSON.parse(this.namespace);
    const parts = attempt.parts.map((p) =>
      Uint8Array.from(atob(p), (c) => c.charCodeAt(0)),
    );
    const copies = await openAttempt(m, parts);
    if (
      m.server !== server ||
      m.sender !== principal ||
      m.device !== device ||
      copies.length !== packets.length
    )
      throw fail("invalid");
    const seen = new Set();
    for (const p of packets) {
      const c = copies.find((c) => c.device === p.context?.peer_device);
      if (
        !c ||
        seen.has(c.device) ||
        c.principal !== p.context.peer_principal ||
        c.account_epoch !== p.context.peer_epoch ||
        base64(jsonBytes(c.envelope)) !== base64(p.envelope)
      )
        throw fail("invalid");
      checkEnvelope(m, c);
      const target = m.targets.find((t) => t.device === c.device);
      if (
        p.context.peer_identity !== target.identity ||
        p.context.scope !== DEVICE_PAIR_SCOPE ||
        // The wire copy must name exactly this packet's session and claim, so an
        // accepted receipt establishes the session that was actually admitted.
        p.sessionID !== sessionRef(c.session_id) ||
        (c.claim_id || "") !== (p.claim?.claim_id || "")
      )
        throw fail("invalid");
      seen.add(c.device);
    }
  }
  async commitAttempt(s, manifest, parts, packets, archive) {
    const attempt = {
      manifest: structuredClone(manifest),
      parts: parts.map(base64),
    };
    return this.commitSend(
      s,
      manifest.account_epoch + ":" + manifest.attempt_id,
      packets,
      archive,
      attempt,
    );
  }
  async attemptStatusRequest(s, id) {
    const fresh = await this.validated(s, [["outbox", id]]),
      old = this.require(fresh, ["outbox", id]);
    if (!old.revision) throw fail("invalid");
    const b = json(old.data);
    if (!b.attempt) throw fail("invalid");
    const digest = await digestBytes(validateManifest(b.attempt.manifest));
    return {
      manifest: b.attempt.manifest,
      digest,
      ...(b.receipt ? { receipt: b.receipt } : {}),
    };
  }
  async applyAttemptResult(s, id, result) {
    if (!["accepted", "rejected", "aborted"].includes(result.state))
      throw fail("invalid");
    const ref = ["outbox", id],
      old = this.require(s, ref);
    if (!old.revision) throw fail("invalid");
    const b = json(old.data);
    if (!b.attempt) throw fail("invalid");
    const m = b.attempt.manifest;
    if (
      result.server !== m.server ||
      result.account_epoch !== m.account_epoch ||
      result.attempt_id !== m.attempt_id ||
      result.logical_id !== m.logical_id ||
      result.digest !== (await digestBytes(validateManifest(m))) ||
      (result.state === "accepted" && !result.message_id)
    )
      throw fail("conflict");
    if (b.receipt) {
      if (!equalJSON(b.receipt, result)) throw fail("conflict");
      await this.#commit(s, {});
      return;
    }
    const lr = ["logical", m.logical_id],
      logical = json(this.require(s, lr).data);
    if (logical.attemptID !== id) throw fail("conflict");
    b.receipt = result;
    b.status = result.state;
    logical.state = result.state;
    let snap = s;
    const changes = { [key(ref)]: bytes(b), [key(lr)]: bytes(logical) };
    if (result.state === "accepted") {
      // The server now holds these session rows: establish first-contact
      // sessions of this attempt (drop their pending claim) in the same commit.
      snap = await this.extended(
        s,
        b.packets.map((p) => ["session", p.sessionID]),
      );
      this.compare(s, snap);
      for (const p of b.packets) {
        const sr = ["session", p.sessionID],
          v = this.require(snap, sr);
        if (!v.revision) throw fail("corrupt");
        const st = json(v.data);
        if (!st.pending) continue;
        delete st.pending;
        changes[key(sr)] = bytes(st);
      }
    }
    await this.#commit(snap, changes);
  }
  async attemptRetirementRequest(s, id) {
    const result = await this.attemptStatusRequest(s, id);
    if (!result.receipt) throw fail("invalid");
    return {
      attempt_id: result.manifest.attempt_id,
      account_epoch: result.manifest.account_epoch,
      digest: result.receipt.digest,
    };
  }
  async attemptWire(s, id) {
    const result = await this.retrySend(s, id),
      b = json(result.data);
    if (!b.attempt || b.status !== "pending") throw fail("invalid");
    return {
      manifest: b.attempt.manifest,
      parts: b.attempt.parts.map((p) =>
        Uint8Array.from(atob(p), (c) => c.charCodeAt(0)),
      ),
    };
  }
  async accountAuthority(s) {
    const fresh = await this.validated(s, []),
      c = json(this.require(fresh, ctl).data);
    return {
      accountID: c.accountID,
      epoch: c.accountEpoch,
      fence: fresh.fence.generation,
      blocked: c.sendingBlocked,
      nextAttempt: c.nextAttempt,
      retired:
        c.retired?.epoch === c.accountEpoch
          ? { through: c.retired.through, above: c.retired.above.slice() }
          : { through: 0, above: [] },
    };
  }
  // Durable retire_receipt progress for the current account epoch: a
  // contiguous watermark plus sparse sequences above it (bounded), so a new
  // session never replays retirement of already retired attempts.
  async noteRetired(s, id) {
    const [epochText, seqText] = String(id).split(":");
    const epoch = Number(epochText),
      seq = attemptSequence(seqText);
    const fresh = await this.extended(s, [["outbox", id]]),
      c = json(this.require(fresh, ctl).data),
      old = this.require(fresh, ["outbox", id]);
    if (!old.revision || !json(old.data).receipt || epoch !== c.accountEpoch)
      throw fail("invalid");
    const r =
      c.retired?.epoch === epoch ? c.retired : { epoch, through: 0, above: [] };
    if (seq <= r.through || r.above.includes(seq)) {
      await this.#commit(fresh, {});
      return;
    }
    const above = [...r.above, seq].sort((a, b) => a - b);
    let through = r.through;
    while (above[0] === through + 1) {
      through++;
      above.shift();
    }
    if (above.length > 1024) throw fail("exhausted");
    await this.#commit(fresh, {}, { retired: { epoch, through, above } });
  }
  async checkAccountAuthority(s, token) {
    const c = await this.accountAuthority(s);
    if (
      c.fence !== token.fence ||
      c.accountID !== token.accountID ||
      c.epoch !== token.epoch ||
      c.blocked
    )
      throw fail("fenced");
  }
  async prepareAccountTransition(s, accountID, account, reset, bundle, otks) {
    const [server, principal, device] = JSON.parse(this.namespace);
    const { publicationDigest, resetTranscript } = await import(
      "./olm-reset.js"
    );
    resetTranscript(reset);
    if ((await publicationDigest(bundle)) !== reset.publication_digest)
      throw fail("invalid");
    if (
      !validID(accountID) ||
      !account?.length ||
      reset.server !== server ||
      reset.principal !== principal ||
      reset.device !== device ||
      reset.new_epoch !== reset.old_epoch + 1 ||
      reset.new_epoch !== bundle.account_epoch ||
      reset.generation !== bundle.generation ||
      reset.new_identity !== bundle.identity ||
      reset.new_signing !== bundle.signing ||
      Object.keys(otks).length !== bundle.one_time_keys.length
    )
      throw fail("invalid");
    const ref = ["transition", reset.transition_id],
      ar = ["account", accountID],
      pr = ["publication", reset.transition_id],
      fresh = await this.extended(s, [
        ref,
        ar,
        pr,
        ...Object.keys(otks).map((id) => ["obligation", id]),
      ]);
    this.compare(s, fresh);
    const expected = { account_id: accountID, reset, bundle },
      old = this.require(fresh, ref);
    if (old.revision) {
      const prior = json(old.data);
      delete prior.receipt;
      if (!equalJSON(prior, expected)) throw fail("conflict");
      await this.#commit(fresh, {});
      return;
    }
    const c = json(this.require(fresh, ctl).data);
    if (
      c.sendingBlocked ||
      c.accountEpoch !== reset.old_epoch ||
      c.accountID === accountID ||
      this.require(fresh, ar).revision
    )
      throw fail("fenced");
    const changes = {
        [key(ref)]: bytes(expected),
        [key(ar)]: account,
        [key(pr)]: bytes({
          account_id: accountID,
          payload: Array.from(jsonBytes(bundle)),
        }),
      },
      seen = new Set();
    for (const [id, o] of Object.entries(otks)) {
      const kr = ["obligation", id];
      if (
        this.require(fresh, kr).revision ||
        o.consumed ||
        o.closed ||
        o.account_id !== accountID ||
        o.account_epoch !== reset.new_epoch ||
        o.publication_id !== reset.transition_id ||
        seen.has(o.key_id) ||
        !bundle.one_time_keys.some(
          (k) => k.id === o.key_id && k.key === o.public_key,
        )
      )
        throw fail("conflict");
      seen.add(o.key_id);
      changes[key(kr)] = bytes(o);
    }
    await this.#commit(fresh, changes, { sendingBlocked: true });
  }
  async promoteAccount(s, id, receipt) {
    const ref = ["transition", id],
      old = this.require(s, ref);
    if (!old.revision) throw fail("invalid");
    const t = json(old.data),
      r = t.reset;
    const canonical = {
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
    };
    if (
      receipt.server !== r.server ||
      receipt.device !== r.device ||
      receipt.transition_id !== r.transition_id ||
      receipt.account_epoch !== r.new_epoch ||
      receipt.publication_digest !== r.publication_digest ||
      receipt.digest !== (await digestBytes(jsonBytes(canonical)))
    )
      throw fail("conflict");
    if (t.receipt) {
      if (!equalJSON(t.receipt, receipt)) throw fail("conflict");
      await this.#commit(s, {});
      return;
    }
    const c = json(this.require(s, ctl).data);
    if (c.accountEpoch !== r.old_epoch || !c.sendingBlocked)
      throw fail("fenced");
    t.receipt = receipt;
    await this.#commit(
      s,
      { [key(ref)]: bytes(t) },
      {
        accountID: t.account_id,
        accountEpoch: r.new_epoch,
        nextAttempt: 0,
        sendingBlocked: false,
      },
    );
  }
  async accountTransitionRequest(s, id) {
    const fresh = await this.extended(s, [["transition", id]]),
      old = this.require(fresh, ["transition", id]);
    if (!old.revision) throw fail("invalid");
    const out = json(old.data);
    await this.#commit(fresh, {});
    return out;
  }
  async retireUnsubmittedKeys(s, accountID, account, proofs) {
    if (
      !account?.length ||
      Object.keys(proofs).length < 1 ||
      Object.keys(proofs).length > 100
    )
      throw fail("invalid");
    const [server, , device] = JSON.parse(this.namespace),
      ar = ["account", accountID];
    this.require(s, ar);
    const changes = { [key(ar)]: account };
    for (const [id, p] of Object.entries(proofs)) {
      const ref = ["obligation", id],
        v = this.require(s, ref);
      if (!v.revision) throw fail("invalid");
      const o = json(v.data);
      if (
        p.state !== "closed" ||
        p.server !== server ||
        p.device !== device ||
        p.account_epoch !== o.account_epoch ||
        p.key_id !== o.key_id ||
        o.account_id !== accountID ||
        o.consumed ||
        o.closed ||
        !o.public_key ||
        !/^[a-f0-9]{64}$/.test(p.proof)
      )
        throw fail("conflict");
      if (
        p.proof !==
        (await digestBytes(
          jsonBytes({
            server: p.server,
            device: p.device,
            account_epoch: p.account_epoch,
            key_id: p.key_id,
            state: p.state,
            proof: "",
          }),
        ))
      )
        throw fail("conflict");
      changes[key(ref)] = bytes({ ...o, closed: true, closure_proof: p.proof });
    }
    await this.#commit(s, changes);
  }
  async retirePeerSessions(s, trustID, reset, ids) {
    if (
      ids.length < 1 ||
      ids.length > 160 ||
      reset.server !== JSON.parse(this.namespace)[0]
    )
      throw fail("invalid");
    this.trust(s, trustID);
    const changes = {};
    for (const id of ids) {
      const ref = ["session", id],
        v = this.require(s, ref);
      if (!v.revision) throw fail("invalid");
      const state = json(v.data),
        c = state.context;
      if (
        c.peer_principal !== reset.principal ||
        c.peer_device !== reset.device ||
        c.peer_epoch !== reset.old_epoch ||
        c.peer_identity !== reset.old_identity
      )
        throw fail("conflict");
      changes[key(ref)] = bytes({ ...state, retired: true });
    }
    await this.#commit(s, changes);
  }
  async pendingRefs(kind, after = "", limit = 100) {
    if (
      !kinds.has(kind) ||
      !Number.isInteger(limit) ||
      limit < 1 ||
      limit > 200
    )
      throw fail("invalid");
    return withDB(async (db) => {
      const fence = await identityFence(db);
      if (!fence.active) throw fail("fenced");
      return new Promise((resolve, reject) => {
        const tx = db.transaction(Stores.ratchet, "readonly"),
          refs = [];
        const r = tx
          .objectStore(Stores.ratchet)
          .openKeyCursor(
            IDBKeyRange.bound(
              [this.namespace, kind, after],
              [this.namespace, kind, []],
              !!after,
              true,
            ),
          );
        r.onsuccess = () => {
          if (r.result && refs.length < limit) {
            refs.push(r.result.key.slice(1));
            if (refs.length < limit) r.result.continue();
          }
        };
        tx.oncomplete = () => resolve(refs);
        tx.onabort = tx.onerror = () => reject(tx.error || fail("aborted"));
      });
    });
  }
}
