import test from "node:test";
import assert from "node:assert/strict";

import { parseFileTokens, withAttachmentMeta } from "../src/core/files.js";
import { audioMime, formatDuration, isVoiceNoteBody, pickVoiceMime, voiceExtension, voiceNoteName, waveformPeaks } from "../src/core/voice-notes.js";

const KEY = Buffer.alloc(32, 7).toString("base64");

test("voice notes are named like the desktop's: voice-<uuid>.<weba|ogg|m4a>", () => {
    assert.equal(voiceNoteName("audio/webm;codecs=opus", "u-1"), "voice-u-1.weba");
    assert.equal(voiceNoteName("audio/ogg;codecs=opus", "u-2"), "voice-u-2.ogg");
    assert.equal(voiceNoteName("audio/mp4", "u-3"), "voice-u-3.m4a");
    assert.equal(voiceExtension("audio/mp4;codecs=mp4a.40.2"), "m4a");
    assert.match(voiceNoteName("audio/webm"), /^voice-[0-9a-f-]{36}\.weba$/);
});

test("the recorder prefers WebM Opus, then Ogg, then MP4 (iOS)", () => {
    assert.equal(pickVoiceMime(() => true), "audio/webm;codecs=opus");
    assert.equal(pickVoiceMime(type => type === "audio/mp4"), "audio/mp4");
    assert.equal(pickVoiceMime(() => false), "");
    assert.equal(pickVoiceMime(() => { throw new Error("unsupported"); }), "");
});

test("audio attachments map to playback types; other files do not", () => {
    assert.equal(audioMime("voice-1.weba"), "audio/webm");
    assert.equal(audioMime("VOICE.OGG"), "audio/ogg");
    assert.equal(audioMime("note.m4a"), "audio/mp4");
    assert.equal(audioMime("song.mp3"), "");
    assert.equal(audioMime("photo.png"), "");
});

test("a voice note message is exactly one audio token", () => {
    const token = `[file:0123456789abcdef0123456789abcdef.vcx#${KEY}#nx1;s=10;d=1500#voice-u.weba]`;
    assert.equal(isVoiceNoteBody(token), true);
    assert.equal(isVoiceNoteBody(`listen ${token}`), false);
    assert.equal(isVoiceNoteBody(token.replace("weba", "png")), false);
});

test("duration and waveform travel in the token's nx1 segment and parse back", () => {
    const uploaded = `[file:0123456789abcdef0123456789abcdef.vcx#${KEY}#nx1;s=2048#voice-u.weba]`;
    const token = withAttachmentMeta(uploaded, { durationMs: 14250, peaks: [0, 0.5, 1] });
    assert.equal(token, `[file:0123456789abcdef0123456789abcdef.vcx#${KEY}#nx1;s=2048;d=14250;w=Ag_#voice-u.weba]`);
    const [ref] = parseFileTokens(token);
    assert.equal(ref.name, "voice-u.weba");
    assert.equal(ref.size, 2048);
    assert.equal(ref.durationMs, 14250);
    assert.deepEqual(ref.peaks.map(p => Math.round(p * 63)), [0, 32, 63]);
    // A token without a segment gains one; a foreign string is left alone.
    assert.equal(withAttachmentMeta(`[file:a.vcx#${KEY}#voice.ogg]`, { durationMs: 900 }), `[file:a.vcx#${KEY}#nx1;d=900#voice.ogg]`);
    assert.equal(withAttachmentMeta("plain text", { durationMs: 900 }), "plain text");
});

test("waveform peaks are scaled to the loudest bar; durations read m:ss", () => {
    const samples = new Float32Array(96);
    samples[10] = 0.25;
    samples[90] = -0.5;
    const peaks = waveformPeaks([samples], 4);
    assert.deepEqual(peaks, [0.5, 0, 0, 1]);
    assert.deepEqual(waveformPeaks([new Float32Array(0)]), []);
    assert.equal(formatDuration(14250), "0:14");
    assert.equal(formatDuration(5 * 60 * 1000), "5:00");
});

test("tokens match the desktop's attachment-meta.js byte for byte", async () => {
    const desktop = await import("../../client/frontend/src/attachment-meta.js");
    const uploaded = `[file:0123456789abcdef0123456789abcdef.vcx#${KEY}#nx1;s=31337#voice-abc.ogg]`;
    const meta = { durationMs: 61000, peaks: Array.from({ length: 48 }, (_, i) => (i % 7) / 6) };
    assert.equal(withAttachmentMeta(uploaded, meta), desktop.withAttachmentMeta(uploaded, meta));
    const [ref] = parseFileTokens(desktop.withAttachmentMeta(uploaded, meta));
    assert.equal(ref.durationMs, 61000);
    assert.equal(ref.peaks.length, 48);
});
