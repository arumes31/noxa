// Candidate storage only: no wire migration or transport lives in this module.
import {
  Stores,
  openDB,
  identityFence,
  fencedWrite,
  seal,
  unseal,
} from "./db.js";
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
  async #commit(s, changes) {
    this.validate(s);
    return withDB(async (db) => {
      const c = json(s.records[key(ctl)].data);
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
          if (!before && !after.consumed) c.obligations++;
          else if (before && !before.consumed && after.consumed)
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
        Object.keys(changes).length ? { ...changes, [key(ctl)]: bytes(c) } : {},
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
  compare(s, fresh) {
    for (const [k, v] of Object.entries(s.records))
      if (fresh.records[k]?.revision !== v.revision) throw fail("conflict");
  }
  sessionValue(s, ref, state, context, receiving = false) {
    if (
      !state?.length ||
      !context ||
      !["peer_principal", "peer_device", "peer_identity", "conversation"].every(
        (k) => validID(context[k]),
      ) ||
      context.protocol !== "noxa-olm-v1"
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
    return bytes({
      snapshot: Array.from(state),
      context,
      created_at: previous?.created_at || Math.floor(Date.now() / 1000),
      retired: previous?.retired || false,
    });
  }
  async setTrust(s, id, value, revoked = false) {
    const r = ["trust", id];
    const old = this.require(s, r);
    if (old.revision && json(old.data).revoked && !revoked)
      throw fail("revoked");
    await this.#commit(s, { [key(r)]: bytes({ value, revoked }) });
  }
  async commitSend(s, id, packets, archive) {
    if (!validID(id) || !packets.length || !archive?.length)
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
      fresh = await this.extended(s, [r]);
    const old = fresh.records[key(r)];
    if (old.revision) {
      const b = json(old.data);
      for (const p of b.packets)
        if (this.trust(fresh, p.trustID) !== p.trustRevision)
          throw fail("conflict");
      await this.#commit(fresh, {});
      return { data: old.data, reused: true };
    }
    this.compare(s, fresh);
    const changes = {},
      saved = { status: "pending", packets: [], archive: Array.from(archive) };
    for (const p of packets) {
      const ref = ["session", p.sessionID],
        k = key(ref);
      this.require(s, ref);
      if (changes[k] || !p.envelope?.length) throw fail("invalid");
      const trustRevision = this.trust(s, p.trustID);
      changes[k] = this.sessionValue(s, ref, p.state, p.context);
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
      });
    }
    const result = bytes(saved);
    changes[key(r)] = result;
    await this.#commit(fresh, changes);
    return { data: result, reused: false };
  }
  async ack(s, id) {
    const r = ["outbox", id],
      old = this.require(s, r);
    if (!old.revision) throw fail("invalid");
    const b = json(old.data);
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
      if (obligation.account_id !== (p.accountID || "local"))
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
        o.consumed
      )
        throw fail("conflict");
      if (o.account_id && o.account_id !== accountID) throw fail("invalid");
      changes[key(ref)] = bytes({ ...o, account_id: accountID });
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
    const revision = this.trust(fresh, trustID),
      old = fresh.records[key(r)];
    if (old.revision) {
      const saved = json(old.data);
      if (saved.trustID !== trustID || saved.trustRevision !== revision)
        throw fail("conflict");
      await this.#commit(fresh, {});
      return new Uint8Array(saved.request);
    }
    this.compare(s, fresh);
    await this.#commit(fresh, {
      [key(r)]: bytes({
        trustID,
        trustRevision: revision,
        request: Array.from(request),
      }),
    });
    return request.slice();
  }
  async retrySend(s, id) {
    let fresh = await this.extended(s, [["outbox", id]]);
    const old = fresh.records[key(["outbox", id])];
    if (!old.revision) throw fail("invalid");
    const saved = json(old.data);
    const checked = await this.extended(
      fresh,
      saved.packets.map((p) => ["trust", p.trustID]),
    );
    this.compare(fresh, checked);
    for (const p of saved.packets)
      if (this.trust(checked, p.trustID) !== p.trustRevision)
        throw fail("conflict");
    await this.#commit(checked, {});
    return { data: old.data, reused: true };
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
