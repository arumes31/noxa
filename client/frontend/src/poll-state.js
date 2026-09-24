const prefix = "[noxa-poll:v1]";

export function parsePoll(body) {
    if (typeof body !== "string" || !body.startsWith(prefix)) return null;
    try {
        const poll = JSON.parse(body.slice(prefix.length));
        if (!poll || typeof poll.question !== "string" || !Array.isArray(poll.options) || poll.options.length < 2 || poll.options.length > 10 ||
            poll.options.some(option => typeof option !== "string" || [...option].length > 80) || [...poll.question].length > 300 ||
            typeof poll.multiple !== "boolean" || !Number.isSafeInteger(poll.closes_at)) return null;
        return poll;
    } catch { return null; }
}

export function pollBody(poll) { return prefix + JSON.stringify(poll); }

export function validatePoll(poll, now = Math.floor(Date.now() / 1000)) {
    if (!parsePoll(pollBody(poll)) || !poll.question.trim() || poll.options.some(option => !option.trim()) ||
        new Set(poll.options.map(option => option.trim().toLowerCase())).size !== poll.options.length ||
        poll.closes_at <= now || poll.closes_at > now + 7 * 86400) return "poll.invalid";
    return "";
}

export function togglePollChoice(choices, choice, multiple) {
    if (choices.includes(choice)) return choices.filter(value => value !== choice);
    return multiple ? [...choices, choice].sort((a, b) => a - b) : [choice];
}
