import assert from "node:assert/strict";
import { test } from "node:test";
import { parsePoll, pollBody, validatePoll, togglePollChoice } from "../src/poll-state.js";

test("poll bodies preserve names as text and reject malformed structures", () => {
    const poll = { question: "Choose <script>", options: ["A", "B"], multiple: false, closes_at: 2000 };
    assert.deepEqual(parsePoll(pollBody(poll)), poll);
    for (const body of ["ordinary", "[noxa-poll:v1]null", '[noxa-poll:v1]{"options":"bad"}', "[noxa-poll:v1]{}"])
        assert.equal(parsePoll(body), null);
    assert.equal(validatePoll(poll, 1000), "");
    assert.notEqual(validatePoll({ ...poll, options: [" A", "a "] }, 1000), "");
    assert.notEqual(validatePoll(poll, 2000), "");
});

test("single choice replaces or clears; multiple choice toggles independently", () => {
    assert.deepEqual(togglePollChoice([0], 1, false), [1]);
    assert.deepEqual(togglePollChoice([1], 1, false), []);
    assert.deepEqual(togglePollChoice([0], 1, true), [0, 1]);
    assert.deepEqual(togglePollChoice([0, 1], 0, true), [1]);
});
