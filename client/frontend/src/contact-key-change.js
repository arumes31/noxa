// contact-key-change.js — pure helpers for the "contact's signing key
// changed" confirmation (item 241). The dialog shows both fingerprints in
// readable blocks so a person can compare them with the contact out of band.

export const FINGERPRINT_GROUP_SIZE = 4;
export const FINGERPRINT_GROUPS_PER_LINE = 8;

// normalizeFingerprint strips separators and lowercases hex; anything else
// (an unexpected format from an older server) is returned trimmed as-is so
// the dialog still shows something comparable.
export function normalizeFingerprint(value) {
    const text = String(value ?? "").trim();
    const compact = text.replace(/[\s:-]+/g, "");
    return /^[0-9a-fA-F]+$/.test(compact) ? compact.toLowerCase() : text;
}

// groupFingerprint splits a fingerprint into 4-character blocks.
export function groupFingerprint(value, size = FINGERPRINT_GROUP_SIZE) {
    const normalized = normalizeFingerprint(value);
    if (!normalized) return [];
    const step = Math.max(1, Number(size) || FINGERPRINT_GROUP_SIZE);
    const groups = [];
    for (let index = 0; index < normalized.length; index += step) groups.push(normalized.slice(index, index + step));
    return groups;
}

// formatFingerprintLines returns display lines of grouped blocks (8 blocks
// per line turns a 64-hex SHA-256 fingerprint into two even rows).
export function formatFingerprintLines(value, { size = FINGERPRINT_GROUP_SIZE, perLine = FINGERPRINT_GROUPS_PER_LINE } = {}) {
    const groups = groupFingerprint(value, size);
    const lines = [];
    const width = Math.max(1, Number(perLine) || FINGERPRINT_GROUPS_PER_LINE);
    for (let index = 0; index < groups.length; index += width) lines.push(groups.slice(index, index + width).join(" "));
    return lines;
}

export function formatFingerprint(value, options) {
    return formatFingerprintLines(value, options).join("\n");
}

// fingerprintsDiffer compares two fingerprints ignoring formatting.
export function fingerprintsDiffer(a, b) {
    return normalizeFingerprint(a) !== normalizeFingerprint(b);
}

// differingGroups marks which blocks of the new fingerprint differ from the
// old one, so the dialog can emphasise them (with text, not only color).
export function differingGroups(oldValue, newValue, size = FINGERPRINT_GROUP_SIZE) {
    const before = groupFingerprint(oldValue, size);
    const after = groupFingerprint(newValue, size);
    return after.map((group, index) => group !== before[index]);
}
