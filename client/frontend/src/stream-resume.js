// stream-resume.js — stream watches a resumed session asks to restore.
// After a session resumes, the server sends stream_watch_resume: to the
// resumed client for the sources it watched, and to its viewers for its own
// sources, which its rebuilt peer publishes again. The next listing of such a
// source starts the normal watch request once; nothing else is bypassed.
export const RESUME_WATCH_MS = 5 * 60_000;
const RESUME_WATCH_MAX = 64;

export function createResumeWatches(now = () => Date.now()) {
    const pending = new Map(); // "publisher|slot" -> expiry
    return {
        note(publisherID, slot) {
            if (typeof publisherID !== "string" || !publisherID || (slot !== "screen" && slot !== "cam")) return false;
            const id = `${publisherID}|${slot}`;
            pending.delete(id);
            pending.set(id, now() + RESUME_WATCH_MS);
            while (pending.size > RESUME_WATCH_MAX) pending.delete(pending.keys().next().value);
            return true;
        },
        take(id) {
            const until = pending.get(id);
            pending.delete(id);
            return until !== undefined && until >= now();
        },
    };
}
