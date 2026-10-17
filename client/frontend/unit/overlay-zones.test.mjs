import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { readFileSync } from "node:fs";
import {
    GAME_UI_PRESETS, GAME_UI_PRESET_IDS, OVERLAY_ANCHORS, anchorAvailability, anchorOf, anchorPlacement, gameUIPreset,
    nearestFreeAnchor, nominalOverlayBox, placementAllowed, placementOf, placementRect, presetZones, zonesHit,
} from "../src/overlay-zones.js";
import { settingsShellEnglish, settingsShellGerman } from "../src/settings-shell-messages.js";

const free = (preset, box = nominalOverlayBox(80)) => anchorAvailability(preset, box).filter(a => !a.blocked).map(a => a.id);

describe("overlay anchors", () => {
    it("has eight anchors around an excluded centre cell", () => {
        assert.equal(OVERLAY_ANCHORS.length, 8);
        const cells = new Set(OVERLAY_ANCHORS.map(a => a.row * 3 + a.col));
        assert.equal(cells.size, 8);
        assert.equal(cells.has(4), false);
    });

    it("stores anchors as positions the native overlay already understands", () => {
        const go = readFileSync(new URL("../../gaming_overlay.go", import.meta.url), "utf8");
        const valid = go.match(/case "center-left", "top-left", "top-right", "bottom-left", "bottom-right", "custom":/);
        assert.ok(valid, "Go still accepts the six positions");
        for (const anchor of OVERLAY_ANCHORS) {
            const placement = anchorPlacement(anchor.id);
            assert.ok(["center-left", "top-left", "top-right", "bottom-left", "bottom-right", "custom"].includes(placement.position));
            assert.equal(anchorOf(placement), anchor.id, `${anchor.id} round-trips`);
        }
        assert.equal(anchorPlacement("middle"), null);
    });

    it("reads saved placements and treats other custom positions as free placement", () => {
        assert.deepEqual(placementOf({}), { position: "center-left", x: 0, y: 0 });
        assert.deepEqual(placementOf({ gaming_overlay_position: "custom", gaming_overlay_x: 50, gaming_overlay_y: 100 }), { position: "custom", x: 50, y: 100 });
        assert.equal(anchorOf(placementOf({ gaming_overlay_position: "custom", gaming_overlay_x: 50, gaming_overlay_y: 100 })), "bottom-center");
        assert.equal(anchorOf(placementOf({ gaming_overlay_position: "custom", gaming_overlay_x: 10, gaming_overlay_y: 46 })), "");
        assert.deepEqual(placementOf({ gaming_overlay_position: "custom", gaming_overlay_x: 140, gaming_overlay_y: -3 }), { position: "custom", x: 100, y: 0 });
    });

    it("places the overlay like gamingOverlayPlacementSized (margins aside)", () => {
        const box = { w: 20, h: 40 };
        assert.deepEqual(placementRect({ position: "top-left" }, box), { x: 0, y: 0, w: 20, h: 40 });
        assert.deepEqual(placementRect({ position: "top-right" }, box), { x: 80, y: 0, w: 20, h: 40 });
        assert.deepEqual(placementRect({ position: "bottom-right" }, box), { x: 80, y: 60, w: 20, h: 40 });
        assert.deepEqual(placementRect({ position: "center-left" }, box), { x: 0, y: 25, w: 20, h: 40 });
        assert.deepEqual(placementRect({ position: "custom", x: 50, y: 50 }, box), { x: 40, y: 30, w: 20, h: 40 });
        assert.deepEqual(placementRect({ position: "center-left" }, { w: 20, h: 100 }).y, 0, "never above the screen");
    });

    it("sizes the nominal overlay with the native formula", () => {
        const box = nominalOverlayBox(80);
        assert.ok(Math.abs(box.w - (88 + 6 * 24) * 0.8 / 960 * 100) < 1e-9);
        assert.ok(Math.abs(box.h - 256 * 0.8 / 540 * 100) < 1e-9);
        assert.ok(nominalOverlayBox(200).h > box.h);
        assert.deepEqual(nominalOverlayBox(10), nominalOverlayBox(75), "scale is clamped like the setting");
        assert.ok(nominalOverlayBox(200, 8).h <= 100);
    });
});

describe("game UI presets", () => {
    it("matches the reference: MMO leaves 4 of 8 anchors, middle left among them", () => {
        assert.deepEqual(free("mmo").sort(), ["bottom-right", "center-left", "center-right", "top-center"]);
        const blocked = anchorAvailability("mmo", nominalOverlayBox(80)).filter(a => a.blocked);
        assert.deepEqual(Object.fromEntries(blocked.map(a => [a.id, a.blockedBy])), {
            "top-left": ["unitFrames"], "top-right": ["map"], "bottom-left": ["gameChat"], "bottom-center": ["actionBars"],
        });
    });

    it("leaves free anchors in every preset and all eight without one", () => {
        assert.equal(free("none").length, 8);
        assert.equal(free("shooter").length, 4);
        assert.equal(free("moba").length, 3);
        for (const id of GAME_UI_PRESET_IDS) assert.ok(free(id).length > 0, id);
    });

    it("keeps zones on screen and translated", () => {
        for (const [id, zones] of Object.entries(GAME_UI_PRESETS)) {
            for (const zone of zones) {
                assert.ok(zone.x >= 0 && zone.y >= 0 && zone.x + zone.w <= 100 && zone.y + zone.h <= 100, `${id}/${zone.id}`);
                assert.ok(settingsShellEnglish[`overlay.zone.${zone.id}`] && settingsShellGerman[`overlay.zone.${zone.id}`], zone.id);
            }
            for (const key of [`overlay.preset.${id}`, `overlay.presetShort.${id}`]) {
                assert.ok(settingsShellEnglish[key] && settingsShellGerman[key], key);
            }
        }
        for (const anchor of OVERLAY_ANCHORS) assert.ok(settingsShellEnglish[`overlay.anchor.${anchor.id}`] && settingsShellGerman[`overlay.anchor.${anchor.id}`]);
    });

    it("accepts exactly the presets Go validates", () => {
        const go = readFileSync(new URL("../../settings.go", import.meta.url), "utf8");
        const accepted = go.match(/switch s\.GamingOverlayGameUI \{\s*case ([^:]+):/)[1].match(/"([a-z]*)"/g).map(v => v.slice(1, -1)).filter(Boolean);
        assert.deepEqual(accepted.sort(), [...GAME_UI_PRESET_IDS].sort());
        assert.equal(gameUIPreset("rts"), "none");
        assert.equal(gameUIPreset(undefined), "none");
        assert.equal(presetZones("unknown"), GAME_UI_PRESETS.none);
    });
});

describe("no-go placement", () => {
    const box = nominalOverlayBox(80);

    it("rejects drops onto hatched zones only while avoidance is on", () => {
        const chat = { position: "bottom-left", x: 0, y: 0 };
        assert.equal(placementAllowed(chat, "mmo", box), false);
        assert.equal(placementAllowed(chat, "mmo", box, false), true);
        assert.equal(placementAllowed(chat, "none", box), true);
        assert.deepEqual(zonesHit(placementRect(chat, box), presetZones("mmo")).map(z => z.id), ["gameChat"]);
    });

    it("snaps to the closest free anchor", () => {
        assert.equal(nearestFreeAnchor({ position: "bottom-left" }, "mmo", box), "center-left");
        assert.equal(nearestFreeAnchor({ position: "top-right" }, "mmo", box), "center-right");
        assert.equal(nearestFreeAnchor({ position: "custom", x: 60, y: 100 }, "mmo", box), "bottom-right");
        assert.equal(nearestFreeAnchor({ position: "center-left" }, "mmo", box), "center-left");
    });

    it("reports when a preset blocks every anchor", () => {
        const huge = { w: 100, h: 100 };
        assert.equal(free("mmo", huge).length, 0);
        assert.equal(nearestFreeAnchor({ position: "center-left" }, "mmo", huge), "");
    });
});
