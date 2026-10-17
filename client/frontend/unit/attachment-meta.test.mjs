import { describe, it, beforeEach } from "node:test";
import assert from "node:assert/strict";
import { setLanguage } from "../src/i18n.js";
import { parseFileRef } from "../src/chat-parsers.js";
import {
    decodePeaks, encodeAttachmentMeta, encodePeaks, formatDuration, formatFileSize, normalizePeaks, parseAttachmentMeta,
    resamplePeaks, splitNameMeta, voicePreview, withAttachmentMeta,
} from "../src/attachment-meta.js";
import { attachmentKind, fileCardText, previewLine } from "../src/dm-model.js";

const KEY = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=";
const e2e = (text, extra = {}) => ({ text, direct: true, enc: true, encVerified: true, encMode: "e2e", ...extra });

beforeEach(() => setLanguage("en"));

describe("attachment metadata segment", () => {
    it("matches the Go token grammar and round-trips", () => {
        const peaks = Array.from({ length: 48 }, (_, i) => i / 47);
        const segment = encodeAttachmentMeta({ size: 48213, durationMs: 14250, peaks });
        assert.match(segment, /^nx1;s=48213;d=14250;w=[A-Za-z0-9_-]{48}$/);
        const meta = parseAttachmentMeta(segment);
        assert.equal(meta.size, 48213);
        assert.equal(meta.durationMs, 14250);
        assert.equal(meta.peaks.length, 48);
        meta.peaks.forEach((level, i) => assert.ok(Math.abs(level - peaks[i]) <= 1 / 63));
        assert.equal(encodeAttachmentMeta({}), "");
        assert.equal(encodeAttachmentMeta({ size: 0, durationMs: -1, peaks: [] }), "");
    });

    it("parses references with and without metadata like the Go parser", () => {
        const sized = parseFileRef(`blob.vcx#${KEY}#nx1;s=1258291#report.pdf`);
        assert.equal(sized.storage, "blob.vcx");
        assert.equal(sized.key, KEY);
        assert.equal(sized.name, "report.pdf");
        assert.deepEqual(sized.meta, { size: 1258291, durationMs: null, peaks: null });
        assert.equal(parseFileRef(`blob.vcx#${KEY}#report.pdf`).meta, null);
        assert.equal(parseFileRef("legacy.png").meta, null);
        // Unkeyed (legacy) captures and malformed segments never become metadata.
        assert.deepEqual([parseFileRef("blob##nx1;s=5#a.pdf").name, parseFileRef("blob##nx1;s=5#a.pdf").meta], ["nx1;s=5#a.pdf", null]);
        assert.deepEqual(splitNameMeta("nx1;s=1 2#a.pdf"), { name: "nx1;s=1 2#a.pdf", meta: null });
        assert.deepEqual(splitNameMeta("plain.pdf"), { name: "plain.pdf", meta: null });
    });

    it("rejects malformed, oversized and out-of-range values", () => {
        for (const bad of [null, "", "nx1;", "nx2;s=1", "nx1;S=1", "nx1;w=a/b", "nx1;z=" + "a".repeat(300)]) {
            assert.equal(parseAttachmentMeta(bad), null, String(bad));
        }
        assert.deepEqual(parseAttachmentMeta("nx1;s=0;d=99999999999;z=future;w=" + "A".repeat(65)), { size: null, durationMs: null, peaks: null });
        assert.equal(decodePeaks("a/b"), null);
        assert.equal(encodePeaks(null), "");
        assert.equal(encodePeaks([NaN, 2, -1]), "A_A");
    });

    it("adds a voice note's duration and waveform to an uploaded token", () => {
        const uploaded = `[file:blob.vcx#${KEY}#nx1;s=9000#voice-1.weba]`;
        const token = withAttachmentMeta(uploaded, { durationMs: 14000, peaks: [0, 0.5, 1] });
        assert.equal(token, `[file:blob.vcx#${KEY}#nx1;s=9000;d=14000;w=Ag_#voice-1.weba]`);
        // A token from an older backend (no size) still gains the voice fields.
        assert.equal(withAttachmentMeta(`[file:blob.vcx#${KEY}#v.weba]`, { durationMs: 900 }), `[file:blob.vcx#${KEY}#nx1;d=900#v.weba]`);
        // Nothing to add, or not a sealed token: unchanged.
        assert.equal(withAttachmentMeta(`[file:blob.vcx#${KEY}#v.weba]`, { durationMs: 0, peaks: null }), `[file:blob.vcx#${KEY}#v.weba]`);
        assert.equal(withAttachmentMeta("[file:legacy.weba]", { durationMs: 900 }), "[file:legacy.weba]");
        assert.equal(withAttachmentMeta("hello", { durationMs: 900 }), "hello");
    });

    it("formats sizes and durations in English and German", () => {
        assert.equal(formatFileSize(940), "940 B");
        assert.equal(formatFileSize(12 * 1024 + 300), "12 KB");
        assert.equal(formatFileSize(1258291), "1.2 MB");
        assert.equal(formatFileSize(3 * 1024 ** 3), "3 GB");
        assert.equal(formatFileSize(null), "");
        assert.equal(formatFileSize(1258291, "de"), "1,2 MB");
        assert.equal(formatDuration(14000), "0:14");
        assert.equal(formatDuration(65400), "1:05");
        assert.equal(formatDuration(400), "0:01");
        assert.equal(formatDuration(3723000), "1:02:03");
        assert.equal(formatDuration(undefined), "");
    });

    it("resamples and normalizes waveforms", () => {
        assert.deepEqual(resamplePeaks([0, 1], 4), [0, 0, 1, 1]);
        assert.deepEqual(resamplePeaks(null, 4), []);
        assert.deepEqual(normalizePeaks([0.1, 0.4, NaN]), [0.25, 1, 0]);
        assert.deepEqual(normalizePeaks([0, 0]), [0, 0]);
        assert.deepEqual(normalizePeaks([]), []);
    });
});

describe("file cards and previews with and without metadata", () => {
    it("shows the size on a card only when the sealed reference carries it", () => {
        assert.deepEqual(fileCardText("report.pdf", { size: 1258291 }),
            { size: "1.2 MB", meta: "1.2 MB · encrypted", title: "Download report.pdf (1.2 MB)" });
        const legacy = fileCardText("report.pdf", null);
        assert.equal(legacy.size, "");
        assert.equal(legacy.meta, "encrypted");
        setLanguage("de");
        assert.equal(fileCardText("bericht.pdf", { size: 1258291 }).meta, "1,2 MB · verschlüsselt");
    });

    it("previews a voice note with its duration when known", () => {
        const voice = `[file:blob#${KEY}#nx1;s=9000;d=14000#voice.weba]`;
        assert.equal(attachmentKind(voice).meta.durationMs, 14000);
        assert.equal(previewLine(e2e(voice)), "Voice note · 0:14");
        assert.equal(previewLine(e2e(voice, { self: true })), "you: Voice note · 0:14");
        assert.equal(previewLine(e2e(`[file:blob#${KEY}#voice.weba]`)), "Voice note");
        // Duration is a voice-note property; other files keep their name.
        assert.equal(previewLine(e2e(`[file:blob#${KEY}#nx1;s=9000;d=14000#clip.pdf]`)), "File · clip.pdf");
        setLanguage("de");
        assert.equal(previewLine(e2e(voice)), "Sprachnachricht · 0:14");
    });

    it("draws the static voice waveform only from stored peaks", () => {
        assert.deepEqual(voicePreview(parseAttachmentMeta("nx1;d=14000;w=A_g")), { duration: "0:14", bars: [12, 100, 51] });
        assert.deepEqual(voicePreview(null), { duration: "", bars: [] });
        assert.deepEqual(voicePreview({ durationMs: 2000, peaks: null }), { duration: "0:02", bars: [] });
    });
});
