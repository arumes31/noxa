// voice-notes.js — the voice message convention shared with the desktop
// (client/frontend/src/voice-messages.js, voice-recording.js):
//   - recorded with MediaRecorder as Opus in WebM, Opus in Ogg, or AAC in MP4
//     (iOS), at most 5 minutes and 5 MiB;
//   - uploaded as an ordinary encrypted attachment named
//     "voice-<uuid>.<weba|ogg|m4a>" and sent as a message whose whole text is
//     that one [file:…] token, with the duration (d) and a 48-bar waveform (w)
//     in the token's metadata segment.
// The desktop shows a voice player for exactly such messages; any attachment
// with one of the three extensions plays inline here.

export const VOICE_MAX_MS = 5 * 60 * 1000;
export const VOICE_MAX_BYTES = 5 * 1024 * 1024;
export const WAVEFORM_BARS = 48;

/** Recording formats in order of preference (the desktop's order). */
export const VOICE_MIME_TYPES = ["audio/webm;codecs=opus", "audio/ogg;codecs=opus", "audio/mp4"];

const MIME_BY_EXTENSION = { weba: "audio/webm", ogg: "audio/ogg", m4a: "audio/mp4" };

/** The first format the recorder supports, or "". */
export function pickVoiceMime(isTypeSupported = type => globalThis.MediaRecorder?.isTypeSupported?.(type)) {
    return VOICE_MIME_TYPES.find(type => { try { return !!isTypeSupported(type); } catch { return false; } }) || "";
}

/** The file extension of a recorded MIME type. */
export function voiceExtension(mimeType) {
    const type = String(mimeType || "");
    return type.includes("ogg") ? "ogg" : type.includes("mp4") ? "m4a" : "weba";
}

/** The attachment name of a new voice note. */
export function voiceNoteName(mimeType, id = crypto.randomUUID()) {
    return `voice-${id}.${voiceExtension(mimeType)}`;
}

/** The playback MIME type of an attachment name, or "" when it is not audio. */
export function audioMime(name) {
    const extension = String(name || "").split(".").pop().toLowerCase();
    return MIME_BY_EXTENSION[extension] || "";
}

/** Whether a message body is a voice note: exactly one audio attachment token. */
export function isVoiceNoteBody(text) {
    const match = /^\[file:([^\]]+)\]$/.exec(String(text || "").trim());
    if (!match) return false;
    const fields = match[1].split("#");
    return fields.length >= 3 && !!audioMime(fields.at(-1));
}

/** m:ss of milliseconds. */
export function formatDuration(ms) {
    const total = Math.max(0, Math.round((Number(ms) || 0) / 1000));
    return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, "0")}`;
}

/**
 * Peak level per bar over the decoded channels, scaled so the loudest bar
 * is 1 (speech rarely reaches full scale and would look flat).
 */
export function waveformPeaks(channels, bars = WAVEFORM_BARS) {
    const length = channels[0]?.length || 0;
    if (!length) return [];
    const peaks = [];
    for (let bar = 0; bar < bars; bar++) {
        const from = Math.floor(bar * length / bars);
        const to = Math.max(from + 1, Math.floor((bar + 1) * length / bars));
        let peak = 0;
        for (const data of channels) for (let i = from; i < to && i < length; i++) peak = Math.max(peak, Math.abs(data[i]));
        peaks.push(peak);
    }
    const max = Math.max(...peaks);
    return max > 0 ? peaks.map(level => level / max) : peaks;
}

/** Root-mean-square level of samples in dBFS (-60 floor), for the meter. */
export function levelDB(samples) {
    let sum = 0;
    for (const sample of samples) sum += sample * sample;
    const rms = Math.sqrt(sum / Math.max(1, samples.length));
    return Math.max(-60, 20 * Math.log10(rms || 1e-6));
}
