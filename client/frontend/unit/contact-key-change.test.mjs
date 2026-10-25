import { describe, it } from "node:test";
import assert from "node:assert/strict";
import {
    differingGroups, fingerprintsDiffer, formatFingerprint, formatFingerprintLines, groupFingerprint, normalizeFingerprint,
} from "../src/contact-key-change.js";

const sha = "0123456789abcdef".repeat(4);

describe("fingerprint grouping", () => {
    it("splits a SHA-256 hex fingerprint into sixteen 4-character blocks", () => {
        const groups = groupFingerprint(sha);
        assert.equal(groups.length, 16);
        assert.ok(groups.every(group => group.length === 4));
        assert.equal(groups.join(""), sha);
    });

    it("renders two even lines of eight blocks", () => {
        const lines = formatFingerprintLines(sha);
        assert.equal(lines.length, 2);
        assert.equal(lines[0], "0123 4567 89ab cdef 0123 4567 89ab cdef");
        assert.equal(formatFingerprint(sha), lines.join("\n"));
        assert.deepEqual(formatFingerprintLines("abcdef12", { size: 2, perLine: 2 }), ["ab cd", "ef 12"]);
    });

    it("normalizes case and separators before grouping", () => {
        assert.equal(normalizeFingerprint(" AB:CD-EF 01 "), "abcdef01");
        assert.deepEqual(groupFingerprint("AB:CD:EF:01:23"), ["abcd", "ef01", "23"]);
        assert.equal(fingerprintsDiffer("ABCD EF01", "abcd:ef01"), false);
        assert.equal(fingerprintsDiffer(sha, sha.replace(/^0/, "f")), true);
    });

    it("keeps a non-hex value readable and handles empty input", () => {
        assert.equal(normalizeFingerprint("not hex!"), "not hex!");
        assert.deepEqual(groupFingerprint(""), []);
        assert.deepEqual(groupFingerprint(null), []);
        assert.deepEqual(formatFingerprintLines(undefined), []);
        assert.deepEqual(groupFingerprint("abcdef", 0), ["abcd", "ef"]);
    });

    it("marks only the blocks that changed", () => {
        const changed = sha.slice(0, 4) + "ffff" + sha.slice(8);
        const marks = differingGroups(sha, changed);
        assert.equal(marks.length, 16);
        assert.deepEqual(marks.map((flag, index) => flag ? index : -1).filter(index => index >= 0), [1]);
        assert.deepEqual(differingGroups("", "abcd"), [true]);
    });
});
