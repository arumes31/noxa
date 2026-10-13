// A manual, local record of existing polls; this module never requests stats.
const MAX_SAMPLES = 1000;
const MAX_BYTES = 4 * 1024 * 1024;
const numeric = value => typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : null;
const fields = `sample_ms width height requested_fps settings_fps capture_width capture_height capture_fps encoded_fps sent_fps reported_fps bitrate_bps encode_ms target_bitrate_bps available_outgoing_bitrate_bps transport_rtt_ms remote_rtt_ms remote_fraction_lost encoding_max_bitrate_bps bandwidth_limited_ms cpu_limited_ms retransmit_bitrate_bps retransmit_percent frame_bytes key_frames key_frames_delta pli_count fir_count huge_frames_sent qp_sum send_delay_ms frames_encoded frames_sent packets_sent bytes_sent retransmitted_packets retransmitted_bytes received_fps decoded_fps dropped_fps decode_ms jitter_buffer_ms recent_loss_percent nacks plis freezes frames_received frames_decoded frames_dropped freeze_count total_freezes_duration_seconds timestamp_ms bytes_received`.split(" ");
const counters = new Set(`frames_encoded frames_sent packets_sent bytes_sent retransmitted_packets retransmitted_bytes key_frames pli_count fir_count huge_frames_sent qp_sum frames_received frames_decoded frames_dropped freeze_count total_freezes_duration_seconds bytes_received`.split(" "));
const presentationFields = "sample_ms observed_callbacks last_callback_age_ms callback_gaps_over_100_ms unobserved_presentation_callbacks callback_coverage".split(" ");
const distributions = "callback_interval_ms compositor_submission_interval_ms predicted_display_interval_ms decoder_processing_ms estimated_receive_to_display_ms browser_estimated_capture_to_display_ms".split(" ");
const clone = value => structuredClone(value);

export function diagnosticSample(direction, rows, presentation) {
    if (!Array.isArray(rows) || !rows.length || rows.length > 8) return null;
    const projected = rows.map(row => {
        const out = Object.fromEntries(fields.map(key => [key, numeric(row[key])]));
        const codec = String(row.codec ?? "").replace(/^video\//i, "").toUpperCase();
        out.codec = ["H264", "H265", "VP8", "VP9", "AV1"].includes(codec) ? codec : null;
        out.quality_reason = ["none", "bandwidth", "cpu", "other"].includes(row.quality_reason) ? row.quality_reason : null;
        out.power_efficient = typeof row.power_efficient === "boolean" ? row.power_efficient : null;
        out.encoding_active = typeof row.encoding_active === "boolean" ? row.encoding_active : null;
        // Export fixed families, not implementation-provided arbitrary strings.
        const implementation = String(row.implementation ?? "").split(/fallback from:/i)[0].toLowerCase();
        out.implementation = /ffmpeg|openh264|libvpx|dav1d/.test(implementation) ? "software" :
            /nvidia|nvenc/.test(implementation) ? "nvidia" : /d3d11/.test(implementation) ? "d3d11" :
                /mediafoundation/.test(implementation) ? "mediafoundation" : /intel|qsv/.test(implementation) ? "intel" :
                    /amd|amf/.test(implementation) ? "amd" : null;
        return out;
    });
    let timing = null;
    if (direction === "received" && presentation) {
        timing = Object.fromEntries(presentationFields.map(key => [key, numeric(presentation[key])]));
        timing.stale = typeof presentation.stale === "boolean" ? presentation.stale : null;
        timing.window_ms = 30000;
        for (const key of distributions) timing[key] = Object.fromEntries(
            ["count", "mean", "variance", "p50", "p95", "p99", "max"].map(field => [field, numeric(presentation[key]?.[field])]));
    }
    return { rows: projected, presentation: timing };
}

export function receiverRecordingSample(sample, presentation) {
    // summarizeStream aggregates rows. Do not duplicate those totals as per-row data.
    if (sample?.cumulative?.length !== 1) return null;
    return diagnosticSample("received", sample.cumulative.map(row => ({ ...row,
        codec: sample.codec, width: sample.width, height: sample.height, sample_ms: sample.sampleMS,
        bitrate_bps: sample.bitrate, bytes_received: sample.bytes, received_fps: sample.receivedFPS, decoded_fps: sample.fps,
        dropped_fps: sample.droppedFPS, decode_ms: sample.decodeMS, jitter_buffer_ms: sample.bufferMS,
        recent_loss_percent: sample.lossPercent, nacks: sample.nacks, plis: sample.plis, freezes: sample.freezes,
        implementation: sample.decoder, power_efficient: sample.powerEfficientDecoder,
    })), presentation);
}

export function createStreamDiagnosticRecording({ direction, slot = null, cadenceMS, captureScope,
    now = () => performance.now(), utc = () => Date.now(), schedule = (fn, ms) => setTimeout(fn, ms), cancel = clearTimeout,
    onChange = () => {}, onFinish = () => {}, onStart = () => {}, maxSamples = MAX_SAMPLES, maxBytes = MAX_BYTES }) {
    let record = null, timer = null, current = null, identity = null, previous = null, known = null;
    function finish(reason) {
        if (record?.state !== "recording") return;
        record.state = reason === "duration" ? "completed" : "interrupted";
        record.stop_reason = reason;
        record.ended_monotonic_ms = now(); record.ended_utc_ms = utc();
        record.trailing_gap_ms = Math.max(0, Math.min(now(), record.deadline_monotonic_ms) - (record.samples.at(-1)?.monotonic_ms ?? record.started_monotonic_ms));
        cancel(timer); timer = null; current = null; onFinish(clone(record)); onChange();
    }
    function check() {
        if (record?.state !== "recording") return false;
        if (!current()) { finish("scope_changed"); return false; }
        if (now() >= record.deadline_monotonic_ms) { finish("duration"); return false; }
        return true;
    }
    function tick() { if (check()) { onChange(); timer = schedule(tick, Math.min(1000, record.deadline_monotonic_ms - now())); } }
    return {
        start(minutes = 5) {
            if (record?.state === "recording" || ![5, 30].includes(minutes)) return false;
            current = captureScope(); if (typeof current !== "function" || !current()) { current = null; return false; }
            identity = null; previous = null; known = null;
            const start = now();
            record = { schema: "noxa-stream-recording-v1", direction, slot: ["screen", "cam"].includes(slot) ? slot : null, state: "recording", stop_reason: null,
                epoch: 1, started_monotonic_ms: start, started_utc_ms: utc(), deadline_monotonic_ms: start + minutes * 60000,
                ended_monotonic_ms: null, ended_utc_ms: null, cadence_ms: cadenceMS, sample_count: 0, sample_bytes: 0,
                limits: { samples: maxSamples, sample_bytes: maxBytes }, failures: 0, first_failure_monotonic_ms: null, last_failure_monotonic_ms: null, gaps: 0, largest_gap_ms: 0, trailing_gap_ms: null,
                note: "Local existing-poll snapshots. Missing values are unknown. Geometry and hardware are sampled, not continuous. Entry times are local poll completion; sender counter intervals are approximate, not RTP or RTCStats clock timestamps. Presentation distributions are overlapping 30-second windows, not whole-record percentiles or physical-display measurements.", samples: [] };
            timer = schedule(tick, 1000); onStart(); onChange(); return true;
        },
        add(sample, key) {
            if (!check()) return false;
            if (!sample || typeof key !== "string" || !key.length) { this.fail(); return false; }
            if (identity !== null && key !== identity) { finish("epoch_changed"); return false; }
            const safe = clone(sample);
            if (previous && safe.rows.length !== previous.rows.length) { finish("epoch_changed"); return false; }
            if (known && safe.rows.some((row, i) => [...counters].some(field =>
                row[field] !== null && known[i]?.[field] != null && row[field] < known[i][field]))) {
                finish("counter_reset"); return false;
            }
            const at = now(), gap = at - (record.samples.at(-1)?.monotonic_ms ?? record.started_monotonic_ms);
            const entry = { monotonic_ms: at, utc_ms: utc(), gap_ms: gap, ...safe };
            const bytes = new TextEncoder().encode(JSON.stringify(entry)).length;
            if (record.sample_count >= maxSamples || record.sample_bytes + bytes > maxBytes) { finish("resource_limit"); return false; }
            identity = key; previous = safe;
            known ??= safe.rows.map(() => ({}));
            safe.rows.forEach((row, i) => { for (const field of counters) if (row[field] !== null) known[i][field] = row[field]; });
            if (gap > cadenceMS * 2) record.gaps++;
            record.largest_gap_ms = Math.max(record.largest_gap_ms, gap);
            record.samples.push(entry); record.sample_count++; record.sample_bytes += bytes; onChange(); return true;
        },
        fail() { if (check()) { record.failures++; record.first_failure_monotonic_ms ??= now(); record.last_failure_monotonic_ms = now(); onChange(); } },
        stop: (reason = "manual") => finish(reason),
        snapshot: () => record ? clone(record) : null,
        status: () => record ? { state: record.state, stop_reason: record.stop_reason, sample_count: record.sample_count,
            remaining_seconds: Math.max(0, Math.ceil((record.deadline_monotonic_ms - now()) / 1000)) } : null,
        dispose: () => { finish("scope_changed"); cancel(timer); },
    };
}

export function mountStreamDiagnosticRecording(container, options, translate, copy) {
    const panel = document.createElement("div"); panel.className = "stream-recording";
    panel.innerHTML = '<label><span></span> <select><option value="5">5 min</option><option value="30">30 min</option></select></label> <button type="button"></button><p aria-live="off"></p><span class="sr-only" role="status"></span><small></small>';
    container.append(panel);
    const select = panel.querySelector("select"), button = panel.querySelector("button");
    let announcedState = null;
    const archive = copy ? diagnosticArchiveUI(container.ownerDocument, translate, copy) : null;
    const token = {};
    const recorder = createStreamDiagnosticRecording({ ...options, onChange: render,
        onStart: () => archive?.remove(token), onFinish: record => archive?.save(token, record) });
    function render() {
        const status = recorder.status(), active = status?.state === "recording";
        panel.querySelector("label span").textContent = translate("record.duration");
        panel.querySelector("small").textContent = translate("record.local");
        select.disabled = active; button.textContent = translate(active ? "record.stop" : "record.start");
        panel.querySelector("p").textContent = status ? translate(active ? "record.progress" : "record.finished", {
            count: status.sample_count, seconds: status.remaining_seconds, reason: translate(`record.reason.${status.stop_reason}`) }) : "";
        if (status?.state !== announcedState) {
            announcedState = status?.state;
            panel.querySelector('[role="status"]').textContent = active ? translate("record.started") : panel.querySelector("p").textContent;
        }
    }
    button.onclick = () => { if (recorder.status()?.state === "recording") recorder.stop(); else recorder.start(Number(select.value)); };
    render(); return { ...recorder, render };
}


// Only sanitized completed records survive a tile/session teardown. No media or
// scope references, disk storage or background collection are retained here.
export function createDiagnosticArchive({ now = () => performance.now(), schedule = (fn, ms) => setTimeout(fn, ms),
    cancel = clearTimeout, onChange = () => {}, retentionMS = 30 * 60000, maxRecords = 4, maxBytes = 16 * 1024 * 1024 } = {}) {
    const records = new Map();
    let bytes = 0;
    function remove(token) {
        const item = records.get(token); if (!item) return;
        cancel(item.timer); bytes -= item.bytes; records.delete(token); onChange();
    }
    return {
        save(token, record) {
            if (!record || record.state === "recording") return false;
            const data = clone(record), size = new TextEncoder().encode(JSON.stringify(data)).length;
            remove(token); if (size > maxBytes) return false;
            while (records.size >= maxRecords || bytes + size > maxBytes) remove(records.keys().next().value);
            records.set(token, { data, bytes: size, expires: now() + retentionMS, timer: schedule(() => remove(token), retentionMS) });
            bytes += size; onChange(); return true;
        },
        remove,
        clear() { for (const token of [...records.keys()]) remove(token); },
        entries() {
            for (const [token, item] of records) if (now() >= item.expires) remove(token);
            return [...records].map(([token, item]) => ({ token, direction: item.data.direction, state: item.data.state,
                stop_reason: item.data.stop_reason, started_utc_ms: item.data.started_utc_ms, ended_utc_ms: item.data.ended_utc_ms }));
        },
        has(token) { const item = records.get(token); if (!item) return false;
            if (now() >= item.expires) { remove(token); return false; } return true; },
        read(token) { const item = records.get(token); if (!item) return null;
            if (now() >= item.expires) { remove(token); return null; } return clone(item.data); },
    };
}

const diagnosticArchives = new WeakMap();
function diagnosticArchiveUI(doc, translate, copy) {
    if (diagnosticArchives.has(doc)) return diagnosticArchives.get(doc);
    const root = doc.createElement("details"); root.className = "stream-recording stream-recording-archive"; root.hidden = true;
    root.innerHTML = '<summary></summary><p></p><div></div>';
    const anchor = doc.getElementById("sharing-status") || doc.getElementById("video-grid");
    anchor?.insertAdjacentElement("beforebegin", root);
    const archive = createDiagnosticArchive({ onChange: render });
    function render() {
        const entries = archive.entries(); root.hidden = !entries.length;
        root.querySelector("summary").textContent = translate("record.saved", { count: entries.length });
        root.querySelector("p").textContent = translate("record.retention");
        root.querySelector("div").replaceChildren(...entries.map(entry => {
            const row = doc.createElement("div"), label = doc.createElement("p"), copyButton = doc.createElement("button"), clear = doc.createElement("button");
            const date = value => Number.isFinite(value) ? new Date(value).toISOString() : "—";
            label.textContent = `${translate(`record.${entry.direction}`)} · ${translate(`record.reason.${entry.stop_reason}`)} · ${date(entry.started_utc_ms)} – ${date(entry.ended_utc_ms)}`;
            copyButton.type = clear.type = "button"; copyButton.textContent = translate("record.copySaved"); clear.textContent = translate("record.clear");
            copyButton.onclick = () => { const record = archive.read(entry.token); if (record) void copy(JSON.stringify({ recording: record }, null, 2), { isCurrent: () => root.isConnected && archive.has(entry.token) }); };
            clear.onclick = () => archive.remove(entry.token);
            row.append(label, copyButton, clear); return row;
        }));
    }
    doc.defaultView?.addEventListener("pagehide", () => archive.clear());
    doc.defaultView?.addEventListener("noxa-language-changed", render);
    diagnosticArchives.set(doc, archive); return archive;
}
