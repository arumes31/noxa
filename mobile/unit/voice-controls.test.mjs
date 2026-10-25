import test from "node:test";
import assert from "node:assert/strict";
import { Msg } from "../src/core/frame.js";
import * as settings from "../src/core/settings.js";
import { Voice } from "../src/core/voice.js";

function fakeSession() {
    const requests = [];
    const self = { client_id: "me", unique_id: "u-me", channel_id: 5 };
    const session = Object.assign(new EventTarget(), {
        clientID: "me",
        capabilities: new Set(),
        clients: () => [self, { client_id: "other", unique_id: "u-other", channel_id: 5 }],
        channels: () => [{ id: 5, clients: [self] }],
        self: () => self,
        serverInfo: async () => ({ echo_channel_id: 0 }),
        conn: {
            open: true,
            on: () => () => {},
            send: (type, body) => requests.push({ type, body, sent: true }),
            request: async (type, body) => {
                requests.push({ type, body });
                if (type === Msg.WhisperSet) return { operation: Msg.WhisperSet, active: body.active };
                if (type === Msg.PrioritySpeaker) return { operation: Msg.PrioritySpeaker, active: body.active };
                if (type === Msg.VoiceQueueRequest) return { action: body.action, channel_id: body.channel_id };
                return {};
            },
        },
    });
    return { session, requests, self };
}

test("whisper, reply, priority and queue send the protocol frames", async t => {
    settings._resetCache();
    const { session, requests, self } = fakeSession();
    const voice = new Voice(session);
    t.after(() => voice.close());
    voice.channelID = 5;

    voice.setWhisperTargets({ uniqueIDs: ["u-other", "u-other"], channelIDs: [7] });
    assert.deepEqual(voice.whisperTargets, { uniqueIDs: ["u-other"], channelIDs: [7] });
    await voice.startWhisper();
    assert.equal(voice.whispering, true);
    assert.deepEqual(requests.at(-1), { type: Msg.WhisperSet, body: { ack_requested: true, active: true, unique_ids: ["u-other"], channel_ids: [7] } });
    await voice.stopWhisper();
    assert.equal(voice.whispering, false);
    assert.equal(requests.at(-1).body.active, false);

    // An incoming whisper remembers who to reply to.
    session.dispatchEvent(new CustomEvent("whisper", { detail: { from_client_id: "c9", from_unique_id: "u-9", from_nickname: "Nine", speaking: true } }));
    assert.equal(voice.whisperers.has("c9"), true);
    assert.equal(voice.lastWhisperer.uniqueID, "u-9");
    await voice.replyWhisper();
    assert.deepEqual(requests.at(-1).body.unique_ids, ["u-9"]);
    session.dispatchEvent(new CustomEvent("whisper", { detail: { from_client_id: "c9", speaking: false } }));
    assert.equal(voice.whisperers.has("c9"), false);

    await voice.setPrioritySpeaker(true);
    assert.equal(self.priority_speaker, true);
    assert.equal(voice.prioritySpeaker, true);
    session.dispatchEvent(new CustomEvent("server-event", { detail: { type: "priority_speaker_changed", data: { client_id: "me", active: false } } }));
    assert.equal(voice.prioritySpeaker, false);

    await voice.queue("raise");
    assert.deepEqual(requests.at(-1), { type: Msg.VoiceQueueRequest, body: { action: "raise", channel_id: 5, client_id: "" } });

    // A moderator calling on this member raises "called" once.
    let called = 0;
    voice.addEventListener("called", () => called++);
    self.hand_called = true;
    session.dispatchEvent(new CustomEvent("snapshot"));
    session.dispatchEvent(new CustomEvent("snapshot"));
    assert.equal(called, 1);
    voice.channelID = 0;
});

test("settings drive push-to-talk, data saver and carry-over state", t => {
    settings._resetCache();
    const { session } = fakeSession();
    const voice = new Voice(session, { muted: true, deafened: false, lastWhisperer: { uniqueID: "u-1", nickname: "One" } });
    t.after(() => voice.close());
    assert.equal(voice.muted, true, "mute survives a reconnect");
    assert.equal(voice.lastWhisperer.nickname, "One");
    voice.setPushToTalk(true);
    assert.equal(settings.get("activation"), "ptt");
    assert.equal(voice.pushToTalk, true);
    voice.setPushToTalk(false);
    assert.equal(settings.get("activation"), "continuous");
    assert.equal(voice.receiveLayer(), "high");
    settings.set({ lowBandwidth: true });
    assert.equal(voice.lowBandwidth, true);
    assert.equal(voice.receiveLayer(), "low");
    settings.set({ lowBandwidth: false, receiveQuality: "medium" });
    assert.equal(voice.receiveLayer(), "mid");
    settings.reset();
});
