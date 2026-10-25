// settings-registry.js — the sections of the Settings area on the You
// screen. Any feature module can add its own rows without touching you.js:
//
//     import { registerSettings } from "./settings-registry.js";
//     registerSettings("voice", [
//         { key: "voice.echo", label: t("voice.echo"), keywords: "aec", render: () => toggleRow(...) },
//     ], { title: t("settings.section.voice") });
//
// section  a short id. Known ids keep a fixed order: appearance, chat,
//          notifications, voice, dm, language, data, about; others follow
//          in registration order. Registering an existing section appends
//          rows (a row with the same key replaces the old one).
// row      {key, label, render(), keywords?}. render() returns a DOM node
//          and is called each time the You screen draws, so it reads
//          current values. label (plus keywords) is what the search box at
//          the top of Settings matches.
// options  {title} — the section heading; defaults to the
//          "settings.section.<id>" string, or the id itself.
//
// Register at module load and import the module from where the feature
// lives (or from you.js), so the rows exist before the You screen renders.
// Workstreams expected to plug in here: "voice", "notifications", "dm".

const ORDER = ["appearance", "chat", "notifications", "voice", "dm", "language", "data", "about"];
const sections = new Map(); // id -> {id, title, rows: Map<key, row>, seq}
let sequence = 0;
const listeners = new Set();

export function registerSettings(section, rows = [], { title } = {}) {
    if (!section || typeof section !== "string") throw new Error("registerSettings needs a section id");
    let entry = sections.get(section);
    if (!entry) {
        entry = { id: section, title: title || "", rows: new Map(), seq: sequence++ };
        sections.set(section, entry);
    }
    if (title) entry.title = title;
    for (const row of rows) {
        if (!row?.key || typeof row.render !== "function") throw new Error("a settings row needs key and render()");
        entry.rows.set(row.key, { label: row.key, keywords: "", ...row });
    }
    for (const listener of listeners) listener(section);
}

/** Removes a section or one row of it (tests, features that go away). */
export function unregisterSettings(section, key) {
    if (key === undefined) sections.delete(section);
    else sections.get(section)?.rows.delete(key);
    for (const listener of listeners) listener(section);
}

function rank(entry) {
    const known = ORDER.indexOf(entry.id);
    return known >= 0 ? known : ORDER.length + entry.seq;
}

/** Sections in display order: [{id, title, rows: [...]}]. */
export function settingsSections() {
    return [...sections.values()].sort((a, b) => rank(a) - rank(b))
        .map(entry => ({ id: entry.id, title: entry.title, rows: [...entry.rows.values()] }));
}

/** Whether a row matches a search: every word in label, keywords or section title. */
export function rowMatches(row, query, sectionTitle = "") {
    const words = String(query || "").toLowerCase().split(/\s+/).filter(Boolean);
    if (!words.length) return true;
    const haystack = `${row.label} ${row.keywords || ""} ${sectionTitle}`.toLowerCase();
    return words.every(word => haystack.includes(word));
}

/** Sections with only the rows that match; empty sections are dropped. */
export function filterSettings(query, list = settingsSections()) {
    if (!String(query || "").trim()) return list;
    return list.map(section => ({ ...section, rows: section.rows.filter(row => rowMatches(row, query, section.title)) }))
        .filter(section => section.rows.length);
}

/** Calls listener(sectionID) after every registration; returns a stop function. */
export function onSettingsRegistered(listener) {
    listeners.add(listener);
    return () => listeners.delete(listener);
}
