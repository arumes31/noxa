// Worker-model harness adopted from the review 2b fix5 repros: a serial op
// queue, per-pid active epoch and retireOne that clears the active key
// (frames drop), around the real MediaKeyEngine on a virtual clock.
// Virtual-clock harness with a WORKER MODEL (serial op queue, per-pid active epoch,
// retireOne clears the active key -> frames drop), around the real MediaKeyEngine.
const SRC = new URL("../src/media-keys.js", import.meta.url).href;
const mk = await import(SRC);
export const { MediaKeyEngine, REMOVAL_BOUND_MS } = mk;
const SLOTS = ["mic", "cam", "screen", "screenaudio"];
let worldN = 0;
export function world({ members = 20, seed = 1 } = {}) {
  let rs = seed >>> 0 || 1; const rnd = () => { rs ^= rs << 13; rs >>>= 0; rs ^= rs >>> 17; rs ^= rs << 5; rs >>>= 0; return rs / 4294967296; };
  const me = `u-me-${++worldN}`; let seq = 0; const created = new Map();
  let mono = 1_900_000_000_000; const queue = [];
  const timers = {
    setTimeout(fn, ms) { const t = { at: mono + Math.max(0, ms | 0), fn }; if (process.env.TDBG && ms === 2200) { console.log("arm2200", mono % 1e6, new Error().stack.split(String.fromCharCode(10))[2].trim()); const f = fn; t.fn = () => { console.log("fire2200", mono % 1e6); f(); }; } queue.push(t); return t; },
    clearTimeout(t) { const i = queue.indexOf(t); if (i >= 0) queue.splice(i, 1); },
    setInterval(fn, ms) { const t = { at: mono + ms, fn: () => { t.at = mono + ms; queue.push(t); fn(); } }; queue.push(t); return t; },
    clearInterval(t) { const i = queue.indexOf(t); if (i >= 0) queue.splice(i, 1); },
  };
  const tick = () => new Promise(r => setImmediate(r));
  const settle = async () => { for (let i = 0; i < 12; i++) await tick(); await new Promise(r => setTimeout(r, 1)); for (let i = 0; i < 12; i++) await tick(); };
  const advance = async total => { const end = mono + total;
    for (;;) { await settle(); queue.sort((a, b) => a.at - b.at); const next = queue[0];
      if (!next || next.at > end) { mono = end; await settle(); return; } queue.shift(); mono = Math.max(mono, next.at); next.fn(); } };
  const sleep = ms => new Promise(r => timers.setTimeout(r, ms));
  // ---- worker model ----
  const pids = new Map(); const timeline = []; // {t, pid, active}
  const st = pid => { if (!pids.has(pid)) pids.set(pid, { epochs: new Set(), active: 0 }); return pids.get(pid); };
  const setActive = (pid, e) => { const s = st(pid); if (s.active !== e) { s.active = e; if (pid.startsWith("self|")) timeline.push({ t: mono, pid, active: e }); } };
  let chain = Promise.resolve();
  let fault = () => null; // (op, pid, epoch) => {hold, rejectBefore, rejectAfter}
  let depth = 0; const depthTl = [[0,0]];
  const serial = (op, pid, epoch, apply) => { depth++; depthTl.push([mono, depth]); const dec = () => { depth--; depthTl.push([mono, depth]); }; if (process.env.BYPASS && op.startsWith("retire")) { try { return Promise.resolve(apply()); } catch (e) { return Promise.reject(e); } } const run = chain.then(async () => {
      const f = fault(op, pid, epoch) || {};
      if (f.holdBefore) await sleep(f.holdBefore);
      if (f.rejectBefore) throw new Error(`${op} rejected`);
      const v = apply();
      if (f.hold) await sleep(f.hold);
      if (f.rejectAfter) throw new Error(`${op} timed out`);
      return v; });
    run.then(dec, dec); chain = run.catch(() => {}); return run; };
  const opsLog = [];
  const media = {
    setKey(pid, e) { if (pid === "self|mic" && !created.has(e)) created.set(e, ++seq); return serial("setKey", pid, e, () => { st(pid).epochs.add(e); }); },
    activate(pid, e) { opsLog.push([mono, "activate", pid, e]); return serial("activate", pid, e, () => { if (!st(pid).epochs.has(e)) throw new Error("unknown media epoch"); setActive(pid, e); }); },
    retireOne(pid, e) { opsLog.push([mono, "retireOne", pid, e]); return serial("retireOne", pid, e, () => { const s = st(pid); s.epochs.delete(e); if (s.active === e) setActive(pid, 0); }); },
    retire(pid, through) { opsLog.push([mono, "retire", pid, through]); return serial("retire", pid, through, () => { const s = st(pid); for (const e of [...s.epochs]) if (e <= through) s.epochs.delete(e); if (s.active && s.active <= through) setActive(pid, 0); }); },
    forget(pid) { return serial("forget", pid, 0, () => { pids.delete(pid); }); },
  };
  const sends = []; let sendDelay = () => 0;
  const engineOpts = { media, timers, now: () => mono, sendTimeoutMs: 20_000,
    send: c => { const s = { seq: ++seq, at: mono, device: c.device, epoch: c.body.epoch }; sends.push(s);
      const d = sendDelay(c.device, c.body.epoch); if (d === Infinity) return new Promise(() => {}); if (!d) return Promise.resolve({ ok: true });
      return new Promise(r => timers.setTimeout(() => r({ ok: true }), d)); } };
  const e = new MediaKeyEngine(engineOpts);
  const m = i => ({ client_id: `c${i}`, unique_id: `u${i}`, media_device: `d${i}`, channel_id: 5 });
  let roster = [{ client_id: "me", unique_id: me, media_device: "d-me", channel_id: 5 }, ...Array.from({ length: members }, (_, i) => m(i))];
  const intervals = [];
  const W = { depthTl, e, media, created, sends, timeline, opsLog, pids, timers, advance, settle, rnd, intervals,
    get mono() { return mono; },
    set fault(f) { fault = f; }, set sendDelay(f) { sendDelay = f; },
    inRoster: i => roster.some(r => r.client_id === `c${i}`),
    async start() { e.setRoster(roster); e.start({ channelID: 5, clientID: "me", principal: me, device: "d-me" }); await advance(10_000); },
    join(i) { if (W.inRoster(i)) return; roster = [...roster, m(i)]; const iv = intervals.findLast(v => v.device === `d${i}` && v.rejoinedAt == null); if (iv) { iv.rejoinedAt = mono; iv.jseq = ++seq; } e.setRoster(roster); },
    remove(i) { if (!W.inRoster(i)) return; intervals.push({ device: `d${i}`, removedAt: mono, rseq: ++seq, rejoinedAt: null, jseq: Infinity }); roster = roster.filter(r => r.client_id !== `c${i}`); e.setRoster(roster); },
    encoding(slot = "mic") { return st(`self|${slot}`).active; },
  };
  return W;
}
// Confidentiality: per removal interval, no sender slot's ACTIVE worker epoch is one the device
// received, at any time in (tD + bound, rejoin). Device never receives an epoch created after tD.
export function check(W, { bound = REMOVAL_BOUND_MS, slack = 1 } = {}) {
  const v = []; const end0 = W.mono;
  for (const iv of W.intervals) {
    const end = iv.rejoinedAt ?? Infinity;
    const cr = ep => W.created.get(ep) ?? 0;
    const bad = W.sends.filter(s => s.device === iv.device && cr(s.epoch) > iv.rseq && (s.seq < iv.jseq || cr(s.epoch) < iv.jseq));
    if (bad.length) v.push(`${iv.device}@+${iv.removedAt % 1e6}: received ${bad.length} epoch(s) created after its removal`);
    if (end - iv.removedAt <= bound) continue;
    const held = new Set(W.sends.filter(s => s.device === iv.device && s.at < end).map(s => s.epoch));
    const from = iv.removedAt + bound + slack;
    for (const slot of SLOTS) {
      const pid = `self|${slot}`; const tl = W.timeline.filter(x => x.pid === pid);
      let cur = 0; let exposedFrom = null; let worst = 0;
      const segs = [];
      for (const x of tl) { segs.push([x.t, x.active]); }
      // walk segments
      let prevT = -Infinity; cur = 0;
      const pts = [...segs, [Math.min(end, end0), null]];
      for (const [t, a] of pts) {
        const s0 = Math.max(prevT, from), s1 = Math.min(t, end, end0);
        if (cur && held.has(cur) && s1 > s0) { worst = Math.max(worst, s1 - from); exposedFrom ??= s0; }
        prevT = t; if (a !== null) cur = a;
      }
      if (exposedFrom !== null) v.push(`${iv.device}: ${slot} encrypts under an epoch it holds after tD+${bound}: from +${exposedFrom - iv.removedAt} ms, until +${worst + bound + slack} ms${worst + from >= Math.min(end, end0) ? " (still at end)" : ""}`);
    }
  }
  return v;
}
export function availability(W) {
  const v = []; const st = W.e.status(); const ep = W.encoding("mic");
  if (!ep) v.push("availability: mic has no active worker epoch at end");
  if (st.state === "paused") v.push("availability: status paused at end");
  return v;
}
// Engine-level check for a SERIAL worker: an exposure counts only while the engine has not yet
// POSTED a retirement of that (pid, epoch); a posted retire stuck behind a busy worker queue is
// worker latency, not an engine fault.
export function checkPosted(W, { bound = REMOVAL_BOUND_MS, slack = 1 } = {}) {
  const v = []; const end0 = W.mono;
  const retirePost = (pid, e) => { let t = Infinity; for (const [at, op, p, x] of W.opsLog) if (p === pid && ((op === "retireOne" && x === e) || (op === "retire" && x >= e))) { t = Math.min(t, at); } return t; };
  for (const iv of W.intervals) {
    const end = Math.min(iv.rejoinedAt ?? Infinity, end0);
    if ((iv.rejoinedAt ?? Infinity) - iv.removedAt <= bound) continue;
    const held = new Set(W.sends.filter(s => s.device === iv.device && s.at < end).map(s => s.epoch));
    const from = iv.removedAt + bound + slack;
    for (const slot of SLOTS) {
      const pid = `self|${slot}`; const tl = W.timeline.filter(x => x.pid === pid);
      for (let k = 0; k < tl.length; k++) { const e = tl[k].active; if (!e || !held.has(e)) continue;
        const s = tl[k].t, sEnd = k + 1 < tl.length ? tl[k + 1].t : end0;
        const a = Math.max(s, from), b = Math.min(sEnd, end, retirePost(pid, e));
        // idle sub-intervals of [a,b): worker queue empty (a retire posted then would apply at once)
        let idle = 0, firstIdle = null; if (b > a) { const d = W.depthTl; for (let q = 0; q < d.length; q++) { if (d[q][1] !== 0) continue; const qs = Math.max(d[q][0], a), qe = Math.min(q + 1 < d.length ? d[q + 1][0] : Infinity, b); if (qe > qs) { idle += qe - qs; firstIdle ??= qs; } } }
        if (process.env.IDLE ? idle > 0 : b > a) { v.push(`${iv.device}: ${slot} on held epoch ${e % 1e6} with no retire posted from +${a - iv.removedAt} to +${b - iv.removedAt} ms`); break; } }
    }
  }
  return v;
}
