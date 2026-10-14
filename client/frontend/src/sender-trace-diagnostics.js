// Optional numeric diagnostics for a rejected finite native trace. Never copy
// arbitrary trace strings, PID/TID values, arguments, or timings from the tail.
const LIMIT = 131072;
const KINDS = ["encode", "encoded", "enqueue", "process"];
const SCOPES = ["global", "process", "thread", "absent", "other"];
const RELATIONS = ["same", "different", "unavailable"];
const CHECKS = ["cross_kind_pid", "within_kind_pid", "within_kind_tid", "timestamp", "producer_id", "rtp_timestamp", "sequence_number",
    "message_limit", "raw_limit", "event_limit", "retained_limit", "diagnostic_limit", "malformed_data"];
const HISTOGRAMS = { phases: ["B", "X", "I", "i", "E", "other"], scopes: SCOPES,
    pid_states: ["absent", "invalid", "zero", "positive"], tid_states: ["absent", "invalid", "zero", "positive"] };
const count = n => Number.isSafeInteger(n) && n >= 0 && n <= LIMIT;

export function numericTraceDiagnostics(value) {
    if (!value || value.scan_limit !== LIMIT || !count(value.entries_examined) || !count(value.entries_after_freeze) ||
        !count(value.malformed_entries) || value.entries_after_freeze > value.entries_examined || value.malformed_entries > value.entries_examined ||
        typeof value.classification_truncated !== "boolean") return null;
    const byKind = {};
    let seen = 0;
    for (const kind of KINDS) {
        const input = value.by_kind?.[kind];
        if (!input || !count(input.seen) || !count(input.category_expected) || !count(input.category_other) ||
            input.category_expected + input.category_other !== input.seen) return null;
        const output = { seen: input.seen, category_expected: input.category_expected, category_other: input.category_other };
        for (const [field, keys] of Object.entries(HISTOGRAMS)) {
            if (!keys.every(key => count(input[field]?.[key])) || keys.reduce((sum, key) => sum + input[field][key], 0) !== input.seen) return null;
            output[field] = Object.fromEntries(keys.map(key => [key, input[field][key]]));
        }
        byKind[kind] = output; seen += input.seen;
    }
    if (seen + value.malformed_entries > value.entries_examined) return null;
    let rejection;
    if (value.first_rejection !== undefined) {
        const r = value.first_rejection;
        if (!r || ![...KINDS, "none"].includes(r.kind) || !CHECKS.includes(r.check) || !SCOPES.includes(r.instant_scope) ||
            !RELATIONS.includes(r.pid_relation) || !RELATIONS.includes(r.tid_relation)) return null;
        rejection = { kind: r.kind, check: r.check, instant_scope: r.instant_scope, pid_relation: r.pid_relation, tid_relation: r.tid_relation };
    }
    if (!rejection && (value.entries_after_freeze > 0 || value.classification_truncated)) return null;
    return { scan_limit: LIMIT, entries_examined: value.entries_examined, entries_after_freeze: value.entries_after_freeze,
        malformed_entries: value.malformed_entries, classification_truncated: value.classification_truncated, by_kind: byKind,
        ...(rejection ? { first_rejection: rejection } : {}) };
}
