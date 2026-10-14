// Explicit, finite native sender timing. No trace or bridge listener is active by default.
const PREFIX = "__noxa_sender_trace_v1:";
const TYPE = "noxa.sender_trace.v1";
const KINDS = ["encode", "encoded", "enqueue", "process"];
const MAX_BYTES = 2 * 1024 * 1024;
const finite = n => typeof n === "number" && Number.isFinite(n) && n >= 0;
const uint = (n, max) => Number.isSafeInteger(n) && n >= 0 && n <= max;
const identifier = value => typeof value === "string" && value.length > 0 && value.length <= 256;
const reason = value => typeof value === "string" && /^[a-z_]{0,64}$/.test(value) ? value : "unavailable";
const active = state => ["arming", "starting", "recording", "stopping"].includes(state);

export function numericSenderTrace(value) {
    if (!value || value.schema !== "noxa.sender_trace.numeric.v1" || typeof value.complete !== "boolean" ||
        value.timer_cooperative !== true || !finite(value.elapsed_ms) || !uint(value.dropped_unknown_events, Number.MAX_SAFE_INTEGER) ||
        !Array.isArray(value.events) || value.events.length > 32768 ||
        !KINDS.every(key => uint(value.marker_counts?.[key], Number.MAX_SAFE_INTEGER))) return null;
    const events = [];
    for (const event of value.events) {
        if (!event || !KINDS.includes(event.kind) || (!finite(event.trace_us) || event.trace_us > Number.MAX_SAFE_INTEGER) ||
            (event.rtp_timestamp !== undefined && !uint(event.rtp_timestamp, 0xffffffff)) ||
            (event.sequence_number !== undefined && !uint(event.sequence_number, 0xffff))) return null;
        events.push({ kind: event.kind, trace_us: event.trace_us,
            ...(event.rtp_timestamp !== undefined ? { rtp_timestamp: event.rtp_timestamp } : {}),
            ...(event.sequence_number !== undefined ? { sequence_number: event.sequence_number } : {}) });
    }
    const record = { schema: value.schema, complete: value.complete, reason: reason(value.reason), elapsed_ms: value.elapsed_ms,
        timer_cooperative: true, marker_counts: Object.fromEntries(KINDS.map(key => [key, value.marker_counts[key]])),
        events, dropped_unknown_events: value.dropped_unknown_events };
    return new TextEncoder().encode(JSON.stringify(record)).length <= MAX_BYTES ? record : null;
}

export function createSenderTrace({ supported, arm, bridge, captureScope, opaqueScope = () => crypto.randomUUID(),
    now = () => performance.now(), schedule = (fn, ms) => setTimeout(fn, ms), cancel = clearTimeout, onChange = () => {}, onStart = () => {} }) {
    let owner = null, disposed = false, state = "idle", why = "", record = null, timer = null, listening = false, exportRequest = null;
    const notify = () => onChange();
    const live = attempt => { try { return attempt?.current?.() === true; } catch { return false; } };
    function listen() { if (!listening) { bridge.addEventListener("message", receive); listening = true; } }
    function detach() { if (listening) bridge.removeEventListener("message", receive); listening = false; }
    function post(command, attempt = owner) {
        if (!attempt?.token) return false;
        try { bridge.postMessage(PREFIX + JSON.stringify({ command, token: attempt.token, scope: attempt.scope,
            ...(command === "stop" && attempt.interrupted === "scope_changed" ? { scope_lost: true } : {}) })); return true; }
        catch { return false; }
    }
    function settleExport(value) {
        if (!exportRequest) return;
        cancel(exportRequest.timer); const resolve = exportRequest.resolve; exportRequest = null; detach(); resolve(value); notify();
    }
    function finish(next, cause) {
        state = next; why = reason(cause); cancel(timer); timer = null;
        if (owner) owner.current = null;
        detach(); notify();
    }
    function fail(cause) { if (owner) owner.interrupted ??= cause; post("stop"); finish("failed", cause); settleExport(null); }
    function tick() {
        if (!active(state)) return;
        if (now() - owner.started >= 90000 || (state === "arming" && now() - owner.started >= 10000) ||
            (state === "stopping" && now() - owner.stopped >= 10000)) { fail("timeout"); return; }
        if (owner.current && !live(owner)) stop("scope_changed");
        if (active(state)) timer = schedule(tick, 1000);
    }
    function stop(cause = "manual") {
        if (!active(state)) return;
        if (cause === "scope_changed") owner.interrupted = cause; else owner.interrupted ??= cause;
        if (!owner.token) { finish("failed", cause); return; }
        state = "stopping"; why = reason(cause); owner.stopped = now(); owner.current = null;
        if (!post("stop")) fail("unavailable"); else notify();
    }
    function receive(event) {
        let data = event.data;
        if (typeof data === "string") {
            if (data.length > MAX_BYTES + 4096) return;
            try { data = JSON.parse(data); } catch { return; }
        }
        if (!owner || !data || data.type !== TYPE || data.token !== owner.token || data.scope !== owner.scope) return;
        if (exportRequest) {
            if (!["completed", "failed"].includes(data.state)) return;
            const value = numericSenderTrace(data.record);
            if (value && owner.interrupted) { value.complete = false; value.reason = reason(owner.interrupted); }
            if (value && state === "failed") { value.complete = false; value.reason = why || "unavailable"; }
            if (value) record = value;
            else why = "unavailable";
            settleExport(value ? structuredClone(value) : null); return;
        }
        if (!active(state)) return;
        if (owner.current && !live(owner)) { stop("scope_changed"); return; }
        if (["completed", "failed"].includes(data.state)) { finish(data.state, owner.interrupted || data.reason); return; }
        if (state === "stopping") return;
        if (["starting", "recording", "stopping"].includes(data.state)) {
            state = data.state; if (state === "stopping") owner.stopped = now(); notify();
        }
    }
    return {
        async start() {
            if (disposed || active(state) || exportRequest) return false;
            let captured;
            try { captured = captureScope?.(); } catch { return false; }
            if (!captured || !captured.tabID || typeof captured.current !== "function" || !live(captured)) return false;
            const attempt = { ...captured, scope: opaqueScope(), token: null, started: now(), interrupted: null };
            if (!identifier(attempt.scope) || !/^[A-Za-z0-9_-]{16,128}$/.test(attempt.scope)) return false;
            owner = attempt; record = null; state = "arming"; why = ""; onStart(); notify(); timer = schedule(tick, 1000);
            try {
                if (!await supported()) { if (owner === attempt && active(state)) fail("unavailable"); return false; }
                if (disposed || owner !== attempt || state !== "arming") return false;
                if (!live(attempt)) { fail("scope_changed"); return false; }
                const token = await arm(attempt.tabID, attempt.scope);
                if (typeof token !== "string" || !/^[a-f0-9]{64}$/.test(token)) throw new Error("invalid token");
                attempt.token = token;
                if (disposed || owner !== attempt || state !== "arming") { post("stop", attempt); if (owner === attempt) notify(); return false; }
                if (!live(attempt)) { fail("scope_changed"); return false; }
                state = "starting"; listen();
                if (!post("start", attempt)) { fail("unavailable"); return false; }
                notify(); return true;
            } catch { if (owner === attempt && active(state)) fail("unavailable"); return false; }
        },
        stop,
        export() {
            if (record) return Promise.resolve(structuredClone(record));
            if (!owner?.token || active(state) || exportRequest || !["completed", "failed"].includes(state)) return Promise.resolve(null);
            return new Promise(resolve => {
                exportRequest = { resolve, timer: schedule(() => { why = "timeout"; settleExport(null); }, 10000) };
                try { listen(); if (!post("export")) settleExport(null); } catch { settleExport(null); } notify();
            });
        },
        status: () => ({ state, reason: why, disposed, exporting: !!exportRequest, cached: !!record,
            canExport: !!record || !!owner?.token && !active(state) && !exportRequest && ["completed", "failed"].includes(state) }),
        invalidate() { if (active(state)) stop("replaced"); cancel(timer); timer = null; detach(); settleExport(null);
            owner = null; if (!record) { state = "failed"; why = "replaced"; } notify(); },
        dispose() { disposed = true; captureScope = null; stop("scope_changed"); notify(); },
        destroy() { disposed = true; captureScope = null; if (active(state)) { owner.interrupted = "scope_changed"; post("stop"); } cancel(timer); timer = null;
            detach(); settleExport(null); owner = null; record = null; state = "failed"; why = "removed"; },
    };
}

// At most one saved control per window, retained for 30 minutes. Native data is
// fetched only by an explicit Copy click. A new trace invalidates uncopied data.
const archives = new WeakMap();
function archiveFor(doc) {
    if (archives.has(doc)) return archives.get(doc);
    let saved = null, expiry = null;
    function clear() { if (saved) { saved.control.destroy(); saved.panel.remove(); } saved = null; clearTimeout(expiry); expiry = null; }
    const archive = {
        save(panel, control) {
            clear(); saved = { panel, control };
            doc.getElementById("sharing-status")?.insertAdjacentElement("beforebegin", panel);
            panel.dataset.savedTrace = "true"; expiry = setTimeout(clear, 30 * 60000);
        },
        replaceNative() { saved?.control.invalidate(); },
        clear,
    };
    doc.defaultView?.addEventListener("pagehide", clear);
    archives.set(doc, archive); return archive;
}

export function mountSenderTrace(container, options, translate, copy) {
    const doc = container.ownerDocument, view = doc.defaultView, app = view.go?.main?.App, bridge = view.chrome?.webview;
    if (!bridge?.postMessage || !bridge.addEventListener || !bridge.removeEventListener ||
        typeof app?.SupportsSenderTrace !== "function" || typeof app?.ArmSenderTraceForTab !== "function") return { render() {}, dispose() {} };
    const panel = doc.createElement("div"); panel.className = "stream-recording sender-trace";
    panel.innerHTML = '<p></p><button type="button" data-trace="start"></button> <button type="button" data-trace="stop"></button> <button type="button" data-trace="copy"></button> <button type="button" data-trace="clear" hidden></button><small></small><p role="status"></p>';
    container.append(panel);
    const archive = archiveFor(doc);
    const control = createSenderTrace({ ...options, bridge, supported: () => app.SupportsSenderTrace(),
        arm: (tab, scope) => app.ArmSenderTraceForTab(tab, scope), onChange: render, onStart: () => archive.replaceNative() });
    function render() {
        const status = control.status();
        panel.querySelector("p").textContent = translate(status.disposed ? "trace.saved" : "trace.title");
        panel.querySelector("small").textContent = translate("trace.local");
        for (const key of ["start", "stop", "copy", "clear"]) panel.querySelector(`[data-trace="${key}"]`).textContent = translate(`trace.${key}`);
        panel.querySelector('[data-trace="start"]').disabled = status.disposed || active(status.state) || status.exporting;
        panel.querySelector('[data-trace="stop"]').disabled = !active(status.state) || status.state === "stopping";
        panel.querySelector('[data-trace="copy"]').disabled = !status.canExport || status.exporting;
        panel.querySelector('[data-trace="clear"]').hidden = !status.disposed;
        panel.querySelector('[role="status"]').textContent = status.state === "idle" ? "" : translate(`trace.state.${status.state}`);
        if (status.reason) panel.querySelector('[role="status"]').textContent += " · " + translate("trace.reason", { reason: status.reason });
    }
    panel.querySelector('[data-trace="start"]').onclick = () => { void control.start(); };
    panel.querySelector('[data-trace="stop"]').onclick = () => control.stop();
    panel.querySelector('[data-trace="copy"]').onclick = async () => {
        const record = await control.export();
        if (record && panel.isConnected) await copy(JSON.stringify({ sender_trace: record }, null, 2), { isCurrent: () => panel.isConnected });
    };
    panel.querySelector('[data-trace="clear"]').onclick = () => archive.clear();
    render(); return { render, dispose() { control.dispose(); if (control.status().state !== "idle") archive.save(panel, control); else control.destroy(); } };
}
