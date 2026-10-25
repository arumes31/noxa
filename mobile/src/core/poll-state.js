// poll-state.js — poll arithmetic for the poll card: percentages, ballot
// toggling, validation before sending (internal/netproto/poll.go rules) and
// the closes-at countdown. Parsing is ChatSession.parsePoll.

export const POLL_DURATIONS = Object.freeze({ hour: 3600, day: 86400, week: 604800 });

/**
 * Whole-number percent of voters per option. Multiple choice can sum past
 * 100; with no voters every option is 0.
 */
export function pollPercentages(counts = [], totalVoters = 0) {
    return counts.map(count => totalVoters > 0 ? Math.round((Math.max(0, count) * 100) / totalVoters) : 0);
}

/** The ballot after tapping option index: single choice replaces, multiple toggles. */
export function togglePollChoice(choices = [], index, multiple) {
    if (!multiple) return choices.length === 1 && choices[0] === index ? [] : [index];
    return choices.includes(index) ? choices.filter(c => c !== index) : [...choices, index].sort((a, b) => a - b);
}

/**
 * Checks a poll definition as the server will; returns an error key
 * (poll.error.*) or "" when valid. now is Unix seconds.
 */
export function validatePoll({ question = "", options = [], closesAt = 0 } = {}, now = Date.now() / 1000) {
    if (!question.trim()) return "poll.error.question";
    if ([...question].length > 300) return "poll.error.questionLong";
    if (options.length < 2) return "poll.error.fewOptions";
    if (options.length > 10) return "poll.error.manyOptions";
    const seen = new Set();
    for (const option of options) {
        const normalized = option.trim().toLowerCase();
        if (!normalized) return "poll.error.emptyOption";
        if ([...option].length > 80) return "poll.error.longOption";
        if (seen.has(normalized)) return "poll.error.duplicate";
        seen.add(normalized);
    }
    if (!(closesAt > now) || closesAt > now + POLL_DURATIONS.week) return "poll.error.duration";
    return "";
}

/** Seconds left until closesAt split for display: {days, hours, minutes, seconds, total}. */
export function pollRemaining(closesAt, now = Date.now() / 1000) {
    const total = Math.max(0, Math.floor(closesAt - now));
    return { total, days: Math.floor(total / 86400), hours: Math.floor((total % 86400) / 3600), minutes: Math.floor((total % 3600) / 60), seconds: total % 60 };
}

/** Whether a poll is closed: the server said so, or its time ran out. */
export function pollClosed(state, closesAt, now = Date.now() / 1000) {
    return !!state?.closed || (closesAt > 0 && now >= closesAt);
}

/** Keeps the newer of two poll states (versions grow with every change). */
export function newerPollState(current, next) {
    if (!next) return current;
    if (!current || (next.version || 0) >= (current.version || 0)) return next;
    return current;
}

/**
 * Runs a server action, retrying a few times when the account's chat rate
 * limit (shared by messages, polls and thread actions) refuses it: a
 * quick series of taps should land, not fail with "slow down".
 */
export async function withRateRetry(action, { attempts = 4, delay = 1000, wait = ms => new Promise(resolve => setTimeout(resolve, ms)) } = {}) {
    for (let attempt = 1; ; attempt++) {
        try {
            return await action();
        } catch (error) {
            if (attempt >= attempts || !/rate limit|slow down/i.test(error?.message || "")) throw error;
            await wait(delay * attempt);
        }
    }
}
