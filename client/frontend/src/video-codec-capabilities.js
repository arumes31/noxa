// Explicit local prediction only: no media, peer, codec preference or capture changes.
const CODECS = ["H264", "H265", "AV1", "VP9", "VP8"];
const CONFIGURATION = { width: 1920, height: 1080, framerate: 60, bitrate: 8000000 };
const LIMITS = { max_profiles_per_family: 8, max_queries: 32, max_native_pending: 2,
    query_timeout_ms: 2000, total_timeout_ms: 20000, max_advertised_entries: 128 };
const integer = max => value => /^(0|[1-9][0-9]{0,9})$/.test(value) && Number(value) <= max ? Number(value) : null;
const bit = integer(1);
const hex = length => value => new RegExp(`^[0-9a-fA-F]{${length}}$`).test(value) ? value.toLowerCase() : null;
const PARAMETERS = {
    H264: { "profile-level-id": hex(6), "packetization-mode": integer(2), "level-asymmetry-allowed": bit,
        "max-fs": integer(16777215), "max-mbps": integer(16777215), "max-fr": integer(240) },
    H265: { "profile-id": integer(31), "tier-flag": bit, "level-id": integer(255), "interop-constraints": hex(12),
        "sprop-max-don-diff": integer(65535), "tx-mode": value => ["SRST", "MRST", "MRMT"].includes(value) ? value : null },
    AV1: { profile: integer(2), "level-idx": integer(31), tier: bit },
    VP9: { "profile-id": integer(3), "max-fs": integer(16777215), "max-fr": integer(240) },
    VP8: { "max-fs": integer(16777215), "max-fr": integer(240) },
};

function profile(codec, row) {
    if (row.clockRate !== 90000 || (row.sdpFmtpLine != null && typeof row.sdpFmtpLine !== "string")) return null;
    const fmtp = row.sdpFmtpLine || "", parameters = {};
    if (fmtp.length > 512 || /[\r\n\x00]/.test(fmtp)) return null;
    const parts = fmtp ? fmtp.split(";") : [];
    if (parts.length > 16) return null;
    for (const part of parts) {
        const match = /^\s*([a-zA-Z0-9-]+)\s*=\s*([^;=]+?)\s*$/.exec(part);
        if (!match) return null;
        const key = match[1].toLowerCase(), validate = PARAMETERS[codec][key];
        if (!Object.hasOwn(PARAMETERS[codec], key) || Object.hasOwn(parameters, key)) return null;
        const value = validate(match[2]);
        if (value === null) return null;
        parameters[key] = value;
    }
    const canonical = Object.keys(parameters).sort().map(key => `${key}=${parameters[key]}`).join(";");
    return { parameters, contentType: `video/${codec}${canonical ? `;${canonical}` : ""}` };
}

// Prioritize packetization-mode 1 and distinct H264 profiles before additional
// levels of the same profile. Main/high must not disappear behind baseline rows.
function orderedProfiles(codec, profiles) {
    if (codec !== "H264") return profiles;
    const first = [], rest = [], seen = new Set();
    const modeOne = profiles.filter(value => value.parameters["packetization-mode"] === 1);
    for (const value of modeOne) {
        const key = value.parameters["profile-level-id"]?.slice(0, 2) ?? "default";
        if (seen.has(key)) rest.push(value);
        else { seen.add(key); first.push(value); }
    }
    return [...first, ...rest, ...profiles.filter(value => value.parameters["packetization-mode"] !== 1)];
}

function emptyReport(startedAt) {
    return { schema: "noxa_video_codec_capabilities_v1", status: "partial", configuration: { ...CONFIGURATION },
        started_at_ms: startedAt, elapsed_ms: 0, actual_hardware_verified: false,
        apis: { sender: "not_checked", receiver: "not_checked", encoding: "not_checked", decoding: "not_checked" },
        limits: { ...LIMITS }, counts: { advertised_profiles: 0, selected_profiles: 0, omitted_profiles: 0,
            invalid_profiles: 0, duplicate_profiles: 0, uninspected_entries: 0, queries_started: 0, native_pending: 0 },
        codecs: CODECS.flatMap(codec => ["encode", "decode"].map(direction => ({ codec, direction, advertised: null,
            advertised_profiles: 0, omitted_profiles: 0, invalid_profiles: 0, duplicate_profiles: 0, profiles: [] }))) };
}

export function createVideoCodecCapabilityProbe({ sender = globalThis.RTCRtpSender, receiver = globalThis.RTCRtpReceiver,
    mediaCapabilities = globalThis.navigator?.mediaCapabilities, now = () => performance.now(), wallNow = () => Date.now(),
    schedule = (callback, milliseconds) => setTimeout(callback, milliseconds), cancel = timer => clearTimeout(timer) } = {}) {
    const pending = new Map();
    let running = false;
    const query = (key, method, video, report) => {
        // Do not attach another race/continuation to an unresolved native call.
        // Repeated explicit runs report unknown until it actually settles.
        if (pending.has(key)) return null;
        if (pending.size >= LIMITS.max_native_pending) return null;
        // Keep the native slot until native settlement, including after JS timeout
        // or cancellation. Repeated explicit runs cannot accumulate hung calls.
        let result;
        try { result = method.call(mediaCapabilities, { type: "webrtc", video }); }
        catch { result = Promise.reject(null); }
        const promise = Promise.resolve(result).then(value => ({ value }), () => ({ error: true }));
        pending.set(key, promise); report.counts.queries_started++;
        void promise.then(() => { if (pending.get(key) === promise) pending.delete(key); });
        return promise;
    };
    return { async run({ current = () => true } = {}) {
        const report = emptyReport(wallNow()), started = now();
        if (running) { report.status = "busy"; report.counts.native_pending = pending.size; return report; }
        running = true;
        const valid = () => { try { return current() === true; } catch { return false; } };
        const timeLeft = () => LIMITS.total_timeout_ms - Math.max(0, now() - started);
        try {
            if (!valid()) { report.status = "cancelled"; return report; }
            const methods = {};
            for (const [direction, source, api, methodName, methodAPI] of [
                ["encode", sender, "sender", "encodingInfo", "encoding"],
                ["decode", receiver, "receiver", "decodingInfo", "decoding"],
            ]) {
                try { methods[direction] = mediaCapabilities?.[methodName]; } catch { /* Unknown API stays unavailable. */ }
                report.apis[methodAPI] = typeof methods[direction] === "function" ? "available" : "unavailable";
                let rows;
                try {
                    if (typeof source?.getCapabilities !== "function") { report.apis[api] = "unavailable"; continue; }
                    const value = source.getCapabilities("video");
                    if (!Array.isArray(value?.codecs)) { report.apis[api] = "error"; continue; }
                    rows = value.codecs; report.apis[api] = "available";
                } catch { report.apis[api] = "error"; continue; }
                const uninspected = Math.max(0, rows.length - LIMITS.max_advertised_entries);
                report.counts.uninspected_entries += uninspected;
                const groups = report.codecs.filter(group => group.direction === direction);
                for (const group of groups) group.advertised = uninspected ? null : false;
                for (const row of rows.slice(0, LIMITS.max_advertised_entries)) {
                    const mime = typeof row?.mimeType === "string" ? row.mimeType.toUpperCase() : "";
                    const group = groups.find(value => `VIDEO/${value.codec}` === mime);
                    if (!group) continue;
                    group.advertised = true; group.advertised_profiles++; report.counts.advertised_profiles++;
                    const parsed = profile(group.codec, row);
                    if (!parsed) { group.invalid_profiles++; report.counts.invalid_profiles++; continue; }
                    if (group.profiles.some(value => value.contentType === parsed.contentType)) {
                        group.duplicate_profiles++; report.counts.duplicate_profiles++; continue;
                    }
                    group.profiles.push(parsed);
                }
                for (const group of groups) group.profiles = orderedProfiles(group.codec, group.profiles);
            }
            // Round-robin families/directions, so one codec cannot consume the
            // entire query budget before other advertised families are examined.
            const selected = new Set(), jobs = [];
            for (let index = 0; index < LIMITS.max_profiles_per_family && selected.size < LIMITS.max_queries; index++) {
                for (const group of report.codecs) {
                    if (selected.size < LIMITS.max_queries && group.profiles[index]) {
                        selected.add(group.profiles[index]); jobs.push({ group, row: group.profiles[index] });
                    }
                }
            }
            for (const group of report.codecs) {
                const omitted = group.profiles.filter(value => !selected.has(value)).length;
                group.omitted_profiles = omitted; report.counts.omitted_profiles += omitted;
                group.profiles = group.profiles.filter(value => selected.has(value));
                report.counts.selected_profiles += group.profiles.length;
            }
            for (const job of jobs) {
                job.contentType = job.row.contentType; delete job.row.contentType;
                Object.assign(job.row, { status: "budget_exhausted", supported: null, smooth: null, power_efficient: null });
            }
            let cancelled = !valid();
            for (const { group, row, contentType } of jobs) {
                if (!valid()) cancelled = true;
                if (cancelled) { row.status = "cancelled"; continue; }
                if (timeLeft() <= 0) continue;
                if (typeof methods[group.direction] !== "function") { row.status = "api_unavailable"; continue; }
                const promise = query(`${group.direction}:${contentType}`, methods[group.direction],
                    { contentType, ...CONFIGURATION }, report);
                if (!promise) continue;
                let timer, result;
                try {
                    result = await Promise.race([promise, new Promise(resolve => {
                        timer = schedule(() => resolve(null), Math.min(LIMITS.query_timeout_ms, timeLeft()));
                    })]);
                } finally { cancel(timer); }
                if (!valid()) { cancelled = true; row.status = "cancelled"; }
                else if (timeLeft() <= 0 || result === null) row.status = "timeout";
                else if (result.error) row.status = "error";
                else {
                    try {
                        const value = result.value;
                        const supported = value?.supported, smooth = value?.smooth, efficient = value?.powerEfficient;
                        row.supported = typeof supported === "boolean" ? supported : null;
                        row.smooth = typeof smooth === "boolean" ? smooth : null;
                        row.power_efficient = typeof efficient === "boolean" ? efficient : null;
                        row.status = [row.supported, row.smooth, row.power_efficient].some(value => value === null) ? "invalid_result" : "ok";
                    } catch { row.status = "invalid_result"; }
                }
            }
            report.status = cancelled ? "cancelled" : timeLeft() > 0 && Object.values(report.apis).every(value => value === "available") &&
                !report.counts.omitted_profiles && !report.counts.invalid_profiles && !report.counts.uninspected_entries &&
                report.codecs.every(group => group.profiles.every(row => row.status === "ok")) ? "complete" : "partial";
            return report;
        } finally {
            report.elapsed_ms = Math.max(0, now() - started);
            report.counts.native_pending = pending.size;
            running = false;
        }
    } };
}

let defaultProbe;
export function checkVideoCodecCapabilities(options) {
    defaultProbe ??= createVideoCodecCapabilityProbe();
    return defaultProbe.run(options);
}
