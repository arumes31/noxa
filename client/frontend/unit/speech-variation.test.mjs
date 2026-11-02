import assert from "node:assert/strict";
import { test } from "node:test";
import { readFileSync } from "node:fs";
import { VARIED_SPEECH_EVENTS, speechVariantCount, presenceSpeechKey, variedSpeechClip } from "../src/speech-variation.js";
import { SpeechQueue } from "../src/speech-queue.js";
import { SPEECH_ASSETS } from "../src/speech-catalog.js";

test("each presence event selects the same clip for all observers independent of local state", () => {
    const member = { client_id: "session-a", presence_token: "opaque-membership-a", nickname: "Alice", channel_id: 8 };
    const observers = [member, { ...member, nickname: "Local alias", role: "guest" }, structuredClone(member)];
    for (const event of VARIED_SPEECH_EVENTS) {
        const clips = observers.map(client => variedSpeechClip(event, presenceSpeechKey(client, 8)));
        assert.equal(new Set(clips).size, 1);
        const choices = new Set(Array.from({ length: 200 }, (_, i) => variedSpeechClip(event, presenceSpeechKey({ ...member, presence_token: "move-" + i }, 8))));
        assert.equal(choices.size, speechVariantCount(event), event);
        for (const language of ["de", "en"]) {
            const assets = [...choices].map(id => SPEECH_ASSETS[language][id]);
            assert.ok(assets.every(Boolean), `${event}/${language}: all real assets required`);
            assert.equal(new Set(assets.map(clip => clip.transcript)).size, speechVariantCount(event), `${event}/${language}: distinct texts`);
        }
    }
});

test("missing event context preserves legacy recordings and no arbitrary clip can be selected", () => {
    assert.equal(presenceSpeechKey(null, 8), "");
    for (const key of [null, undefined, 123, ""]) assert.equal(variedSpeechClip("user_join", key), "user_join");
    assert.equal(variedSpeechClip("banned", "one"), "banned");
});

test("different observers play the same recorded voice and share the base event policy", () => {
    const eventKey = presenceSpeechKey({ client_id: "someone", presence_token: "one" }, 8);
    const played = [];
    for (const activeTabID of ["observer-a", "observer-b", "observer-c"]) {
        const state = { activeTabID, serverGeneration: activeTabID.length, settings: { language: "de" } };
        const queue = new SpeechQueue({ engine: { play: id => { played.push(id); return true; }, active: [] },
            assets: SPEECH_ASSETS, getState: () => state, isDND: () => false });
        assert.equal(queue.enqueue("user_join", { eventKey, delay: 0 }), true);
        assert.equal(queue.current.event, "user_join");
        assert.equal(queue.enqueue("user_join", { eventKey: "another-move", delay: 0 }), false, "variants cannot bypass cooldown");
        state.settings.speech_events = { user_join: false };
        assert.equal(queue.allowed("user_join", state.settings, false), false);
    }
    assert.equal(played.length, 3); assert.equal(new Set(played).size, 1);
});

test("new join and leave recordings cover five character voices in both languages", () => {
    const metrics = JSON.parse(readFileSync(new URL("../src/assets/speech/metrics.json", import.meta.url)));
    for (const language of ["de", "en"]) for (const event of ["user_join", "user_leave"]) {
        const recordings = Array.from({ length: 10 }, (_, i) => metrics[`${language}/${event}_v${i + 10}`]);
        assert.ok(recordings.every(Boolean));
        assert.deepEqual([...new Set(recordings.map(recording => recording.voice))].sort(), ["DWARF1", "ELF1", "GNOME1", "GOBLIN1", "TROLL1"]);
        assert.equal(new Set(recordings.map(recording => recording.sha256)).size, 10);
    }
});
