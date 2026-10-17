// Placement aid for the fullscreen voice overlay settings page.
//
// Game UI presets describe where a typical game draws its own HUD ("no-go
// zones"). They only constrain where the settings page lets you put the
// overlay; the native overlay still receives the same position, x and y
// preferences as before and knows nothing about zones.
//
// Every coordinate here is a percentage of the monitor (0–100).

// The eight speaker anchors of the 3×3 picker. The centre cell is never an
// anchor: a voice overlay in the middle of a game always covers gameplay.
// Anchors reuse the existing named positions where one exists; the others are
// stored as a custom position so older clients still place them correctly.
export const OVERLAY_ANCHORS = Object.freeze([
    { id: "top-left", row: 0, col: 0, position: "top-left" },
    { id: "top-center", row: 0, col: 1, position: "custom", x: 50, y: 0 },
    { id: "top-right", row: 0, col: 2, position: "top-right" },
    { id: "center-left", row: 1, col: 0, position: "center-left" },
    { id: "center-right", row: 1, col: 2, position: "custom", x: 100, y: 50 },
    { id: "bottom-left", row: 2, col: 0, position: "bottom-left" },
    { id: "bottom-center", row: 2, col: 1, position: "custom", x: 50, y: 100 },
    { id: "bottom-right", row: 2, col: 2, position: "bottom-right" },
]);

const zone = (id, x, y, w, h) => Object.freeze({ id, x, y, w, h });

// Zone ids double as i18n keys (overlay.zone.<id>). Rectangles follow the
// common default HUD layout of each genre.
export const GAME_UI_PRESETS = Object.freeze({
    none: Object.freeze([]),
    mmo: Object.freeze([
        zone("unitFrames", 0, 0, 32, 12),
        zone("map", 86, 0, 14, 24),
        zone("gameChat", 0, 70, 30, 30),
        zone("actionBars", 31, 90, 38, 10),
    ]),
    shooter: Object.freeze([
        zone("minimap", 0, 0, 18, 24),
        zone("killFeed", 76, 0, 24, 18),
        zone("health", 0, 86, 26, 14),
        zone("ammo", 76, 84, 24, 16),
    ]),
    moba: Object.freeze([
        zone("scoreboard", 34, 0, 32, 8),
        zone("gameChat", 0, 62, 26, 24),
        zone("abilities", 30, 84, 40, 16),
        zone("map", 80, 70, 20, 30),
    ]),
});

export const GAME_UI_PRESET_IDS = Object.freeze(Object.keys(GAME_UI_PRESETS));

// The preview models the overlay on a logical 960 × 540 desktop so the stack
// stays legible in a small frame; the native size formula is the same one
// gamingOverlayPlacement uses (88 + 24 px per name character wide, 184 px for
// the first speaker plus 72 px per further speaker tall, times the scale).
export const PREVIEW_SCREEN = Object.freeze({ width: 960, height: 540 });

export function nominalOverlayBox(scale = 80, speakers = 2, nameLength = 6) {
    const factor = Math.min(200, Math.max(75, Number(scale) || 80)) / 100;
    const rows = Math.max(1, speakers);
    return {
        w: Math.min(100, (88 + nameLength * 24) * factor / PREVIEW_SCREEN.width * 100),
        h: Math.min(100, (184 + (rows - 1) * 72) * factor / PREVIEW_SCREEN.height * 100),
    };
}

export function gameUIPreset(id) {
    return Object.hasOwn(GAME_UI_PRESETS, id) ? id : "none";
}

export function presetZones(id) {
    return GAME_UI_PRESETS[gameUIPreset(id)];
}

const clampPercent = value => Math.min(100, Math.max(0, Math.round(Number(value) || 0)));

// placementOf reads the saved overlay placement from settings.
export function placementOf(settings = {}) {
    const position = settings.gaming_overlay_position || "center-left";
    return position === "custom"
        ? { position, x: clampPercent(settings.gaming_overlay_x), y: clampPercent(settings.gaming_overlay_y) }
        : { position, x: 0, y: 0 };
}

export function anchorPlacement(anchorId) {
    const anchor = OVERLAY_ANCHORS.find(item => item.id === anchorId);
    if (!anchor) return null;
    return anchor.position === "custom"
        ? { position: "custom", x: anchor.x, y: anchor.y }
        : { position: anchor.position, x: 0, y: 0 };
}

// anchorOf returns the anchor a placement sits on, or "" for a free position.
export function anchorOf(placement) {
    for (const anchor of OVERLAY_ANCHORS) {
        if (anchor.position !== placement.position) continue;
        if (anchor.position !== "custom" || (anchor.x === placement.x && anchor.y === placement.y)) return anchor.id;
    }
    return "";
}

// placementRect mirrors gamingOverlayPlacementSized (margins aside): named
// corners hug the edges, center-left centres the stack on 45 % height and a
// custom position is a percentage of the remaining travel.
export function placementRect(placement, box) {
    const { w, h } = box;
    let x = 100 - w, y = 0;
    if (placement.position.endsWith("left")) x = 0;
    if (placement.position.startsWith("bottom")) y = 100 - h;
    if (placement.position === "center-left") y = 45 - h / 2;
    if (placement.position === "custom") {
        x = (100 - w) * placement.x / 100;
        y = (100 - h) * placement.y / 100;
    }
    x = Math.max(0, x);
    y = Math.max(0, y);
    return { x, y, w, h };
}

const overlaps = (a, b) => a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;

// zonesHit lists the preset zones a rectangle overlaps.
export function zonesHit(rect, zones) {
    return zones.filter(item => overlaps(rect, item));
}

// anchorAvailability reports, per anchor, which zones block it.
export function anchorAvailability(presetId, box) {
    const zones = presetZones(presetId);
    return OVERLAY_ANCHORS.map(anchor => {
        const blockedBy = zonesHit(placementRect(anchorPlacement(anchor.id), box), zones).map(item => item.id);
        return { id: anchor.id, row: anchor.row, col: anchor.col, blocked: blockedBy.length > 0, blockedBy };
    });
}

// nearestFreeAnchor picks the unblocked anchor closest to a placement, or ""
// when the preset blocks every anchor.
export function nearestFreeAnchor(placement, presetId, box) {
    const from = placementRect(placement, box);
    const centre = rect => [rect.x + rect.w / 2, rect.y + rect.h / 2];
    const [fx, fy] = centre(from);
    let best = "", bestDistance = Infinity;
    for (const anchor of anchorAvailability(presetId, box)) {
        if (anchor.blocked) continue;
        const [ax, ay] = centre(placementRect(anchorPlacement(anchor.id), box));
        const distance = (ax - fx) ** 2 + (ay - fy) ** 2;
        if (distance < bestDistance) { best = anchor.id; bestDistance = distance; }
    }
    return best;
}

// placementAllowed answers whether the overlay may be dropped at placement.
export function placementAllowed(placement, presetId, box, avoidZones = true) {
    if (!avoidZones) return true;
    return zonesHit(placementRect(placement, box), presetZones(presetId)).length === 0;
}
