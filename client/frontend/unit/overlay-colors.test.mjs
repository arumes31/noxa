import { test } from "node:test";
import assert from "node:assert/strict";
import { createOverlayColors, overlayColorPreferences } from "../src/overlay-colors.js";

test("all other channel members get distinct blue colors even while silent", () => {
    const members = Array.from({ length: 100 }, (_, i) => ({ id: `member-${i}` }));
    const colors = createOverlayColors();
    const assigned = colors("server/channel", members);
    assert.equal(new Set(assigned.values()).size, 100);
    for (const color of assigned.values()) {
        assert.match(color, /^#[0-9a-f]{6}$/);
        const [r,g,b] = color.slice(1).match(/../g).map(value => parseInt(value,16));
        assert.ok(b>=g && g>r, color);
    }
    assert.deepEqual(colors("server/channel", members.toReversed()), assigned);
    const newcomers = colors("server/channel", [...members, {id:"new-member"}]);
    for (const {id} of members) assert.equal(newcomers.get(id), assigned.get(id));
});

test("account identities retain their color across peer reconnects and speaking changes", () => {
    const colors = createOverlayColors();
    const first = colors("channel", [{id:"account-a"},{id:"account-b"}]);
    assert.equal(colors("channel", [{id:"account-b"},{id:"account-a"}]).get("account-a"), first.get("account-a"));
    assert.equal(colors("other-channel", [{id:"account-a"}]).size, 1);
});

test("colors remain unique beyond the first palette and at the server member limit", () => {
    const members = Array.from({length:100000}, (_, i) => ({id:`large-${i}`}));
    for (const [start,end] of [[195,235],[180,200],[220,240]]) {
        const colors = createOverlayColors()("large-channel", members,{gaming_overlay_blue_start:start,gaming_overlay_blue_end:end});
        assert.equal(new Set(colors.values()).size, members.length);
        for (const color of colors.values()) {
            const [r,g,b] = color.slice(1).match(/../g).map(value => parseInt(value,16));
            const hue=240-60*(g-r)/(b-r);
            assert.ok(b>=g && g>r && hue>=start && hue<=end, color);
        }
    }
});

test("self is always green by default and saved colors can configure self and the blue range", () => {
    const colors=createOverlayColors(), members=[{id:"self"},{id:"other"},{id:"quiet"}];
    const first=colors("channel",members,{},"self");
    assert.equal(first.get("self"),"#43c798");
    const configured=colors("channel",members,{gaming_overlay_self_color:"#FF8800",gaming_overlay_blue_start:180,gaming_overlay_blue_end:200},"self");
    assert.equal(configured.get("self"),"#ff8800");
    for(const id of ["other","quiet"]) {
        const [r,g,b]=configured.get(id).slice(1).match(/../g).map(value=>parseInt(value,16));
        assert.ok(240-60*(g-r)/(b-r)<=200);
    }
    assert.equal(colors("channel",members.toReversed(),{},"self").get("self"),"#43c798");
    assert.deepEqual(overlayColorPreferences({gaming_overlay_self_color:"bad",gaming_overlay_blue_start:0,gaming_overlay_blue_end:0}),
        {self:"#43c798",start:195,end:235});
    assert.deepEqual(overlayColorPreferences({gaming_overlay_self_color:"#ffffff",gaming_overlay_blue_start:999,gaming_overlay_blue_end:-1}),
        {self:"#ffffff",start:220,end:240});
});
