const hex = rgb => "#" + rgb.map(channel => channel.toString(16).padStart(2,"0")).join("");
const clamp = (value, fallback, min, max) => Math.min(max,Math.max(min,Number.isFinite(value) && value !== 0 ? Math.round(value) : fallback));
export function overlayColorPreferences(settings = {}) {
    const start = clamp(settings.gaming_overlay_blue_start,195,180,220);
    const end = Math.max(start+20,clamp(settings.gaming_overlay_blue_end,235,200,240));
    return {self:/^#[0-9a-f]{6}$/i.test(settings.gaming_overlay_self_color || "") ? settings.gaming_overlay_self_color.toLowerCase() : "#43c798",start,end};
}
export const overlayBlueColor = hue => hex([54,Math.round(224-170*(hue-180)/60),224]);
const makePalette = ({start,end}) => Array.from({length:240}, (_,index) => {
    const hue = (start+.8+(index%40)*(end-start-1.6)/39)/60;
    const saturation = .64 + Math.floor(index/40)%3*.12, value = .78 + Math.floor(index/120)*.18;
    const c = value*saturation, x = c*(1-Math.abs(hue%2-1)), m = value-c;
    return hex([m,x+m,c+m].map(channel=>Math.round(channel*255)));
});
const hash = id => { let value = 2166136261; for (const char of id) value = Math.imul(value^char.codePointAt(0),16777619); return value>>>0; };

// Reserve quiet members' colors too; speaking order cannot recolor participants.
export function createOverlayColors() {
    let current = "", assigned = new Map(), cursor = 0, palette = [];
    return (scope, members, settings = {}, selfID = "") => {
        const preferences = overlayColorPreferences(settings);
        const key = JSON.stringify([scope,preferences,selfID]);
        if (key !== current) { current = key; assigned = new Map(); cursor = 0; palette = makePalette(preferences); }
        const ids = new Set(members.map(member => String(member.id || "")).filter(Boolean));
        for (const id of assigned.keys()) if (!ids.has(id)) assigned.delete(id);
        if (ids.has(selfID)) assigned.set(selfID,preferences.self);
        const used = new Set(assigned.values());
        for (const id of [...ids].sort()) {
            if (assigned.has(id)) continue;
            const start = hash(id)%palette.length;
            let color;
            if (used.size < palette.length) for (let step=0; step<palette.length; step++) {
                const candidate = palette[(start+step)%palette.length];
                if (!used.has(candidate)) { color = candidate; break; }
            }
            // The RGB space supports the server's 100,000-member limit even
            // in the narrowest allowed blue range, without recoloring peers.
            while (!color) {
                const index = cursor++ * 104729 % (96*192*160);
                const r = Math.floor(index/(192*160)), g = 32 + Math.floor(index/160)%192, b = 96 + index%160;
                if (b < g || g <= r) continue;
                const hue = 240-60*(g-r)/(b-r);
                if (hue < preferences.start || hue > preferences.end) continue;
                const candidate = hex([r,g,b]);
                if (!used.has(candidate)) color = candidate;
            }
            assigned.set(id,color); used.add(color);
        }
        return new Map(assigned);
    };
}
