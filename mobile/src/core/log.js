// log.js — a small in-memory log of the recent console output and uncaught
// errors, so "Export logs" in Diagnostics can hand a support person what
// happened without a debugger attached. Nothing is persisted or sent.

export const LOG_LIMIT = 500;

/** A fixed-size ring of lines; the oldest drop out first. */
export class RingBuffer {
    #items;
    #start = 0;
    #size = 0;

    constructor(limit = LOG_LIMIT) {
        this.limit = Math.max(1, limit | 0);
        this.#items = new Array(this.limit);
    }

    push(item) {
        const at = (this.#start + this.#size) % this.limit;
        this.#items[at] = item;
        if (this.#size < this.limit) this.#size++;
        else this.#start = (this.#start + 1) % this.limit;
    }

    get length() {
        return this.#size;
    }

    /** Oldest first. */
    toArray() {
        const out = [];
        for (let i = 0; i < this.#size; i++) out.push(this.#items[(this.#start + i) % this.limit]);
        return out;
    }

    clear() {
        this.#start = 0;
        this.#size = 0;
        this.#items = new Array(this.limit);
    }
}

/** One argument as text; errors keep their stack, objects become JSON. */
export function formatArg(value) {
    if (value instanceof Error) return value.stack || `${value.name}: ${value.message}`;
    if (typeof value === "string") return value;
    try {
        const text = JSON.stringify(value);
        return text === undefined ? String(value) : text;
    } catch {
        return String(value);
    }
}

/** "2026-10-24T10:00:00.000Z warn message" — one line per entry. */
export function formatLine(entry) {
    return `${new Date(entry.at).toISOString()} ${entry.level.padEnd(5)} ${entry.text}`;
}

const buffer = new RingBuffer(LOG_LIMIT);
let installed = false;

/** Adds one entry; long entries are clipped so one blob cannot evict the rest. */
export function record(level, args, at = Date.now()) {
    const text = args.map(formatArg).join(" ");
    buffer.push({ at, level, text: text.length > 4000 ? `${text.slice(0, 4000)}…` : text });
}

/**
 * Wraps console.* and listens for uncaught errors and rejections. Safe to
 * call more than once; the original console methods still run.
 */
export function installLogger(target = globalThis) {
    if (installed || !target.console) return;
    installed = true;
    for (const level of ["log", "info", "warn", "error", "debug"]) {
        const original = target.console[level]?.bind(target.console);
        if (!original) continue;
        target.console[level] = (...args) => {
            try { record(level, args); } catch { /* never break logging */ }
            original(...args);
        };
    }
    target.addEventListener?.("error", event => record("error", [event.error || event.message || "error", event.filename ? `@${event.filename}:${event.lineno}` : ""].filter(Boolean)));
    target.addEventListener?.("unhandledrejection", event => record("error", ["unhandled rejection:", event.reason]));
}

/** The recorded entries, oldest first. */
export function entries() {
    return buffer.toArray();
}

/** The recorded entries as text, one line each. */
export function logText() {
    return entries().map(formatLine).join("\n");
}

/** Test support. */
export function _clear() {
    buffer.clear();
}
