import { describe, it } from "node:test";
import assert from "node:assert/strict";
import {
    MAX_SCENES, MAX_SOURCES, TRANSITION_DEFAULT_MS, StudioStateError, createStudio, parseStudio, serializeStudio,
    addScene, removeScene, renameScene, selectScene, setTransition, addSource, updateSource, removeSource, moveSource,
    take, captureRefs, assetRefs, selectedScene, cleanText, clampTransition,
} from "../src/studio-state.js";

const code = expected => error => error instanceof StudioStateError && error.code === expected;

describe("studio state", () => {
    it("caps scenes at 8 and sources at 8 per scene", () => {
        let studio = createStudio();
        for (let i = 1; i < MAX_SCENES; i++) studio = addScene(studio, `S${i}`);
        assert.equal(studio.scenes.length, 8);
        assert.throws(() => addScene(studio, "ninth"), code("scene_limit"));
        const scene = studio.scenes[0].id;
        for (let i = 0; i < MAX_SOURCES; i++) studio = addSource(studio, scene, { kind: "text", text: `t${i}` }).studio;
        assert.equal(studio.scenes[0].sources.length, 8);
        assert.throws(() => addSource(studio, scene, { kind: "text" }), code("source_limit"));
    });

    it("keeps the last scene and moves the selection on delete", () => {
        let studio = createStudio();
        assert.throws(() => removeScene(studio, studio.selected), code("last_scene"));
        studio = addScene(studio, "B");
        const b = studio.selected;
        studio = removeScene(studio, b);
        assert.equal(studio.scenes.length, 1);
        assert.equal(studio.selected, studio.scenes[0].id);
        assert.throws(() => selectScene(studio, "sc99"), code("scene_missing"));
    });

    it("bounds geometry, crop, transition and text", () => {
        let { studio, source } = addSource(createStudio(), "sc1", { kind: "text", text: "a\u0000b‮c\r\nd".padEnd(400, "x"), color: "red", size: 900, x: -4, y: 2, w: 3, h: 0, crop: { l: 0.9, r: -1 } });
        assert.equal(source.text.startsWith("abc\nd"), true);
        assert.equal([...source.text].length, 280);
        assert.equal(source.color, "#edf4f8");
        assert.equal(source.size, 160);
        assert.deepEqual([source.x, source.y, source.w, source.h], [0, 0.98, 1, 0.02]);
        studio = updateSource(studio, "sc1", source.id, { x: 0.9, w: 0.5, crop: { l: 0.7, t: 0.2 } });
        const updated = selectedScene(studio).sources[0];
        assert.equal(updated.w, 0.5);
        assert.equal(updated.x, 0.5, "kept inside the canvas");
        assert.deepEqual(updated.crop, { l: 0.45, t: 0.2, r: 0, b: 0 });
        assert.deepEqual(clampTransition({ kind: "fade", ms: 50 }), { kind: "fade", ms: 100 });
        assert.deepEqual(clampTransition({ kind: "fade", ms: 9999 }), { kind: "fade", ms: 1500 });
        assert.deepEqual(clampTransition({}), { kind: "fade", ms: TRANSITION_DEFAULT_MS });
        assert.deepEqual(setTransition(studio, { kind: "cut" }).transition, { kind: "cut", ms: 300 });
        assert.equal(cleanText("<script>alert(1)</script>"), "<script>alert(1)</script>", "text stays inert data; it is drawn, never parsed");
    });

    it("refuses unknown kinds, remote image references and kind/asset changes", () => {
        assert.throws(() => addSource(createStudio(), "sc1", { kind: "webpage", url: "https://example.com" }), code("source_invalid"));
        assert.throws(() => addSource(createStudio(), "sc1", { kind: "image", asset: "https://example.com/a.png" }), code("source_invalid"));
        const { studio, source } = addSource(createStudio(), "sc1", { kind: "image", asset: "a-0123456789abcdef0123456789abcdef" });
        const changed = updateSource(studio, "sc1", source.id, { kind: "text", asset: "a-ffffffffffffffffffffffffffffffff", url: "x" });
        const after = selectedScene(changed).sources[0];
        assert.equal(after.kind, "image");
        assert.equal(after.asset, source.asset);
        assert.equal("url" in after, false);
    });

    it("orders z-layers", () => {
        let studio = createStudio();
        const ids = [];
        for (const name of ["a", "b", "c"]) { const added = addSource(studio, "sc1", { kind: "text", name }); studio = added.studio; ids.push(added.source.id); }
        studio = moveSource(studio, "sc1", ids[0], "up");
        assert.deepEqual(selectedScene(studio).sources.map(s => s.name), ["b", "a", "c"]);
        studio = moveSource(studio, "sc1", ids[0], "top");
        assert.deepEqual(selectedScene(studio).sources.map(s => s.name), ["b", "c", "a"]);
        studio = moveSource(studio, "sc1", ids[0], "bottom");
        studio = moveSource(studio, "sc1", ids[0], "down");
        assert.deepEqual(selectedScene(studio).sources.map(s => s.name), ["a", "b", "c"]);
        assert.throws(() => moveSource(studio, "sc1", ids[0], "sideways"), code("direction_invalid"));
    });

    it("takes an immutable program snapshot that draft edits cannot reach", () => {
        let { studio, source } = addSource(createStudio(), "sc1", { kind: "display", capture: "c1", name: "Screen" });
        const program = take(studio);
        assert.equal(Object.isFrozen(program), true);
        assert.equal(Object.isFrozen(program.sources[0].crop), true);
        studio = updateSource(studio, "sc1", source.id, { x: 0.3, w: 0.5, name: "Edited" });
        studio = removeSource(studio, "sc1", source.id);
        assert.equal(selectedScene(studio).sources.length, 0);
        assert.equal(program.sources[0].name, "Screen");
        assert.equal(program.sources[0].capture, "c1");
        assert.throws(() => { program.sources[0].x = 0.5; }, TypeError);
        // Shared capture references: the program still needs c1 after the
        // draft source is gone.
        assert.deepEqual([...captureRefs(studio.scenes)], []);
        assert.deepEqual([...captureRefs([program])], ["c1"]);
    });

    it("persists metadata only: no capture handles, and restore needs recapture", () => {
        let studio = addSource(createStudio(), "sc1", { kind: "display", capture: "c7", surface: "window", audio: true, name: "Game" }).studio;
        studio = addSource(studio, "sc1", { kind: "camera", capture: "c8" }).studio;
        studio = addSource(studio, "sc1", { kind: "image", asset: "a-0123456789abcdef0123456789abcdef" }).studio;
        studio.scenes[0].sources[0].stream = { fake: "MediaStream" };
        const saved = serializeStudio(studio);
        assert.equal(saved.includes("c7"), false);
        assert.equal(saved.includes("capture"), false);
        assert.equal(saved.includes("stream"), false);
        const restored = parseStudio(saved);
        assert.deepEqual([...captureRefs(restored.scenes)], []);
        assert.equal(restored.scenes[0].sources[0].surface, "window");
        assert.equal(restored.scenes[0].sources[0].audio, true);
        assert.deepEqual([...assetRefs(restored)], ["a-0123456789abcdef0123456789abcdef"]);
        // New IDs never collide with restored ones.
        const next = addSource(restored, "sc1", { kind: "text" }).source.id;
        assert.equal(restored.scenes[0].sources.some(s => s.id === next), false);
    });

    it("parses untrusted input defensively", () => {
        for (const bad of ["", "{", "null", JSON.stringify({ v: 2 }), "x".repeat(70000), JSON.stringify({ v: 1, scenes: "no" })]) {
            assert.deepEqual(parseStudio(bad), createStudio());
        }
        const many = { v: 1, scenes: Array.from({ length: 20 }, (_, i) => ({ id: `sc${i + 1}`, name: "n", sources: Array.from({ length: 20 }, (_, j) => ({ id: `src${i * 100 + j + 1}`, kind: "text", text: "t" })) })) };
        many.scenes.push({ id: "sc1", name: "duplicate" }, { id: "bad id", name: "x" });
        const parsed = parseStudio(JSON.stringify(many));
        assert.equal(parsed.scenes.length, 8);
        assert.ok(parsed.scenes.every(scene => scene.sources.length === 8));
        assert.equal(parsed.selected, "sc1");
        const renamed = renameScene(parsed, "sc1", "\u0007 Live\nset ");
        assert.equal(renamed.scenes[0].name, "Live set");
    });
});
