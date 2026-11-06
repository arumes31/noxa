// resume-detector.js — notices that the computer slept. Timers do not run
// while the system is suspended, so a tick that arrives far later than
// scheduled means the machine just resumed. Works without native hooks.
export const RESUME_CHECK_MS = 2000;
export const RESUME_GAP_MS = 10000;

/** Calls onResume(sleptMS) after a suspend; returns a stop function. */
export function createResumeDetector({ onResume, interval = RESUME_CHECK_MS, gap = RESUME_GAP_MS, now = Date.now, timers = globalThis }) {
    let last = now();
    const id = timers.setInterval(() => {
        const current = now();
        const late = current - last - interval;
        last = current;
        if (late >= gap) {
            try { onResume(late); } catch { /* recovery must not stop detection */ }
        }
    }, interval);
    return () => timers.clearInterval(id);
}
