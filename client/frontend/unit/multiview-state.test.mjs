import assert from "node:assert/strict";
import { describe, it } from "node:test";
import {
    AUDIO_FOLLOW, DUCK_LEVEL, SPLIT_DEFAULT, SPLIT_MAX, SPLIT_MIN,
    clampSplit, createStageState, gridShape, hotkeyIndex, publisherOf, shareAudioGains, stageKind,
} from "../src/multiview-state.js";

function memoryStorage() {
    const values = new Map();
    return {
        values,
        getItem: key => values.has(key) ? values.get(key) : null,
        setItem: (key, value) => values.set(key, String(value)),
        removeItem: key => values.delete(key),
    };
}

const blocked = {
    getItem() { throw new Error("blocked"); },
    setItem() { throw new Error("blocked"); },
    removeItem() { throw new Error("blocked"); },
};

describe("stage kind and grid shape", () => {
    it("maps the visible tile count to none, single and multiview", () => {
        assert.equal(stageKind(0), "none");
        assert.equal(stageKind(1), "single");
        assert.equal(stageKind(2), "multi");
        assert.equal(stageKind(7), "multi");
    });

    it("fits 2 side by side, 3-4 as 2x2, 5-6 as 3x2 and more as three columns", () => {
        assert.deepEqual(gridShape(1), { cols: 1, rows: 1 });
        assert.deepEqual(gridShape(2), { cols: 2, rows: 1 });
        assert.deepEqual(gridShape(3), { cols: 2, rows: 2 });
        assert.deepEqual(gridShape(4), { cols: 2, rows: 2 });
        assert.deepEqual(gridShape(6), { cols: 3, rows: 2 });
        assert.deepEqual(gridShape(8), { cols: 3, rows: 3 });
    });

    it("derives the publisher from receiver keys, including ids containing a separator", () => {
        assert.equal(publisherOf("alice|screen"), "alice");
        assert.equal(publisherOf("a|b|cam"), "a|b");
        assert.equal(publisherOf("solo"), "solo");
    });
});

describe("layout mode persistence", () => {
    it("defaults to grid and persists the mode per session and server tab", () => {
        const session = memoryStorage();
        let tab = "tab-a";
        const state = createStageState({ session, scope: () => tab });
        assert.equal(state.mode(), "grid");
        assert.equal(state.setMode("focus"), "focus");
        assert.equal(state.mode(), "focus");
        tab = "tab-b";
        assert.equal(state.mode(), "grid", "another server tab keeps its own mode");
        state.setMode("stack");
        tab = "tab-a";
        assert.equal(state.mode(), "focus");
        assert.equal(createStageState({ session, scope: () => "tab-a" }).mode(), "focus", "a new stage in the same session restores it");
    });

    it("rejects unknown modes and ignores corrupted stored values", () => {
        const session = memoryStorage();
        const state = createStageState({ session, scope: () => "t" });
        state.setMode("focus");
        assert.equal(state.setMode("carousel"), "focus");
        session.setItem("noxa.stage.mode:t", "bogus");
        assert.equal(state.mode(), "grid");
    });

    it("works without storage or with storage that throws", () => {
        for (const storage of [undefined, blocked]) {
            const state = createStageState({ session: storage, local: storage });
            assert.equal(state.setMode("stack"), "stack");
            assert.equal(state.mode(), "grid");
            assert.equal(state.theater(), false);
            assert.deepEqual(state.panel(), { tab: "chat", collapsed: false });
            assert.equal(state.split(), SPLIT_DEFAULT);
            assert.equal(state.resetSplit(), SPLIT_DEFAULT);
        }
    });

    it("persists theater and the side panel tab and collapse state", () => {
        const session = memoryStorage();
        const state = createStageState({ session, scope: () => "t" });
        state.setTheater(true);
        assert.equal(state.theater(), true);
        assert.deepEqual(state.setPanel({ tab: "viewers" }), { tab: "viewers", collapsed: false });
        assert.deepEqual(state.setPanel({ collapsed: true }), { tab: "viewers", collapsed: true });
        assert.deepEqual(createStageState({ session, scope: () => "t" }).panel(), { tab: "viewers", collapsed: true });
        assert.deepEqual(state.setPanel({ tab: "anything", collapsed: false }), { tab: "chat", collapsed: false });
    });

    it("clamps and persists the split size across sessions and resets it", () => {
        const local = memoryStorage();
        const state = createStageState({ local });
        assert.equal(state.split(), SPLIT_DEFAULT);
        assert.equal(state.setSplit(0.5), 0.5);
        assert.equal(createStageState({ local }).split(), 0.5);
        assert.equal(state.setSplit(2), SPLIT_MAX);
        assert.equal(state.setSplit(0.01), SPLIT_MIN);
        assert.equal(clampSplit("nonsense"), SPLIT_DEFAULT);
        assert.equal(state.resetSplit(), SPLIT_DEFAULT);
        assert.equal(local.values.size, 0);
    });
});

describe("tile order, focus and pins", () => {
    it("keeps known tiles in place, appends new ones and moves focus off removed tiles", () => {
        const state = createStageState();
        assert.deepEqual(state.setTiles(["a|screen", "b|screen"]), ["a|screen", "b|screen"]);
        assert.equal(state.focus, "a|screen");
        state.setFocus("b|screen");
        assert.deepEqual(state.setTiles(["c|cam", "b|screen", "a|screen"]), ["a|screen", "b|screen", "c|cam"]);
        assert.equal(state.focus, "b|screen");
        state.setTiles(["a|screen", "c|cam"]);
        assert.equal(state.focus, "a|screen", "a removed focus falls back to the first tile");
        state.setTiles([]);
        assert.equal(state.focus, null);
        assert.equal(state.setFocus("ghost"), null, "unknown keys cannot take focus");
    });

    it("leads with pinned tiles and forgets pins of tiles that left", () => {
        const state = createStageState();
        state.setTiles(["a", "b", "c"]);
        assert.equal(state.togglePin("c"), true);
        assert.deepEqual(state.order, ["c", "a", "b"]);
        assert.equal(state.isPinned("c"), true);
        assert.equal(state.togglePin("c"), false);
        assert.equal(state.togglePin("missing"), false);
        state.togglePin("b");
        state.setTiles(["a", "c"]);
        assert.equal(state.isPinned("b"), false);
        state.setTiles(["a", "c", "b"]);
        assert.deepEqual(state.order, ["c", "a", "b"], "unpinning keeps the current position");
    });

    it("swap exchanges a tile with the focused one and focuses it", () => {
        const state = createStageState();
        state.setTiles(["a", "b", "c", "d"]);
        assert.equal(state.swap("c"), "c");
        assert.deepEqual(state.order, ["c", "b", "a", "d"]);
        assert.equal(state.swap("c"), "c");
        assert.deepEqual(state.order, ["c", "b", "a", "d"]);
        assert.equal(state.swap("zzz"), "c");
    });

    it("1-5 select the n-th tile in display order and nothing beyond five or the tile count", () => {
        const state = createStageState();
        state.setTiles(["a", "b", "c", "d", "e", "f"]);
        assert.equal(state.focusIndex(2), "c");
        assert.equal(state.focusIndex(4), "e");
        assert.equal(state.focusIndex(5), null, "a sixth tile has no hotkey");
        assert.equal(state.focus, "e");
        state.setTiles(["a", "b"]);
        assert.equal(state.focusIndex(3), null);
        assert.equal(state.focusIndex(-1), null);
    });
});

describe("focus-following audio ducking", () => {
    it("plays the focused publisher at full gain and ducks every other publisher to 15 %", () => {
        const state = createStageState();
        state.setTiles(["alice|screen", "bob|screen", "carol|cam"]);
        state.setFocus("bob|screen");
        assert.equal(state.audio, AUDIO_FOLLOW);
        assert.equal(state.audioSource(), "bob|screen");
        assert.deepEqual(Object.fromEntries(state.gains()), { alice: DUCK_LEVEL, bob: 1, carol: DUCK_LEVEL });
        assert.equal(DUCK_LEVEL, 0.15);
        state.focusIndex(0);
        assert.deepEqual(Object.fromEntries(state.gains()), { alice: 1, bob: DUCK_LEVEL, carol: DUCK_LEVEL });
    });

    it("an explicit audio choice survives focus changes and falls back to follow when it leaves", () => {
        const state = createStageState();
        state.setTiles(["alice|screen", "bob|screen"]);
        assert.equal(state.setAudio("bob|screen"), "bob|screen");
        state.setFocus("alice|screen");
        assert.equal(state.audioSource(), "bob|screen");
        assert.deepEqual(Object.fromEntries(state.gains()), { alice: DUCK_LEVEL, bob: 1 });
        state.setTiles(["alice|screen", "dave|screen"]);
        assert.equal(state.audio, AUDIO_FOLLOW);
        assert.equal(state.setAudio("not-a-tile"), AUDIO_FOLLOW);
    });

    it("a publisher's camera and screen tiles share one gain; one tile ducks nothing", () => {
        assert.deepEqual(Object.fromEntries(shareAudioGains(["alice|cam", "alice|screen", "bob|screen"], "alice|cam")), { alice: 1, bob: DUCK_LEVEL });
        assert.equal(shareAudioGains(["alice|screen"], "alice|screen").size, 0);
        assert.equal(shareAudioGains(["alice|screen", "bob|screen"], null).size, 0);
        assert.equal(shareAudioGains(["alice|screen", "bob|screen"], "gone|screen").size, 0);
    });
});

describe("focus hotkeys", () => {
    const key = (code, extra = {}) => ({ code, key: code.slice(-1), target: { tagName: "DIV" }, ...extra });

    it("maps number-row and numpad 1-5 to indexes 0-4", () => {
        assert.equal(hotkeyIndex(key("Digit1")), 0);
        assert.equal(hotkeyIndex(key("Digit5")), 4);
        assert.equal(hotkeyIndex(key("Numpad3")), 2);
        assert.equal(hotkeyIndex({ key: "2", target: null }), 1, "layouts without codes fall back to the key");
    });

    it("ignores other digits, modifiers, repeats, handled events and text entry", () => {
        for (const event of [
            key("Digit6"), key("Digit0"), key("KeyA"),
            key("Digit1", { ctrlKey: true }), key("Digit1", { altKey: true }), key("Digit1", { metaKey: true }), key("Digit1", { shiftKey: true }),
            key("Digit1", { repeat: true }), key("Digit1", { defaultPrevented: true }),
            key("Digit1", { target: { tagName: "INPUT" } }), key("Digit1", { target: { tagName: "TEXTAREA" } }),
            key("Digit1", { target: { tagName: "SELECT" } }), key("Digit1", { target: { tagName: "DIV", isContentEditable: true } }),
        ]) assert.equal(hotkeyIndex(event), -1, JSON.stringify(event));
        assert.equal(hotkeyIndex(null), -1);
    });
});
