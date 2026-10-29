// Virtual-clock harness around the real MediaKeyEngine (adopted from the
// review 2b fix2 repros): timers run on a monotonic clock, now() is the wall
// clock (it can step), and events are ordered by a sequence number.
const SRC = new URL("../src/media-keys.js", import.meta.url).href;
const mk = await import(SRC);
export const { MediaKeyEngine, REMOVAL_BOUND_MS } = mk;
let worldN = 0;
export function world({ members = 20, seed = 1 } = {}) {
  const me = `u-me-${++worldN}`; let seq = 0; const created = new Map();
  let mono = 1_900_000_000_000; let offset = 0; let now = mono;
  const queue = [];
  const timers = {
    setTimeout(fn, ms) { const t = { at: mono + Math.max(0, ms | 0), fn }; queue.push(t); return t; },
    clearTimeout(t) { const i = queue.indexOf(t); if (i >= 0) queue.splice(i, 1); },
    setInterval(fn, ms) { const t = { at: mono + ms, fn: () => { t.at = mono + ms; queue.push(t); fn(); } }; queue.push(t); return t; },
    clearInterval(t) { const i = queue.indexOf(t); if (i >= 0) queue.splice(i, 1); },
  };
  const tick = () => new Promise(r => setImmediate(r));
  const settle = async () => { for (let i = 0; i < 10; i++) await tick(); await new Promise(r => setTimeout(r, 1)); for (let i = 0; i < 10; i++) await tick(); };
  const advance = async total => { const end = mono + total;
    for (;;) { await settle(); queue.sort((a, b) => a.at - b.at); const next = queue[0];
      if (!next || next.at > end) { mono = end; now = mono + offset; await settle(); return; } queue.shift(); mono = Math.max(mono, next.at); now = mono + offset; next.fn(); } };
  const active = []; const sends = []; const retired = []; const log = [];
  const hooks = { setKey: null, send: null, activate: null };
  const media = {
    async setKey(pid, e, kid, key) { if (pid === "self|mic" && !created.has(e)) created.set(e, ++seq); if (hooks.setKey) await hooks.setKey(pid, e); },
    async activate(pid, e) { if (hooks.activate) await hooks.activate(pid, e); if (pid === "self|mic") active.push([now, e]); },
    async retire(pid, through) { retired.push({ at: now, pid, through, kind: "through" }); },
    async retireOne(pid, epoch) { retired.push({ at: now, pid, epoch, kind: "one" }); },
    async forget() {},
  };
  let stall = () => false; let delayFn = null;
  const engineOpts = { media, timers, now: () => now, sendTimeoutMs: 20_000,
    send: c => { const s = { seq: ++seq, at: now, device: c.device, epoch: c.body.epoch, kid: c.body.data.kid, keys: c.body.data.keys.mic, done: null };
      sends.push(s); hooks.send?.(s);
      if (stall(c.device, c.body.epoch)) return new Promise(() => {});
      if (delayFn) { const d = delayFn(c.device, c.body.epoch); return new Promise(r => timers.setTimeout(() => { s.done = now; r({ ok: true }); }, d)); }
      s.done = now; return Promise.resolve({ ok: true }); } };
  const e = new MediaKeyEngine(engineOpts);
  const m = i => ({ client_id: `c${i}`, unique_id: `u${i}`, media_device: `d${i}`, channel_id: 5 });
  let roster = [{ client_id: "me", unique_id: me, media_device: "d-me", channel_id: 5 }, ...Array.from({ length: members }, (_, i) => m(i))];
  const intervals = []; // {device, removedAt, rejoinedAt}
  const W = {
    e, media, hooks, created, active, sends, retired, timers, advance, settle,
    get now() { return now; }, stepClock(d) { offset += d; now = mono + offset; },
    stall: fn => { stall = fn; }, delay: fn => { delayFn = fn; },
    async start() { e.setRoster(roster); e.start({ channelID: 5, clientID: "me", principal: me, device: "d-me" }); await advance(10_000); },
    join(i) { roster = [...roster, m(i)]; const iv = intervals.findLast(v => v.device === `d${i}` && v.rejoinedAt == null); if (iv) { iv.rejoinedAt = now; iv.jseq = ++seq; } e.setRoster(roster); },
    remove(i) { intervals.push({ device: `d${i}`, removedAt: now, rseq: ++seq, rejoinedAt: null, jseq: Infinity }); roster = roster.filter(r => r.client_id !== `c${i}`); e.setRoster(roster); },
    intervals,
  };
  return W;
}
// Invariant per removal interval. Returns list of violation strings.
export function check(W, { bound = REMOVAL_BOUND_MS } = {}) {
  const v = []; const { active, sends } = W;
  const activeAt = t => { let ep = 0; for (const [time, epoch] of active) if (time <= t) ep = epoch; return ep; };
  for (const iv of W.intervals) {
    const end = iv.rejoinedAt ?? Infinity;
    // sends of an epoch created after removal while D is out (or created in-gap and sent later)
    const cr = ep => W.created.get(ep) ?? 0;
    const bad = sends.filter(s => s.device === iv.device && cr(s.epoch) > iv.rseq && (s.seq < iv.jseq || cr(s.epoch) < iv.jseq));
    if (bad.length) v.push(`${iv.device}@${iv.removedAt}: received ${bad.length} epoch(s) created after its removal: epochs sent at +${[...new Set(bad.map(b => b.at - iv.removedAt))].join(",")}ms`);
    if (end - iv.removedAt <= bound) continue; // re-joined within the bound: entitled again
    const held = new Set(sends.filter(s => s.device === iv.device && s.at < end).map(s => s.epoch));
    const safe = active.find(([t, ep]) => t >= iv.removedAt && !held.has(ep));
    const atBound = activeAt(iv.removedAt + bound);
    if (held.has(atBound) || !safe) v.push(`${iv.device}@${iv.removedAt}: sender still on an epoch it holds at tD+${bound} (first safe activation ${safe ? safe[0] - iv.removedAt + "ms" : "NEVER"})`);
    else if (safe[0] - iv.removedAt > bound) v.push(`${iv.device}: first safe activation ${safe[0] - iv.removedAt}ms`);
    for (const [t] of active) if (t >= iv.removedAt + bound && t < end && held.has(activeAt(t))) { v.push(`${iv.device}: an epoch it holds re-activated at +${t - iv.removedAt}`); break; }
    if (end === Infinity && held.has(activeAt(W.now))) v.push(`${iv.device}: holds the final active epoch`);
  }
  if (!W.e.activeEpoch) v.push("availability: no active epoch at end");
  return v;
}
export function report(name, W, extra = "") { const v = check(W); console.log(`${v.length ? "FAIL" : "ok  "} ${name}${extra ? " " + extra : ""}${v.length ? "\n      " + v.join("\n      ") : ""}`); return v.length; }
